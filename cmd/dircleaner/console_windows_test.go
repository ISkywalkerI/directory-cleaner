//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func configureConsole(cmd *exec.Cmd) {
	// Isolate console interrupts from the test runner and the user's console.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000010, HideWindow: true} // CREATE_NEW_CONSOLE
}

func interruptConsole(cmd *exec.Cmd) error {
	helper := exec.Command(os.Args[0], "-test.run=^TestWindowsConsoleInterruptHelper$")
	helper.Env = append(os.Environ(), "DIRCLEANER_INTERRUPT_PID="+strconv.Itoa(cmd.Process.Pid))
	if output, err := helper.CombinedOutput(); err != nil {
		return fmt.Errorf("send Ctrl+Break: %w: %s", err, output)
	}
	return nil
}

func TestWindowsConsoleInterruptHelper(t *testing.T) {
	value := os.Getenv("DIRCLEANER_INTERRUPT_PID")
	if value == "" {
		return
	}
	pid, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	kernel := syscall.NewLazyDLL("kernel32.dll")
	_, _, _ = kernel.NewProc("FreeConsole").Call()
	if ok, _, err := kernel.NewProc("AttachConsole").Call(uintptr(pid)); ok == 0 {
		t.Fatal("AttachConsole:", err)
	}
	defer kernel.NewProc("FreeConsole").Call()
	// AttachConsole resets handlers, including Go's runtime handler.
	handler := syscall.NewCallback(func(uint32) uintptr { return 1 })
	if ok, _, err := kernel.NewProc("SetConsoleCtrlHandler").Call(handler, 1); ok == 0 {
		t.Fatal("SetConsoleCtrlHandler:", err)
	}
	if ok, _, err := kernel.NewProc("GenerateConsoleCtrlEvent").Call(syscall.CTRL_BREAK_EVENT, 0); ok == 0 {
		t.Fatal("GenerateConsoleCtrlEvent:", err)
	}
	// Console control handlers run asynchronously.
	time.Sleep(100 * time.Millisecond)
}
