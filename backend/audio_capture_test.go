package main

import (
	"encoding/binary"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestPCMTo16kMono(t *testing.T) {
	format := waveFormat{FormatTag: 1, Channels: 2, SamplesPerSec: 48000, BlockAlign: 4, BitsPerSample: 16}
	data := make([]byte, 12)
	values := []int16{30000, 10000, -30000, -10000, 12345, 12345}
	for i, value := range values {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(value))
	}
	samples, err := pcmTo16kMono(data, 3, format)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0] != 20000 {
		t.Fatalf("混音或重采样错误: %v", samples)
	}
	floatData := make([]byte, 8)
	binary.LittleEndian.PutUint32(floatData, math.Float32bits(1))
	binary.LittleEndian.PutUint32(floatData[4:], math.Float32bits(-1))
	floats, err := pcmTo16kMono(floatData, 1, waveFormat{FormatTag: 3, Channels: 2, SamplesPerSec: 16000, BlockAlign: 8, BitsPerSample: 32})
	if err != nil || len(floats) != 1 || floats[0] != 0 {
		t.Fatalf("浮点格式转换错误: samples=%v error=%v", floats, err)
	}
}

func TestWAVRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio", "segment.wav")
	want := []int16{-32768, -123, 0, 456, 32767}
	if err := writeWAV(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readWAV(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("样本数错误: %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("样本 %d 错误: got=%d want=%d", i, got[i], want[i])
		}
	}
}

func TestContinuousSpeechSegmentation(t *testing.T) {
	segmenter := &audioSegmenter{}
	var segments []capturedAudio
	emit := func(segment capturedAudio) error {
		segments = append(segments, segment)
		return nil
	}
	push := func(samples int, value int16) {
		data := make([]int16, samples)
		for i := range data {
			data[i] = value
		}
		if err := segmenter.Push(data, emit); err != nil {
			t.Fatal(err)
		}
	}
	push(audioSampleRate/2, 4000)
	push(audioSampleRate*4/5, 0)
	push(audioSampleRate*2/5, 5000)
	if len(segments) != 1 {
		t.Fatalf("首个语音端点未切分: %d", len(segments))
	}
	if err := segmenter.Flush(emit); err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 {
		t.Fatalf("停止录音未保留末尾语音: %d", len(segments))
	}
	if segments[0].StartMS != 0 || segments[0].EndMS < 1200 || segments[1].StartMS < segments[0].EndMS {
		t.Fatalf("语音段时间戳不连续: %+v", segments)
	}
	for i := 0; i < audioSampleRate/2; i++ {
		if segments[0].Samples[i] != 4000 {
			t.Fatalf("首段语音内容被裁掉: index=%d value=%d length=%d", i, segments[0].Samples[i], len(segments[0].Samples))
		}
	}
	if segments[1].Samples[len(segments[1].Samples)-1] != 5000 {
		t.Fatal("末尾语音内容被裁掉")
	}
}

func TestMixAudioDoesNotOverflow(t *testing.T) {
	mixed := mixAudio([]int16{32767, -32768}, []int16{32767, -32768})
	if mixed[0] != 32767 || mixed[1] != -32768 {
		t.Fatalf("混合音频溢出: %v", mixed)
	}
}

func extensibleFormatBytes(subFormat uint32, bits uint16) []byte {
	data := make([]byte, 40)
	binary.LittleEndian.PutUint16(data[0:2], 0xfffe)
	binary.LittleEndian.PutUint16(data[2:4], 2)
	binary.LittleEndian.PutUint32(data[4:8], audioSampleRate)
	blockAlign := uint16(2 * bits / 8)
	binary.LittleEndian.PutUint32(data[8:12], uint32(audioSampleRate*int(blockAlign)))
	binary.LittleEndian.PutUint16(data[12:14], blockAlign)
	binary.LittleEndian.PutUint16(data[14:16], bits)
	binary.LittleEndian.PutUint16(data[16:18], 22)
	binary.LittleEndian.PutUint16(data[18:20], bits)
	binary.LittleEndian.PutUint32(data[20:24], 3)
	binary.LittleEndian.PutUint32(data[24:28], subFormat)
	binary.LittleEndian.PutUint16(data[30:32], 0x0010)
	copy(data[32:40], []byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71})
	return data
}

func TestExtensibleWaveFormatPCMandFloat(t *testing.T) {
	pcmFormat, err := parseWaveFormat(extensibleFormatBytes(1, 16))
	if err != nil {
		t.Fatal(err)
	}
	if pcmFormat.FormatTag != 0xfffe || pcmFormat.SubFormat != 1 || pcmFormat.Channels != 2 || pcmFormat.BlockAlign != 4 {
		t.Fatalf("PCM extensible 解析错误: %+v", pcmFormat)
	}
	pcmData := make([]byte, 8)
	for i, value := range []int16{1000, -1000, 2000, 4000} {
		binary.LittleEndian.PutUint16(pcmData[i*2:], uint16(value))
	}
	pcm, err := pcmTo16kMono(pcmData, 2, pcmFormat)
	if err != nil || len(pcm) != 2 || pcm[0] != 0 || pcm[1] != 3000 {
		t.Fatalf("PCM extensible 转换错误: samples=%v error=%v", pcm, err)
	}
	floatFormat, err := parseWaveFormat(extensibleFormatBytes(3, 32))
	if err != nil {
		t.Fatal(err)
	}
	if floatFormat.SubFormat != 3 || floatFormat.BlockAlign != 8 {
		t.Fatalf("IEEE_FLOAT extensible 解析错误: %+v", floatFormat)
	}
	floatData := make([]byte, 8)
	binary.LittleEndian.PutUint32(floatData, math.Float32bits(1))
	binary.LittleEndian.PutUint32(floatData[4:], math.Float32bits(-1))
	floats, err := pcmTo16kMono(floatData, 1, floatFormat)
	if err != nil || len(floats) != 1 || floats[0] != 0 {
		t.Fatalf("IEEE_FLOAT extensible 转换错误: samples=%v error=%v", floats, err)
	}
}

func TestExtensibleWaveFormatErrorIncludesSubFormat(t *testing.T) {
	format, err := parseWaveFormat(extensibleFormatBytes(7, 32))
	if err != nil {
		t.Fatal(err)
	}
	_, err = pcmTo16kMono(make([]byte, 8), 1, format)
	if err == nil || !strings.Contains(err.Error(), "tag=65534") || !strings.Contains(err.Error(), "sub_format=7") || !strings.Contains(err.Error(), "bits=32") {
		t.Fatalf("错误未包含实际 SubFormat: %v", err)
	}
	plain := make([]byte, 18)
	binary.LittleEndian.PutUint16(plain[0:2], 1)
	binary.LittleEndian.PutUint16(plain[2:4], 1)
	binary.LittleEndian.PutUint32(plain[4:8], audioSampleRate)
	binary.LittleEndian.PutUint16(plain[12:14], 2)
	binary.LittleEndian.PutUint16(plain[14:16], 16)
	parsed, err := parseWaveFormat(plain)
	if err != nil || parsed.FormatTag != 1 || parsed.SubFormat != 1 {
		t.Fatalf("普通 PCM 路径回归: format=%+v error=%v", parsed, err)
	}
}
