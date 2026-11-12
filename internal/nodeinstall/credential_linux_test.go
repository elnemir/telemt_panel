//go:build linux && !lite

package nodeinstall

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestOpenPrivateSystemdCredential(t *testing.T) {
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		t.Skip("fixture needs a root-owned systemd-style directory")
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(block)
	defer clear(raw)
	dir := t.TempDir()
	credential := filepath.Join(dir, "node-install-ssh")
	if err := os.WriteFile(credential, raw, 0440); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0550); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	if _, err := Open(credential, FileBinary{}); err != nil {
		t.Fatalf("private systemd credential rejected: %v", err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	if _, err := Open(credential, FileBinary{}); err == nil {
		t.Fatal("group-readable ordinary key accepted")
	}

	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(credential, FileBinary{}); err == nil {
		t.Fatal("group-readable key accepted in a public directory")
	}
	if err := os.Chmod(credential, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(credential, FileBinary{}); err != nil {
		t.Fatalf("owner-only key rejected: %v", err)
	}
}
