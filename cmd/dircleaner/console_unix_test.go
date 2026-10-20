//go:build !windows

package main

import (
	"os"
	"os/exec"
)

func configureConsole(cmd *exec.Cmd) {}

func interruptConsole(cmd *exec.Cmd) error {
	return cmd.Process.Signal(os.Interrupt)
}
