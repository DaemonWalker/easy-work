//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	clsidMMDeviceEnumerator = syscall.GUID{Data1: 0xbcde0395, Data2: 0xe52f, Data3: 0x467c, Data4: [8]byte{0x8e, 0x3d, 0xc4, 0x57, 0x92, 0x91, 0x69, 0x2e}}
	iidIMMDeviceEnumerator  = syscall.GUID{Data1: 0xa95664d2, Data2: 0x9614, Data3: 0x4f35, Data4: [8]byte{0xa7, 0x46, 0xde, 0x8d, 0xb6, 0x36, 0x17, 0xe6}}
	iidIAudioClient         = syscall.GUID{Data1: 0x1cb9ad4c, Data2: 0xdbfa, Data3: 0x4c32, Data4: [8]byte{0xb1, 0x78, 0xc2, 0xf5, 0x68, 0xa7, 0x03, 0xb2}}
	iidIAudioCaptureClient  = syscall.GUID{Data1: 0xc8adbd64, Data2: 0xe71e, Data3: 0x48a0, Data4: [8]byte{0xa4, 0xde, 0x18, 0x5c, 0x39, 0x5c, 0xd3, 0x17}}
)

type iUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}
type iUnknown struct{ Vtbl *iUnknownVtbl }

type immDeviceEnumeratorVtbl struct {
	iUnknownVtbl
	EnumAudioEndpoints                     uintptr
	GetDefaultAudioEndpoint                uintptr
	GetDevice                              uintptr
	RegisterEndpointNotificationCallback   uintptr
	UnregisterEndpointNotificationCallback uintptr
}
type immDeviceEnumerator struct{ Vtbl *immDeviceEnumeratorVtbl }

type immDeviceVtbl struct {
	iUnknownVtbl
	Activate          uintptr
	OpenPropertyStore uintptr
	GetId             uintptr
	GetState          uintptr
}
type immDevice struct{ Vtbl *immDeviceVtbl }

type iAudioClientVtbl struct {
	iUnknownVtbl
	Initialize        uintptr
	GetBufferSize     uintptr
	GetStreamLatency  uintptr
	GetCurrentPadding uintptr
	IsFormatSupported uintptr
	GetMixFormat      uintptr
	GetDevicePeriod   uintptr
	Start             uintptr
	Stop              uintptr
	Reset             uintptr
	SetEventHandle    uintptr
	GetService        uintptr
}
type iAudioClient struct{ Vtbl *iAudioClientVtbl }

type iAudioCaptureClientVtbl struct {
	iUnknownVtbl
	GetBuffer         uintptr
	ReleaseBuffer     uintptr
	GetNextPacketSize uintptr
}
type iAudioCaptureClient struct{ Vtbl *iAudioCaptureClientVtbl }

type wasapiCapture struct {
	source string
	done   chan struct{}
	once   sync.Once
}

func newWASAPICapture(source string) (audioCapture, error) {
	if source != "system" && source != "microphone" && source != "both" {
		return nil, errors.New("音源必须是 system、microphone 或 both")
	}
	return &wasapiCapture{source: source, done: make(chan struct{})}, nil
}

func (c *wasapiCapture) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}

func comResult(result uintptr) error {
	if int32(result) >= 0 {
		return nil
	}
	return fmt.Errorf("WASAPI HRESULT 0x%08x", uint32(result))
}

func releaseCOM(object *iUnknown) {
	if object != nil && object.Vtbl != nil {
		_, _, _ = syscall.SyscallN(object.Vtbl.Release, uintptr(unsafe.Pointer(object)))
	}
}

