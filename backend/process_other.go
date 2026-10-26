//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func configureProcess(cmd *exec.Cmd)    { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func assignProcess(cmd *exec.Cmd) error { return nil }
