// Package storage handles all database actions
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/logger"
	"github.com/klauspost/compress/zstd"
	"github.com/leporo/sqlf"

	// sqlite - https://gitlab.com/cznic/sqlite
	_ "modernc.org/sqlite"

	// rqlite - https://github.com/rqlite/gorqlite | https://rqlite.io/
	_ "github.com/rqlite/gorqlite/stdlib"
)

var (
	db           *sql.DB
	sqlDriver    string
	dbLastAction atomic.Int64
	dbTasks      *databaseTasks

	// zstd compression encoder & decoder
	dbEncoder    *zstd.Encoder
	dbDecoder, _ = zstd.NewReader(nil)

	temporaryFiles = []string{}
)

type databaseTasks struct {
	cancel  context.CancelFunc
	workers sync.WaitGroup
	signals chan os.Signal
	once    sync.Once
}

func markDatabaseAction() {
	dbLastAction.Store(time.Now().UnixNano())
}

// InitDB will initialise the database
func InitDB() error {
	dbTasks = nil
	if err := config.VerifySharedDatabase(); err != nil {
		return err
	}
	// dbEncoder
	var (
		dsn string
		err error
	)

	if config.Compression > 0 {
		var compression zstd.EncoderLevel
		switch config.Compression {
		case 1:
			compression = zstd.SpeedFastest
		case 2:
			compression = zstd.SpeedDefault
		case 3:
			compression = zstd.SpeedBestCompression
		}
		dbEncoder, err = zstd.NewWriter(nil, zstd.WithEncoderLevel(compression))
		if err != nil {
			return err
		}
		logger.Log().Debugf("[db] storing messages with compression: %s", compression.String())
	} else {
		logger.Log().Debug("[db] storing messages with no compression")
	}

	p := config.Database

	if p == "" {
		// when no path is provided then we create a temporary file
		// which will get deleted on Close(), SIGINT or SIGTERM
		p = fmt.Sprintf("%s-%d.db", path.Join(os.TempDir(), "messagepit"), time.Now().UnixNano())
		// delete the Unix socket file on exit
		AddTempFile(p)
		sqlDriver = "sqlite"
		dsn = p
		logger.Log().Debugf("[db] using temporary database: %s", p)
	} else if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		sqlDriver = "rqlite"
		dsn = p
		logger.Log().Debugf("[db] opening rqlite database %s", p)
	} else {
		p = filepath.Clean(p)
		sqlDriver = "sqlite"
		dsn = fmt.Sprintf("file:%s?cache=shared", p)
		logger.Log().Debugf("[db] opening database %s", p)
	}

	config.Database = p

	if sqlDriver == "sqlite" {
		if !isFile(p) {
			// Shared databases contain captured data and local password hashes.
			mode := os.FileMode(0666)
			if config.SharedAuthEnabled() {
				mode = 0600
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, mode)
			if err != nil {
				return fmt.Errorf("[db] %s", err.Error())
			}
			_ = f.Close()
		}
	}

	db, err = sql.Open(sqlDriver, dsn)
	if err != nil {
		return err
	}

	for i := 1; i < 6; i++ {
		if err := Ping(); err != nil {
			logger.Log().Errorf("[db] %s", err.Error())
			logger.Log().Infof("[db] reconnecting in 5 seconds (attempt %d/5)", i)
			time.Sleep(5 * time.Second)
		} else {
			break
		}
	}

	// prevent "database locked" errors
	// @see https://github.com/mattn/go-sqlite3#faq
	db.SetMaxOpenConns(1)

	if sqlDriver == "sqlite" {
		if config.DisableWAL {
			// disable WAL mode for SQLite, allows NFS mounted DBs
			_, err = db.Exec("PRAGMA journal_mode=DELETE; PRAGMA synchronous=NORMAL;")
		} else {
			// SQLite performance tuning (https://phiresky.github.io/blog/2020/sqlite-performance-tuning/)
			_, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;")
		}
		if err != nil {
			return err
		}
	}

	// create tables if necessary & apply migrations
	if err := dbApplySchemas(); err != nil {
		return err
	}

	LoadTagFilters()

	markDatabaseAction()

	ctx, cancel := context.WithCancel(context.Background())
	tasks := &databaseTasks{cancel: cancel, signals: make(chan os.Signal, 1)}
	dbTasks = tasks
	signal.Notify(tasks.signals, os.Interrupt, syscall.SIGTERM)
	tasks.workers.Add(2)
	go func() {
		defer tasks.workers.Done()
		dbCron(ctx)
	}()
	go func() {
		defer tasks.workers.Done()
		dataMigrations()
	}()
	go func() {
		select {
		case s := <-tasks.signals:
			fmt.Printf("[db] got %s signal, shutting down\n", s)
			Close()
			os.Exit(0)
		case <-ctx.Done():
		}
	}()

	return nil
}