func runWasapiSource(source string, done <-chan struct{}, emit func([]int16) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ole32 := syscall.NewLazyDLL("ole32.dll")
	coInitialize := ole32.NewProc("CoInitializeEx")
	result, _, _ := coInitialize.Call(0, 0)
	if err := comResult(result); err != nil {
		return err
	}
	defer ole32.NewProc("CoUninitialize").Call()
	var enumerator *immDeviceEnumerator
	result, _, _ = ole32.NewProc("CoCreateInstance").Call(
		uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, 23,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enumerator)))
	if err := comResult(result); err != nil {
		return err
	}
	defer releaseCOM((*iUnknown)(unsafe.Pointer(enumerator)))
	dataFlow := uintptr(1)
	if source == "system" {
		dataFlow = 0
	}
	var device *immDevice
	result, _, _ = syscall.SyscallN(enumerator.Vtbl.GetDefaultAudioEndpoint, uintptr(unsafe.Pointer(enumerator)), dataFlow, 0, uintptr(unsafe.Pointer(&device)))
	if err := comResult(result); err != nil {
		return err
	}
	defer releaseCOM((*iUnknown)(unsafe.Pointer(device)))
	var client *iAudioClient
	result, _, _ = syscall.SyscallN(device.Vtbl.Activate, uintptr(unsafe.Pointer(device)), uintptr(unsafe.Pointer(&iidIAudioClient)), 23, 0, uintptr(unsafe.Pointer(&client)))
	if err := comResult(result); err != nil {
		return err
	}
	defer releaseCOM((*iUnknown)(unsafe.Pointer(client)))
	var mixFormat *byte
	result, _, _ = syscall.SyscallN(client.Vtbl.GetMixFormat, uintptr(unsafe.Pointer(client)), uintptr(unsafe.Pointer(&mixFormat)))
	if err := comResult(result); err != nil {
		return err
	}
	if mixFormat == nil {
		return errors.New("WASAPI GetMixFormat 未返回音频格式")
	}
	defer ole32.NewProc("CoTaskMemFree").Call(uintptr(unsafe.Pointer(mixFormat)))
	header := unsafe.Slice(mixFormat, 18)
	formatSize := 18 + int(binary.LittleEndian.Uint16(header[16:18]))
	format, err := parseWaveFormat(unsafe.Slice(mixFormat, formatSize))
	if err != nil {
		return err
	}
	flags := uint64(0)
	if source == "system" {
		flags = 0x00020000
	}
	result, _, _ = syscall.SyscallN(client.Vtbl.Initialize, uintptr(unsafe.Pointer(client)), 0, uintptr(flags), 0, 0, uintptr(unsafe.Pointer(mixFormat)), 0)
	if err := comResult(result); err != nil {
		return err
	}
	var capture *iAudioCaptureClient
	result, _, _ = syscall.SyscallN(client.Vtbl.GetService, uintptr(unsafe.Pointer(client)), uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&capture)))
	if err := comResult(result); err != nil {
		return err
	}
	if capture == nil {
		return errors.New("WASAPI GetService 未返回采集接口")
	}
	defer releaseCOM((*iUnknown)(unsafe.Pointer(capture)))
	result, _, _ = syscall.SyscallN(client.Vtbl.Start, uintptr(unsafe.Pointer(client)))
	if err := comResult(result); err != nil {
		return err
	}
	defer syscall.SyscallN(client.Vtbl.Stop, uintptr(unsafe.Pointer(client)))
	for {
		select {
		case <-done:
			return nil
		default:
		}
		var frames uint32
		result, _, _ = syscall.SyscallN(capture.Vtbl.GetNextPacketSize, uintptr(unsafe.Pointer(capture)), uintptr(unsafe.Pointer(&frames)))
		if err := comResult(result); err != nil {
			return err
		}
		if frames == 0 {
			select {
			case <-done:
				return nil
			case <-time.After(5 * time.Millisecond):
			}
			continue
		}
		var data *byte
		var bufferFlags uint32
		var devicePosition, qpcPosition uint64
		result, _, _ = syscall.SyscallN(capture.Vtbl.GetBuffer, uintptr(unsafe.Pointer(capture)), uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&bufferFlags)), uintptr(unsafe.Pointer(&devicePosition)), uintptr(unsafe.Pointer(&qpcPosition)))
		if err := comResult(result); err != nil {
			return err
		}
		samples := make([]int16, (int(frames)*audioSampleRate+int(format.SamplesPerSec)/2)/int(format.SamplesPerSec))
		var err error
		if data != nil && bufferFlags&0x2 == 0 {
			raw := unsafe.Slice(data, int(frames)*int(format.BlockAlign))
			samples, err = pcmTo16kMono(raw, int(frames), format)
		}
		releaseResult, _, _ := syscall.SyscallN(capture.Vtbl.ReleaseBuffer, uintptr(unsafe.Pointer(capture)), uintptr(frames))
		if err != nil {
			return err
		}
		if err = comResult(releaseResult); err != nil {
			return err
		}
		if len(samples) > 0 {
			if err = emit(samples); err != nil {
				return err
			}
		}
	}
}

func (c *wasapiCapture) Run(emit func([]int16) error) error {
	if c.source != "both" {
		return runWasapiSource(c.source, c.done, emit)
	}
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }
	defer stop()
	go func() {
		select {
		case <-c.done:
			stop()
		case <-done:
		}
	}()
	type captured struct {
		stream  int
		samples []int16
		err     error
	}
	chunks := make(chan captured, 32)
	for stream, source := range []string{"system", "microphone"} {
		go func(stream int, source string) {
			err := runWasapiSource(source, done, func(samples []int16) error {
				select {
				case chunks <- captured{stream: stream, samples: samples}:
					return nil
				case <-done:
					return nil
				}
			})
			if err != nil {
				select {
				case chunks <- captured{stream: stream, err: err}:
				case <-done:
				}
			}
		}(stream, source)
	}
	pending := [][]int16{{}, {}}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return nil
		case item := <-chunks:
			if item.err != nil {
				return item.err
			}
			pending[item.stream] = append(pending[item.stream], item.samples...)
		case <-ticker.C:
			for stream := range pending {
				if len(pending[stream]) < audioFrameSamples {
					padded := make([]int16, audioFrameSamples)
					copy(padded, pending[stream])
					pending[stream] = padded
				}
			}
			left := append([]int16(nil), pending[0][:audioFrameSamples]...)
			right := append([]int16(nil), pending[1][:audioFrameSamples]...)
			pending[0] = append(pending[0][:0], pending[0][audioFrameSamples:]...)
			pending[1] = append(pending[1][:0], pending[1][audioFrameSamples:]...)
			if err := emit(mixAudio(left, right)); err != nil {
				return err
			}
		}
	}
}
