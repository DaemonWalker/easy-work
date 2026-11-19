package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

const (
	audioSampleRate   = 16000
	audioFrameSamples = 320
)

type capturedAudio struct {
	StartMS int64
	EndMS   int64
	Samples []int16
}

type audioCapture interface {
	Run(func([]int16) error) error
	Close() error
}

type captureFactory func(source string) (audioCapture, error)

type waveFormat struct {
	FormatTag     uint16
	Channels      uint16
	SamplesPerSec uint32
	BlockAlign    uint16
	BitsPerSample uint16
	SubFormat     uint32
}

func parseWaveFormat(data []byte) (waveFormat, error) {
	if len(data) < 18 {
		return waveFormat{}, errors.New("WASAPI WAVEFORMATEX 数据不完整")
	}
	format := waveFormat{
		FormatTag:     binary.LittleEndian.Uint16(data[0:2]),
		Channels:      binary.LittleEndian.Uint16(data[2:4]),
		SamplesPerSec: binary.LittleEndian.Uint32(data[4:8]),
		BlockAlign:    binary.LittleEndian.Uint16(data[12:14]),
		BitsPerSample: binary.LittleEndian.Uint16(data[14:16]),
		SubFormat:     uint32(binary.LittleEndian.Uint16(data[0:2])),
	}
	if format.FormatTag != 0xfffe {
		return format, nil
	}
	if binary.LittleEndian.Uint16(data[16:18]) < 22 || len(data) < 40 {
		return waveFormat{}, errors.New("WASAPI WAVEFORMATEXTENSIBLE 数据不完整")
	}
	format.SubFormat = binary.LittleEndian.Uint32(data[24:28])
	return format, nil
}

func decodeAudioSample(data []byte, format waveFormat) (int16, error) {
	tag := format.FormatTag
	if tag == 0xfffe {
		if format.SubFormat != 1 && format.SubFormat != 3 {
			return 0, fmt.Errorf("不支持的 WASAPI 采样格式: tag=%d sub_format=%d bits=%d", format.FormatTag, format.SubFormat, format.BitsPerSample)
		}
		tag = uint16(format.SubFormat)
	}
	switch tag {
	case 1:
		switch format.BitsPerSample {
		case 8:
			return int16((int(data[0]) - 128) << 8), nil
		case 16:
			return int16(binary.LittleEndian.Uint16(data)), nil
		case 24:
			value := int32(uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16)
			if value&0x800000 != 0 {
				value |= ^0xffffff
			}
			return int16(value >> 8), nil
		case 32:
			return int16(int32(binary.LittleEndian.Uint32(data)) >> 16), nil
		}
	case 3:
		var value float64
		switch format.BitsPerSample {
		case 32:
			value = float64(math.Float32frombits(binary.LittleEndian.Uint32(data)))
		case 64:
			value = math.Float64frombits(binary.LittleEndian.Uint64(data))
		default:
			return 0, fmt.Errorf("不支持的 WASAPI 浮点采样格式: tag=%d sub_format=%d bits=%d", format.FormatTag, format.SubFormat, format.BitsPerSample)
		}
		value = math.Max(-1, math.Min(1, value))
		return int16(math.Round(value * 32767)), nil
	}
	return 0, fmt.Errorf("不支持的 WASAPI 采样格式: tag=%d sub_format=%d bits=%d", format.FormatTag, format.SubFormat, format.BitsPerSample)
}

func pcmTo16kMono(data []byte, frames int, format waveFormat) ([]int16, error) {
	if format.Channels == 0 || format.SamplesPerSec == 0 || format.BlockAlign == 0 {
		return nil, errors.New("无效音频格式")
	}
	width := int(format.BitsPerSample / 8)
	if width == 0 || int(format.BlockAlign) < width*int(format.Channels) {
		return nil, errors.New("无效音频块对齐")
	}
	if frames < 0 || len(data) < frames*int(format.BlockAlign) {
		return nil, errors.New("音频数据长度不足")
	}
	mono := make([]int32, frames)
	for frame := 0; frame < frames; frame++ {
		var mixed int32
		for channel := 0; channel < int(format.Channels); channel++ {
			offset := frame*int(format.BlockAlign) + channel*width
			sample, err := decodeAudioSample(data[offset:offset+width], format)
			if err != nil {
				return nil, err
			}
			mixed += int32(sample)
		}
		mono[frame] = mixed / int32(format.Channels)
	}
	if format.SamplesPerSec == audioSampleRate {
		out := make([]int16, len(mono))
		for i, sample := range mono {
			out[i] = int16(sample)
		}
		return out, nil
	}
	out := make([]int16, int(math.Round(float64(len(mono))*audioSampleRate/float64(format.SamplesPerSec))))
	for i := range out {
		position := float64(i) * float64(format.SamplesPerSec) / audioSampleRate
		left := int(position)
		right := left + 1
		if right >= len(mono) {
			right = len(mono) - 1
		}
		fraction := position - float64(left)
		out[i] = int16(math.Round(float64(mono[left])*(1-fraction) + float64(mono[right])*fraction))
	}
	return out, nil
}

