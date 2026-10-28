package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type dailyLogWriter struct {
	mu      sync.Mutex
	dir     string
	prefix  string
	console io.Writer
	now     func() time.Time
	day     string
	file    *os.File
}

func newDailyLogWriter(dir, prefix string, console io.Writer) *dailyLogWriter {
	return &dailyLogWriter{dir: dir, prefix: prefix, console: console, now: time.Now}
}

func (w *dailyLogWriter) warn(format string, args ...any) {
	if w.console != nil {
		_, _ = fmt.Fprintf(w.console, "日志警告："+format+"\n", args...)
	}
}

func (w *dailyLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.console != nil {
		_, _ = w.console.Write(p)
	}
	day := w.now().Format("2006-01-02")
	if w.file != nil && w.day != day {
		if err := w.file.Close(); err != nil {
			w.warn("关闭旧日志文件失败: %v", err)
		}
		w.file = nil
	}
	if w.file == nil {
		if err := os.MkdirAll(w.dir, 0755); err != nil {
			w.warn("创建日志目录失败，已降级为控制台输出: %v", err)
			return len(p), nil
		}
		path := filepath.Join(w.dir, w.prefix+"-"+day+".log")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			w.warn("打开日志文件失败，已降级为控制台输出: %v", err)
			return len(p), nil
		}
		w.file = file
		w.day = day
	}
	if _, err := w.file.Write(p); err != nil {
		w.warn("写入日志文件失败，已降级为控制台输出: %v", err)
		_ = w.file.Close()
		w.file = nil
	}
	return len(p), nil
}

func (w *dailyLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
