package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const whisperPort = "18183"

type whisperService interface {
	Models() []WhisperModel
	Ensure(context.Context, string) error
	Infer(context.Context, string) (string, map[string]any, error)
	Status() ModelStatus
	Reload() error
	Stop() error
	Close()
}

type WhisperManager struct {
	mu      sync.RWMutex
	opMu    sync.Mutex
	root    string
	store   *Store
	models  map[string]WhisperModel
	status  ModelStatus
	running *runningModel
	client  *http.Client
	baseURL string
	command func(string, ...string) *exec.Cmd
	closed  bool
}

func scanWhisperModels(root string) ([]WhisperModel, error) {
	files, err := filepath.Glob(filepath.Join(root, "gguf", "whisper", "*"))
	if err != nil {
		return nil, err
	}
	models := []WhisperModel{}
	for _, path := range files {
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".bin" && ext != ".gguf" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		name := filepath.Base(path)
		models = append(models, WhisperModel{ID: name, Name: name, Path: path, Size: info.Size()})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func NewWhisperManager(root string, store *Store, models []WhisperModel) *WhisperManager {
	manager := &WhisperManager{root: root, store: store, models: map[string]WhisperModel{}, status: ModelStatus{Kind: "whisper", Status: "unloaded"}, client: &http.Client{Timeout: 2 * time.Second}, baseURL: "http://127.0.0.1:" + whisperPort, command: exec.Command}
	for _, model := range models {
		manager.models[model.ID] = model
	}
	return manager
}

func (w *WhisperManager) Models() []WhisperModel {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]WhisperModel, 0, len(w.models))
	for _, model := range w.models {
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (w *WhisperManager) Status() ModelStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return clone(w.status)
}
func (w *WhisperManager) setStatus(status, model string, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status.Status = status
	w.status.Model = model
	w.status.Error = ""
	if err != nil {
		w.status.Error = err.Error()
	}
	if status != "loaded" {
		w.status.Details = nil
	}
}
func (w *WhisperManager) health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", w.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("whisper-server health HTTP %d", resp.StatusCode)
	}
	return nil
}
func (w *WhisperManager) Ensure(ctx context.Context, modelID string) error {
	w.opMu.Lock()
	defer w.opMu.Unlock()
	return w.ensureLocked(ctx, modelID, false)
}
func (w *WhisperManager) ensureLocked(ctx context.Context, modelID string, recovery bool) error {
	model, ok := w.models[modelID]
	if !ok {
		return errors.New("请选择可用的 Whisper 模型")
	}
	w.mu.RLock()
	loaded := w.status.Status == "loaded" && w.status.Model == modelID
	w.mu.RUnlock()
	if loaded && w.health(ctx) == nil {
		return nil
	}
	if err := w.unloadLocked(); err != nil {
		return err
	}
	if _, err := os.Stat(model.Path); err != nil {
		w.setStatus("failed", modelID, err)
		return fmt.Errorf("未找到 Whisper 模型文件 %s", model.Path)
	}
	exe := filepath.Join(w.root, "whispercpp", "whisper-server.exe")
	if _, err := os.Stat(exe); err != nil {
		w.setStatus("failed", modelID, err)
		return errors.New("未找到 whispercpp/whisper-server.exe，请按目录结构复制运行资源")
	}
	w.setStatus("loading", modelID, nil)
	started := time.Now()
	log.Printf("Whisper 模型加载开始: model=%s recovery=%v", modelID, recovery)
	writer := newDailyLogWriter(filepath.Join(w.root, "log"), "whisper-server", os.Stdout)
	cmd := w.command(exe, "--host", "127.0.0.1", "--port", whisperPort, "--model", model.Path, "--threads", fmt.Sprint(w.store.Settings().Threads))
	cmd.Dir = w.root
	configureProcess(cmd)
	cmd.Stdout = writer
	cmd.Stderr = writer
	run := &runningModel{cmd: cmd, kind: "whisper", id: modelID, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		_ = writer.Close()
		w.setStatus("failed", modelID, err)
		return err
	}
	if err := assignProcess(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = writer.Close()
		w.setStatus("failed", modelID, err)
		return fmt.Errorf("加入进程清理组失败: %w", err)
	}
	w.mu.Lock()
	w.running = run
	w.mu.Unlock()
	go w.watch(run, writer)
	deadline := time.Now().Add(5 * time.Minute)
	for {
		if err := w.health(ctx); err == nil {
			w.mu.Lock()
			w.status.Status = "loaded"
			w.status.Model = modelID
			w.status.Error = ""
			w.status.LoadMS = time.Since(started).Milliseconds()
			if !recovery {
				w.status.RestartCount = 0
			}
			w.mu.Unlock()
			log.Printf("Whisper 模型加载完成: model=%s load_ms=%d", modelID, w.status.LoadMS)
			return nil
		}
		select {
		case <-ctx.Done():
			w.mu.Lock()
			run.expected = true
			w.mu.Unlock()
			_ = cmd.Process.Kill()
			<-run.done
			w.setStatus("failed", modelID, ctx.Err())
			return ctx.Err()
		case <-run.done:
			w.setStatus("failed", modelID, errors.New("whisper-server 已退出，请查看 log 中的服务日志"))
			return errors.New("Whisper 模型加载失败：whisper-server 已退出")
		case <-time.After(300 * time.Millisecond):
			if time.Now().After(deadline) {
				w.mu.Lock()
				run.expected = true
				w.mu.Unlock()
				_ = cmd.Process.Kill()
				<-run.done
				err := errors.New("等待 Whisper 模型就绪超时")
				w.setStatus("failed", modelID, err)
				return err
			}
		}
	}
}
func (w *WhisperManager) watch(run *runningModel, writer *dailyLogWriter) {
	err := run.cmd.Wait()
	if closeErr := writer.Close(); closeErr != nil {
		log.Printf("关闭 whisper-server 日志失败: %v", closeErr)
	}
	close(run.done)
	w.mu.Lock()
	current := w.running == run
	if current {
		w.running = nil
	}
	expected := run.expected || w.closed
	if current && !expected {
		w.status.Status = "failed"
		w.status.Model = run.id
		w.status.Error = fmt.Sprintf("whisper-server 意外退出: %v；正在自动恢复", err)
	}
	w.mu.Unlock()
	if expected || !current {
		return
	}
	log.Printf("whisper-server 意外退出: model=%s error=%v", run.id, err)
	go w.recover(run.id)
}
func (w *WhisperManager) recover(modelID string) {
	for attempt := 1; attempt <= 3; attempt++ {
		w.mu.Lock()
		if w.closed || w.status.Status != "failed" || w.status.Model != modelID {
			w.mu.Unlock()
			return
		}
		w.status.RestartCount = attempt
		w.status.Error = fmt.Sprintf("崩溃恢复，第 %d/3 次", attempt)
		w.mu.Unlock()
		w.opMu.Lock()
		err := w.ensureLocked(context.Background(), modelID, true)
		w.opMu.Unlock()
		if err == nil {
			return
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	w.setStatus("failed", modelID, errors.New("连续三次重启或模型恢复失败，已停止自动重试"))
}
func (w *WhisperManager) unloadLocked() error {
	w.mu.Lock()
	run := w.running
	if run == nil {
		model := w.status.Model
		w.status = ModelStatus{Kind: "whisper", Status: "unloaded", Model: model}
		w.mu.Unlock()
		return nil
	}
	run.expected = true
	w.status.Status = "unloading"
	w.status.Error = ""
	w.mu.Unlock()
	err := run.cmd.Process.Kill()
	select {
	case <-run.done:
	case <-time.After(10 * time.Second):
		err = errors.New("终止 whisper-server 超时")
	}
	w.mu.Lock()
	if w.running == run {
		w.running = nil
	}
	w.status = ModelStatus{Kind: "whisper", Status: "unloaded", Model: run.id}
	w.mu.Unlock()
	return err
}
func (w *WhisperManager) Reload() error {
	w.opMu.Lock()
	defer w.opMu.Unlock()
	w.mu.RLock()
	loaded := w.status.Status == "loaded"
	modelID := w.status.Model
	w.mu.RUnlock()
	if !loaded {
		return nil
	}
	log.Printf("Whisper 设置变更触发模型重载: model=%s", modelID)
	if err := w.unloadLocked(); err != nil {
		return err
	}
	return w.ensureLocked(context.Background(), modelID, false)
}
func (w *WhisperManager) Stop() error {
	w.opMu.Lock()
	defer w.opMu.Unlock()
	return w.unloadLocked()
}
func (w *WhisperManager) Close() {
	w.opMu.Lock()
	defer w.opMu.Unlock()
	w.mu.Lock()
	w.closed = true
	run := w.running
	if run != nil {
		run.expected = true
	}
	w.mu.Unlock()
	if run != nil {
		_ = run.cmd.Process.Kill()
		select {
		case <-run.done:
		case <-time.After(10 * time.Second):
		}
	}
}
func (w *WhisperManager) Infer(ctx context.Context, path string) (text string, stats map[string]any, err error) {
	started := time.Now()
	defer func() {
		if ctx.Err() != nil {
			if stopErr := w.Stop(); stopErr != nil && err == nil {
				err = stopErr
			}
		}
	}()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for key, value := range map[string]string{"response_format": "json", "language": "ja", "temperature": "0"} {
		if err = form.WriteField(key, value); err != nil {
			return "", nil, err
		}
	}
	part, err := form.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return "", nil, err
	}
	if _, err = part.Write(data); err != nil {
		return "", nil, err
	}
	if err = form.Close(); err != nil {
		return "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", w.baseURL+"/inference", &body)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return "", nil, fmt.Errorf("whisper-server HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	var result struct {
		Text string `json:"text"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", nil, err
	}
	if ctx.Err() != nil {
		return "", nil, ctx.Err()
	}
	text = strings.TrimSpace(result.Text)
	if text == "" {
		return "", nil, errors.New("Whisper 返回空识别结果")
	}
	log.Printf("Whisper 识别完成: file=%s elapsed_ms=%d\n%s", path, time.Since(started).Milliseconds(), text)
	return text, map[string]any{"elapsed_ms": time.Since(started).Milliseconds()}, nil
}
