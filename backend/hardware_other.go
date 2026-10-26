//go:build !windows

package main

type hardwareMonitor struct{}

func newHardwareMonitor() *hardwareMonitor  { return &hardwareMonitor{} }
func (h *hardwareMonitor) Sample() Hardware { return Hardware{RefreshSeconds: 2} }