// Tenant applies an optional prefix to the table name
func tenant(table string) string {
	return fmt.Sprintf("%s%s", config.TenantID, table)
}

// Close stops background database work, closes the database, and deletes temporary files.
func Close() {
	closeDatabase := func() {
		if db != nil {
			if err := db.Close(); err != nil {
				logger.Log().Warn("[db] error closing database, ignoring")
			}
		}
		// Allow SQLite to finish writing WAL logs before deleting temporary files.
		time.Sleep(100 * time.Millisecond)
		deleteTempFiles()
	}
	if dbTasks == nil {
		closeDatabase()
		return
	}
	tasks := dbTasks
	tasks.once.Do(func() {
		signal.Stop(tasks.signals)
		tasks.cancel()
		tasks.workers.Wait()
		closeDatabase()
	})
}

// Ping the database connection and return an error if unsuccessful
func Ping() error {
	return db.Ping()
}

// StatsGet returns the total/unread statistics for a mailbox
func StatsGet() MailboxStats {
	var (
		total  = CountTotal()
		unread = CountUnread()
		tags   = GetAllTags()
	)

	markDatabaseAction()

	return MailboxStats{
		Total:  total,
		Unread: unread,
		Tags:   tags,
	}
}

// CountTotal returns the number of emails in the database
func CountTotal() uint64 {
	var total float64 // use float64 for rqlite compatibility

	_ = sqlf.From(tenant("mailbox")).
		Select("COUNT(*)").To(&total).
		QueryRowAndClose(context.TODO(), db)

	return uint64(total)
}

// CountUnread returns the number of emails in the database that are unread.
func CountUnread() uint64 {
	var total float64 // use float64 for rqlite compatibility

	_ = sqlf.From(tenant("mailbox")).
		Select("COUNT(*)").To(&total).
		Where("Read = ?", 0).
		QueryRowAndClose(context.TODO(), db)

	return uint64(total)
}

// CountRead returns the number of emails in the database that are read.
func CountRead() uint64 {
	var total float64 // use float64 for rqlite compatibility

	_ = sqlf.From(tenant("mailbox")).
		Select("COUNT(*)").To(&total).
		Where("Read = ?", 1).
		QueryRowAndClose(context.TODO(), db)

	return uint64(total)
}

// DbSize returns the size of the SQLite database.
func DbSize() uint64 {
	var total sql.NullFloat64 // use float64 for rqlite compatibility

	err := db.QueryRow("SELECT page_count * page_size AS size FROM pragma_page_count(), pragma_page_size()").Scan(&total)

	if err != nil {
		logger.Log().Errorf("[db] %s", err.Error())
	}

	return uint64(total.Float64)
}

// MessageIDExists checks whether a Message-ID exists in the DB
func MessageIDExists(id string) bool {
	var total int

	_ = sqlf.From(tenant("mailbox")).
		Select("COUNT(*)").To(&total).
		Where("MessageID = ?", id).
		QueryRowAndClose(context.TODO(), db)

	return total != 0
}
