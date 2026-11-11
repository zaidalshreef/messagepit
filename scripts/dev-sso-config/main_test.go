package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubjectMatchesDexEncoding(t *testing.T) {
	// This test vector is also used by Dex's GenSubject test.
	if got := subject("foo", "bar"); got != "CgNmb28SA2Jhcg" {
		t.Fatalf("subject = %q", got)
	}
}

func TestCredentialsArePrivateAndFresh(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	if err := writeFiles(first); err != nil {
		t.Fatal(err)
	}
	if err := writeFiles(second); err != nil {
		t.Fatal(err)
	}
	credentials, err := os.ReadFile(filepath.Join(first, "credentials.txt"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := os.ReadFile(filepath.Join(second, "credentials.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(credentials) == string(other) {
		t.Fatal("generated credentials were reused")
	}
	for _, file := range []string{"dex.yaml", "credentials.txt", "admin-subject", "allowed-subjects"} {
		info, err := os.Stat(filepath.Join(first, file))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s permissions: %v %v", file, info, err)
		}
	}
	config, err := os.ReadFile(filepath.Join(first, "dex.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(credentials), "\n") {
		if _, secret, ok := strings.Cut(line, "Password: "); ok && strings.Contains(string(config), secret) {
			t.Fatal("Dex configuration contains a plaintext password")
		}
	}
}
