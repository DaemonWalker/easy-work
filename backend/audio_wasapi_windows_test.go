//go:build windows

package main

import (
	"syscall"
	"testing"
	"unsafe"
)

func TestWASAPIVTableLayouts(t *testing.T) {
	word := unsafe.Sizeof(uintptr(0))
	client := iAudioClientVtbl{}
	checks := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"GetMixFormat", unsafe.Offsetof(client.GetMixFormat), 8 * word},
		{"GetDevicePeriod", unsafe.Offsetof(client.GetDevicePeriod), 9 * word},
		{"Start", unsafe.Offsetof(client.Start), 10 * word},
		{"Stop", unsafe.Offsetof(client.Stop), 11 * word},
		{"Reset", unsafe.Offsetof(client.Reset), 12 * word},
		{"SetEventHandle", unsafe.Offsetof(client.SetEventHandle), 13 * word},
		{"GetService", unsafe.Offsetof(client.GetService), 14 * word},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("IAudioClient.%s offset=%d want=%d", check.name, check.got, check.want)
		}
	}
	capture := iAudioCaptureClientVtbl{}
	if got := unsafe.Offsetof(capture.GetNextPacketSize); got != 5*word {
		t.Fatalf("IAudioCaptureClient.GetNextPacketSize offset=%d want=%d", got, 5*word)
	}
	wantGUID := syscall.GUID{Data1: 0xc8adbd64, Data2: 0xe71e, Data3: 0x48a0, Data4: [8]byte{0xa4, 0xde, 0x18, 0x5c, 0x39, 0x5c, 0xd3, 0x17}}
	if iidIAudioCaptureClient != wantGUID {
		t.Fatalf("IAudioCaptureClient IID=%+v want=%+v", iidIAudioCaptureClient, wantGUID)
	}
}
