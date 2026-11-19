package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWhisperInferenceMultipart(t *testing.T) {
	var requestError error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference" {
			requestError = errors.New("请求路径错误")
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(2 * 1024 * 1024); err != nil {
			requestError = err
			http.Error(w, err.Error(), 400)
			return
		}
		if r.FormValue("language") != "ja" || r.FormValue("response_format") != "json" {
			requestError = errors.New("识别参数错误")
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			requestError = err
			http.Error(w, err.Error(), 400)
			return
		}
		defer file.Close()
		data := make([]byte, 44)
		if _, err = file.Read(data); err != nil || string(data[:4]) != "RIFF" || header.Filename != "segment.wav" {
			requestError = errors.New("WAV 上传内容错误")
		}
		writeJSON(w, 200, map[string]string{"text": "  テスト English 123  "})
	}))
	defer server.Close()
	manager := NewWhisperManager(t.TempDir(), nil, nil)
	manager.baseURL = server.URL
	path := filepath.Join(t.TempDir(), "segment.wav")
	if err := writeWAV(path, make([]int16, 1600)); err != nil {
		t.Fatal(err)
	}
	text, stats, err := manager.Infer(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if requestError != nil {
		t.Fatal(requestError)
	}
	if text != "テスト English 123" || stats["elapsed_ms"] == nil {
		t.Fatalf("识别响应错误: text=%q stats=%v", text, stats)
	}
}

func TestWhisperHelperProcess(t *testing.T) {
	if os.Getenv("EASYWORK_WHISPER_HELPER") != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestWhisperCancellationKillsExpectedProcess(t *testing.T) {
	if os.Getenv("EASYWORK_WHISPER_HELPER") == "1" {
		t.Skip("辅助进程")
	}
	root := t.TempDir()
	manager := NewWhisperManager(root, nil, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-release
	}))
	defer server.Close()
	defer close(release)
	manager.baseURL = server.URL
	cmd := exec.Command(os.Args[0], "-test.run=^TestWhisperHelperProcess$")
	cmd.Env = append(os.Environ(), "EASYWORK_WHISPER_HELPER=1")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := assignProcess(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	run := &runningModel{cmd: cmd, kind: "whisper", id: "small.bin", done: make(chan struct{})}
	manager.running = run
	manager.status = ModelStatus{Kind: "whisper", Status: "loaded", Model: "small.bin"}
	writer := newDailyLogWriter(filepath.Join(root, "log"), "whisper-test", os.Stdout)
	go manager.watch(run, writer)
	path := filepath.Join(root, "segment.wav")
	if err := writeWAV(path, make([]int16, 1600)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := manager.Infer(ctx, path)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("mock Whisper 请求未开始")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消错误=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消未结束识别")
	}
	select {
	case <-run.done:
	case <-time.After(3 * time.Second):
		t.Fatal("取消未实际终止 Whisper 进程")
	}
	t.Logf("辅助进程输出:\n%s", output.String())
	status := manager.Status()
	if status.Status != "unloaded" {
		t.Fatalf("受控停止被当成崩溃或状态未清理: %+v", status)
	}
	data, _ := json.Marshal(status)
	if string(data) == "" {
		t.Fatal("状态序列化失败")
	}
}

func TestWhisperReloadUsesCurrentThreadsAndDoesNotStartUnloaded(t *testing.T) {
	if os.Getenv("EASYWORK_WHISPER_HELPER") == "1" {
		t.Skip("辅助进程")
	}
	root := t.TempDir()
	store, err := NewStore(filepath.Join(root, "history"))
	if err != nil {
		t.Fatal(err)
	}
	modelPath := filepath.Join(root, "gguf", "whisper", "small.bin")
	if err = os.MkdirAll(filepath.Dir(modelPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(modelPath, []byte("mock"), 0644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "whispercpp", "whisper-server.exe")
	if err = os.MkdirAll(filepath.Dir(exe), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(exe, []byte("mock"), 0644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	manager := NewWhisperManager(root, store, []WhisperModel{{ID: "small.bin", Name: "small.bin", Path: modelPath}})
	defer manager.Close()
	manager.baseURL = server.URL
	var commandMu sync.Mutex
	calls := 0
	threads := []string{}
	manager.command = func(name string, args ...string) *exec.Cmd {
		commandMu.Lock()
		calls++
		for i := range args {
			if args[i] == "--threads" && i+1 < len(args) {
				threads = append(threads, args[i+1])
			}
		}
		commandMu.Unlock()
		cmd := exec.Command(os.Args[0], "-test.run=^TestWhisperHelperProcess$")
		cmd.Env = append(os.Environ(), "EASYWORK_WHISPER_HELPER=1")
		return cmd
	}
	if err = manager.Ensure(context.Background(), "small.bin"); err != nil {
		t.Fatal(err)
	}
	settings := store.Settings()
	settings.Threads = 9
	if _, err = store.UpdateSettings(func(current *Settings) error { *current = settings; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = manager.Reload(); err != nil {
		t.Fatal(err)
	}
	commandMu.Lock()
	if calls != 2 || len(threads) != 2 || threads[1] != "9" {
		commandMu.Unlock()
		t.Fatalf("Whisper 重载未使用当前线程: calls=%d threads=%v", calls, threads)
	}
	commandMu.Unlock()
	if status := manager.Status(); status.Status != "loaded" || status.Model != "small.bin" {
		t.Fatalf("重载后的状态错误: %+v", status)
	}
	if err = manager.Stop(); err != nil {
		t.Fatal(err)
	}
	if err = manager.Reload(); err != nil {
		t.Fatal(err)
	}
	commandMu.Lock()
	defer commandMu.Unlock()
	if calls != 2 {
		t.Fatalf("未加载时 Reload 意外启动 Whisper: calls=%d", calls)
	}
}
