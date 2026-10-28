package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDailyLogWriterWritesConsoleAndAppendsFile(t *testing.T) {
	dir := t.TempDir()
	var console bytes.Buffer
	fixed := time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local)
	writer := newDailyLogWriter(dir, "easywork", &console)
	writer.now = func() time.Time { return fixed }
	if _, err := writer.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	writer = newDailyLogWriter(dir, "easywork", &console)
	writer.now = func() time.Time { return fixed }
	if _, err := writer.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "easywork-2026-10-28.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first\nsecond\n" {
		t.Fatalf("文件内容错误: %q", data)
	}
	if console.String() != "first\nsecond\n" {
		t.Fatalf("控制台内容错误: %q", console.String())
	}
}

func TestDailyLogWriterRotatesAtLocalDateChange(t *testing.T) {
	dir := t.TempDir()
	current := time.Date(2026, 10, 28, 23, 59, 59, 0, time.Local)
	writer := newDailyLogWriter(dir, "chat-server", &bytes.Buffer{})
	writer.now = func() time.Time { return current }
	_, _ = writer.Write([]byte("before\n"))
	current = current.Add(24 * time.Hour)
	_, _ = writer.Write([]byte("after\n"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{"chat-server-2026-10-28.log": "before\n", "chat-server-2026-10-29.log": "after\n"}
	for name, want := range checks {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatalf("%s 内容错误: %q", name, data)
		}
	}
}

func TestDailyLogWriterConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	var console bytes.Buffer
	writer := newDailyLogWriter(dir, "easywork", &console)
	writer.now = func() time.Time { return time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local) }
	var wg sync.WaitGroup
	for worker := 0; worker < 20; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for line := 0; line < 50; line++ {
				_, _ = fmt.Fprintf(writer, "message-%d\n", line)
			}
		}()
	}
	wg.Wait()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "easywork-2026-10-28.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 1000 {
		t.Fatalf("文件日志行数 = %d, want 1000", len(lines))
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "message-") {
			t.Fatalf("出现交叉写入: %q", line)
		}
	}
	if strings.Count(console.String(), "\n") != 1000 {
		t.Fatal("控制台日志不完整")
	}
}

func TestDailyLogWriterFallsBackToConsole(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}
	var console bytes.Buffer
	writer := newDailyLogWriter(filepath.Join(blocked, "log"), "easywork", &console)
	n, err := writer.Write([]byte("service started\n"))
	if err != nil || n != len("service started\n") {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	if !strings.Contains(console.String(), "service started") || !strings.Contains(console.String(), "已降级为控制台输出") {
		t.Fatalf("控制台降级输出不完整: %q", console.String())
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFormatMessagesForLogOmitsOnlyImageData(t *testing.T) {
	messages := []chatMessage{{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "OCR: 完整文本"},
		map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64,secret-image-data"}},
	}}}
	formatted := formatMessagesForLog(messages)
	if !strings.Contains(formatted, "OCR: 完整文本") || !strings.Contains(formatted, "图片数据已省略") {
		t.Fatalf("文本或图片占位缺失: %s", formatted)
	}
	if strings.Contains(formatted, "secret-image-data") {
		t.Fatal("图片 base64 数据被写入日志")
	}
}
