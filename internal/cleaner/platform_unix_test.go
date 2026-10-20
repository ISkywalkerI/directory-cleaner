//go:build !windows

package cleaner

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestUnixAccessAndPermission(t *testing.T) {
	f := fixtureFile(t, t.TempDir(), "file", time.Hour)
	if err := checkPlatformAccess(f.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.Path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(f.Path, 0600) })
	if os.Geteuid() == 0 {
		t.Skip("root bypasses read permission checks")
	}
	if err := checkPlatformAccess(f.Path); err == nil {
		t.Fatal("unreadable file accessible")
	}
}

func TestCollectSkipsFIFO(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(p, 0600); err != nil {
		t.Fatal(err)
	}
	f, total, fail := collect(root)
	if len(f) != 0 || total != 0 || len(fail) != 0 {
		t.Fatalf("%v %d %v", f, total, fail)
	}
}
