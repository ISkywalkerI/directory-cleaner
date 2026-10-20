//go:build windows

package cleaner

import (
	"fmt"
	"syscall"
)

func checkPlatformAccess(path string) error {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return fmt.Errorf("exclusive open: %w", err)
	}
	return syscall.CloseHandle(h)
}
