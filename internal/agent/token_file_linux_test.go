//go:build linux

package agent

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReadAgentTokenFileHexUsesOpenedFileSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-token")
	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(path, []byte("safe-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("malicious\nsecond-line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readAgentTokenFileHex(path, func() {
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := hex.EncodeToString([]byte("safe-token")); got != want {
		t.Fatalf("token hex = %q, want opened snapshot %q", got, want)
	}
}

func TestReadAgentTokenFileHexRejectsSymlinkAndFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAgentTokenFileHex(symlink); err == nil {
		t.Fatal("symlink token file accepted")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := ReadAgentTokenFileHex(fifo); err == nil {
		t.Fatal("FIFO token file accepted")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("FIFO rejection blocked for %v", elapsed)
	}
}

func TestReadAgentTokenFileHexValidatesContentAndBounds(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string
		valid   bool
	}{
		{name: "plain", content: []byte("token"), want: "token", valid: true},
		{name: "LF", content: []byte("token\n"), want: "token", valid: true},
		{name: "CRLF", content: []byte("token\r\n"), want: "token", valid: true},
		{name: "NUL", content: []byte("token\x00")},
		{name: "internal LF", content: []byte("to\nken\n")},
		{name: "internal CR", content: []byte("to\rken\n")},
		{name: "empty"},
		{name: "trimmed empty", content: []byte(" \t\n")},
		{name: "oversized", content: []byte(strings.Repeat("x", 16385))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, tt.content, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadAgentTokenFileHex(path)
			if !tt.valid {
				if err == nil {
					t.Fatalf("invalid token accepted as %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if decoded, err := hex.DecodeString(got); err != nil || string(decoded) != tt.want {
				t.Fatalf("decoded token = %q err=%v, want %q", decoded, err, tt.want)
			}
		})
	}
}
