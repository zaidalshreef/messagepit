package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/coreydaley/messagepit/config"
	"github.com/coreydaley/messagepit/internal/storage"
	"github.com/spf13/cobra"
)

var localResetCmd = &cobra.Command{
	Use:   "reset-local-admin <database>",
	Short: "Reset a local administrator password while the server is stopped",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		file := filepath.Clean(args[0])
		info, err := os.Stat(file)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("an existing SQLite database file is required")
		}
		config.Database, config.TenantID, config.LocalAuth = file, config.DBTenantID(config.TenantID), true
		if err := storage.InitDB(); err != nil {
			return err
		}
		defer storage.Close()
		password := config.LocalAdminPassword
		config.LocalAdminPassword = ""
		if err := storage.ResetLocalAdministrator(context.Background(), config.LocalAdminUsername, password); err != nil {
			return fmt.Errorf("could not reset local administrator; set MP_LOCAL_ADMIN_PASSWORD to a new 12–72 byte password: %w", err)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "Local administrator password updated. Existing sessions will require a new sign-in.")
		return err
	},
}

func init() {
	localResetCmd.Flags().StringVar(&config.LocalAdminUsername, "username", config.LocalAdminUsername, "Local administrator username")
	localResetCmd.Flags().StringVar(&config.TenantID, "tenant-id", config.TenantID, "Database tenant ID")
	rootCmd.AddCommand(localResetCmd)
}
