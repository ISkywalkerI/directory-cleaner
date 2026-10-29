//go:build windows

package cleaner

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestWindowsLockedFileIsPreserved(t *testing.T) {
	f := fixtureFile(t, t.TempDir(), "locked", time.Hour)
	name, err := syscall.UTF16PtrFromString(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.CloseHandle(h) })
	if err := checkPlatformAccess(f.Path); err == nil {
		t.Fatal("locked file accessible")
	}
	if ok, _ := stable(context.Background(), f.Path, f.Size, f.ModTime, 0); ok {
		t.Fatal("locked file stable")
	}
	if result, err := remove(f.Path, f.Size, f.ModTime, false); result.validated || result.deleted || err == nil {
		t.Fatal("locked file removed")
	}
	if _, err := os.Stat(f.Path); err != nil {
		t.Fatal(err)
	}
}