func mixAudio(left, right []int16) []int16 {
	length := len(left)
	if len(right) > length {
		length = len(right)
	}
	out := make([]int16, length)
	for i := range out {
		var a, b int32
		if i < len(left) {
			a = int32(left[i])
		}
		if i < len(right) {
			b = int32(right[i])
		}
		value := (a + b) / 2
		out[i] = int16(value)
	}
	return out
}

type audioSegmenter struct {
	frame       []int16
	preRoll     []int16
	active      []int16
	position    int
	silence     int
	activeStart int
}

func (s *audioSegmenter) Push(samples []int16, emit func(capturedAudio) error) error {
	s.frame = append(s.frame, samples...)
	for len(s.frame) >= audioFrameSamples {
		frame := append([]int16(nil), s.frame[:audioFrameSamples]...)
		s.frame = append(s.frame[:0], s.frame[audioFrameSamples:]...)
		s.position += audioFrameSamples
		var energy int64
		for _, sample := range frame {
			value := int64(sample)
			energy += value * value
		}
		voiced := energy/audioFrameSamples > 500*500
		if s.active == nil {
			s.preRoll = append(s.preRoll, frame...)
			if len(s.preRoll) > audioSampleRate*300/1000 {
				s.preRoll = append(s.preRoll[:0], s.preRoll[len(s.preRoll)-audioSampleRate*300/1000:]...)
			}
			if voiced {
				s.active = append([]int16(nil), s.preRoll...)
				s.activeStart = s.position - len(s.preRoll)
				s.preRoll = nil
				s.silence = 0
			}
			continue
		}
		s.active = append(s.active, frame...)
		if voiced {
			s.silence = 0
		} else {
			s.silence += audioFrameSamples
		}
		if s.silence >= audioSampleRate*700/1000 || len(s.active) >= audioSampleRate*25 {
			if err := s.flush(emit); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *audioSegmenter) Flush(emit func(capturedAudio) error) error {
	if len(s.active) >= audioSampleRate/10 {
		return s.flush(emit)
	}
	s.active = nil
	s.preRoll = nil
	return nil
}

func (s *audioSegmenter) flush(emit func(capturedAudio) error) error {
	segment := capturedAudio{StartMS: int64(s.activeStart) * 1000 / audioSampleRate, EndMS: int64(s.position) * 1000 / audioSampleRate, Samples: append([]int16(nil), s.active...)}
	s.active = nil
	s.preRoll = nil
	s.silence = 0
	return emit(segment)
}

func writeWAV(path string, samples []int16) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	dataSize := len(samples) * 2
	header := make([]byte, 44)
	copy(header, "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+dataSize))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], audioSampleRate)
	binary.LittleEndian.PutUint32(header[28:], audioSampleRate*2)
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(dataSize))
	file, err := os.Create(path + ".tmp")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path + ".tmp")
		}
	}()
	if _, err = file.Write(header); err != nil {
		return err
	}
	data := make([]byte, dataSize)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(sample))
	}
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(path+".tmp", path); err != nil {
		return err
	}
	ok = true
	return nil
}

func readWAV(path string) ([]int16, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" ||
		binary.LittleEndian.Uint16(data[20:22]) != 1 || binary.LittleEndian.Uint16(data[22:24]) != 1 ||
		binary.LittleEndian.Uint32(data[24:28]) != audioSampleRate || binary.LittleEndian.Uint16(data[34:36]) != 16 {
		return nil, errors.New("临时 WAV 格式无效")
	}
	payload := data[44:]
	if len(payload)%2 != 0 {
		return nil, errors.New("临时 WAV 数据损坏")
	}
	samples := make([]int16, len(payload)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(payload[i*2:]))
	}
	return samples, nil
}
