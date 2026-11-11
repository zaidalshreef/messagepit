// Command dev-sso-config creates temporary credentials for the local Dex demo.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/bcrypt"
)

const (
	adminID = "messagepit-test-admin"
	userID  = "messagepit-test-user"
)

// Dex v2.46.1 encodes the local connector and user ID in the OIDC subject.
// IDs are fixed, short ASCII values, so their protobuf lengths fit one byte.
func subject(user, connector string) string {
	value := []byte{0x0a, byte(len(user))}
	value = append(value, user...)
	value = append(value, 0x12, byte(len(connector)))
	value = append(value, connector...)
	return base64.RawURLEncoding.EncodeToString(value)
}

func password() (string, string, error) {
	var bytes [24]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", "", err
	}
	value := base64.RawURLEncoding.EncodeToString(bytes[:])
	hash, err := bcrypt.GenerateFromPassword([]byte(value), 12)
	return value, string(hash), err
}

func writeFiles(dir string) error {
	adminPassword, adminHash, err := password()
	if err != nil {
		return err
	}
	userPassword, userHash, err := password()
	if err != nil {
		return err
	}
	adminSubject, userSubject := subject(adminID, "local"), subject(userID, "local")
	config := fmt.Sprintf(`issuer: http://127.0.0.1:18880/dex
storage:
  type: memory
web:
  http: 0.0.0.0:5556
oauth2:
  skipApprovalScreen: true
staticClients:
  - id: messagepit-local
    name: MessagePit local test
    public: true
    redirectURIs:
      - http://127.0.0.1:18825/auth/callback
enablePasswordDB: true
staticPasswords:
  - email: admin@messagepit.test
    username: admin
    userID: %s
    hash: %q
  - email: developer@messagepit.test
    username: developer
    userID: %s
    hash: %q
`, adminID, adminHash, userID, userHash)
	credentials := fmt.Sprintf(`Local development credentials; valid only while this test instance runs.

Administrator
  Email: admin@messagepit.test
  Password: %s
  Subject: %s

Developer
  Email: developer@messagepit.test
  Password: %s
  Subject: %s

An administrator must explicitly add the developer to a group and grant project access.
`, adminPassword, adminSubject, userPassword, userSubject)
	for name, value := range map[string]string{
		"dex.yaml":         config,
		"credentials.txt":  credentials,
		"admin-subject":    adminSubject,
		"allowed-subjects": adminSubject + "," + userSubject,
	} {
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := file.WriteString(value)
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: dev-sso-config DIRECTORY")
		os.Exit(2)
	}
	if err := writeFiles(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "unable to create local sign-in configuration:", err)
		os.Exit(1)
	}
}
