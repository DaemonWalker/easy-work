//go:build windows

package main

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

var processJob struct {
	sync.Once
	handle syscall.Handle
	err    error
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                [6]uintptr
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}
type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

func jobHandle() (syscall.Handle, error) {
	processJob.Do(func() {
		kernel := syscall.NewLazyDLL("kernel32.dll")
		h, _, err := kernel.NewProc("CreateJobObjectW").Call(0, 0)
		if h == 0 {
			processJob.err = err
			return
		}
		processJob.handle = syscall.Handle(h)
		info := jobObjectExtendedLimitInformation{}
		info.BasicLimitInformation.LimitFlags = 0x2000
		r, _, err := kernel.NewProc("SetInformationJobObject").Call(h, 9, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
		if r == 0 {
			processJob.err = err
		}
	})
	return processJob.handle, processJob.err
}
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x08000000}
}
func assignProcess(cmd *exec.Cmd) error {
	job, err := jobHandle()
	if err != nil {
		return err
	}
	kernel := syscall.NewLazyDLL("kernel32.dll")
	process, _, err := kernel.NewProc("OpenProcess").Call(0x1F0FFF, 0, uintptr(cmd.Process.Pid))
	if process == 0 {
		return err
	}
	defer syscall.CloseHandle(syscall.Handle(process))
	r, _, err := kernel.NewProc("AssignProcessToJobObject").Call(uintptr(job), process)
	if r == 0 {
		return err
	}
	return nil
}
