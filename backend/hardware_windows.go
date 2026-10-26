//go:build windows

package main

import (
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type memoryStatusEx struct {
	Length                                                                                               uint32
	MemoryLoad                                                                                           uint32
	TotalPhys, AvailPhys, TotalPageFile, AvailPageFile, TotalVirtual, AvailVirtual, AvailExtendedVirtual uint64
}

type hardwareMonitor struct {
	mu                             sync.Mutex
	lastIdle, lastKernel, lastUser uint64
	last                           time.Time
	cpu                            float64
}

func newHardwareMonitor() *hardwareMonitor { return &hardwareMonitor{} }
func filetimeValue(v syscall.Filetime) uint64 {
	return uint64(v.HighDateTime)<<32 | uint64(v.LowDateTime)
}
func (h *hardwareMonitor) Sample() Hardware {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	mem := memoryStatusEx{Length: 64}
	_, _, _ = kernel32.NewProc("GlobalMemoryStatusEx").Call(uintptr(unsafe.Pointer(&mem)))
	var idle, kernel, user syscall.Filetime
	_, _, _ = kernel32.NewProc("GetSystemTimes").Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	i, k, u := filetimeValue(idle), filetimeValue(kernel), filetimeValue(user)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.last.IsZero() && k+u > h.lastKernel+h.lastUser {
		total := (k - h.lastKernel) + (u - h.lastUser)
		busy := total - (i - h.lastIdle)
		if total > 0 {
			h.cpu = float64(busy) * 100 / float64(total)
			if h.cpu < 0 {
				h.cpu = 0
			}
			if h.cpu > 100 {
				h.cpu = 100
			}
		}
	}
	h.lastIdle, h.lastKernel, h.lastUser, h.last = i, k, u, time.Now()
	return Hardware{CPU: h.cpu, MemoryUsed: mem.TotalPhys - mem.AvailPhys, MemoryTotal: mem.TotalPhys, MemoryAvailable: mem.AvailPhys, RefreshSeconds: 2}
}
