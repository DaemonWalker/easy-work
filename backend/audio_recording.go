package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

const audioRecordingName = "recording.wav"

type audioRecorder struct {
	path           string
	file           *os.File
	writer         *bufio.Writer
	samples        int64
	pendingSamples int64
	closed         bool
}

func audioRecordingPath(root, meetingID string) string {
	return filepath.Join(root, "audio", meetingID, audioRecordingName)
}

func newAudioRecorder(path string) (*audioRecorder, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	if _, err = file.Write(wavHeader(0)); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &audioRecorder{path: path, file: file, writer: bufio.NewWriterSize(file, 64*1024)}, nil
}

func (r *audioRecorder) Write(samples []int16) error {
	if r.closed {
		return errors.New("完整录音已关闭")
	}
	if len(samples) == 0 {
		return nil
	}
	if r.samples+int64(len(samples)) > int64(math.MaxUint32-36)/2 {
		return errors.New("完整录音已达到 WAV 4 GB 上限")
	}
	data := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(sample))
	}
	if _, err := r.writer.Write(data); err != nil {
		return fmt.Errorf("写入完整录音失败: %w", err)
	}
	r.samples += int64(len(samples))
	r.pendingSamples += int64(len(samples))
	if r.pendingSamples >= audioSampleRate {
		if err := r.writer.Flush(); err != nil {
			return fmt.Errorf("写入完整录音失败: %w", err)
		}
		r.pendingSamples = 0
	}
	return nil
}

func (r *audioRecorder) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	closeWithError := func(err error) error {
		_ = r.file.Close()
		return err
	}
	if err := r.writer.Flush(); err != nil {
		return closeWithError(fmt.Errorf("写入完整录音失败: %w", err))
	}
	dataSize := r.samples * 2
	if _, err := r.file.Seek(0, io.SeekStart); err != nil {
		return closeWithError(err)
	}
	if _, err := r.file.Write(wavHeader(int(dataSize))); err != nil {
		return closeWithError(err)
	}
	if err := r.file.Sync(); err != nil {
		return closeWithError(err)
	}
	if err := r.file.Close(); err != nil {
		return err
	}
	if err := os.Rename(r.path+".tmp", r.path); err != nil {
		return err
	}
	return nil
}

func audioRecordingInfo(path string) (AudioRecording, error) {
	info, err := os.Stat(path)
	if err != nil {
		return AudioRecording{}, err
	}
	dataSize := info.Size() - 44
	if dataSize < 0 || dataSize%2 != 0 {
		return AudioRecording{}, errors.New("完整录音数据损坏")
	}
	return AudioRecording{Status: "ready", Size: info.Size(), DurationMS: dataSize / 2 * 1000 / audioSampleRate}, nil
}

func recoverAudioRecording(path string) (AudioRecording, error) {
	if _, err := os.Stat(path); err == nil {
		return audioRecordingInfo(path)
	}
	tmp := path + ".tmp"
	info, err := os.Stat(tmp)
	if err != nil {
		return AudioRecording{}, err
	}
	dataSize := info.Size() - 44
	if dataSize < 0 || dataSize%2 != 0 || dataSize > math.MaxUint32-36 {
		return AudioRecording{}, errors.New("未完成录音无法恢复")
	}
	file, err := os.OpenFile(tmp, os.O_RDWR, 0644)
	if err != nil {
		return AudioRecording{}, err
	}
	if _, err = file.WriteAt(wavHeader(int(dataSize)), 0); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return AudioRecording{}, err
	}
	if err = os.Rename(tmp, path); err != nil {
		return AudioRecording{}, err
	}
	return audioRecordingInfo(path)
}

func forEachRecordedSegment(ctx context.Context, path string, emit func(capturedAudio) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	header := make([]byte, 44)
	if _, err = io.ReadFull(file, header); err != nil {
		return errors.New("完整录音 WAV 头无效")
	}
	if string(header[:4]) != "RIFF" || string(header[8:12]) != "WAVE" ||
		binary.LittleEndian.Uint16(header[20:22]) != 1 || binary.LittleEndian.Uint16(header[22:24]) != 1 ||
		binary.LittleEndian.Uint32(header[24:28]) != audioSampleRate || binary.LittleEndian.Uint16(header[34:36]) != 16 {
		return errors.New("完整录音 WAV 格式无效")
	}
	segmenter := &audioSegmenter{}
	buffer := make([]byte, 64*1024)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			if n%2 != 0 {
				return errors.New("完整录音 PCM 数据损坏")
			}
			samples := make([]int16, n/2)
			for i := range samples {
				samples[i] = int16(binary.LittleEndian.Uint16(buffer[i*2:]))
			}
			if err = segmenter.Push(samples, emit); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return segmenter.Flush(emit)
}
