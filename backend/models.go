package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const gib uint64 = 1024 * 1024 * 1024

type runningModel struct {
	cmd      *exec.Cmd
	expected bool
	kind, id string
	done     chan struct{}
}
type hardwareSampler interface {
	Sample() Hardware
}

type ModelManager struct {
	mu       sync.RWMutex
	opMu     sync.Mutex
	root     string
	store    *Store
	hardware hardwareSampler
	models   map[string]ModelInfo
	status   map[string]*ModelStatus
	running  map[string]*runningModel
	client   *http.Client
	closed   bool
}

func NewModelManager(root string, store *Store, hardware hardwareSampler, models []ModelInfo) *ModelManager {
	m := &ModelManager{root: root, store: store, hardware: hardware, models: map[string]ModelInfo{}, status: map[string]*ModelStatus{"chat": {Kind: "chat", Status: "unloaded"}, "vl": {Kind: "vl", Status: "unloaded"}}, running: map[string]*runningModel{}, client: &http.Client{Timeout: 15 * time.Second}}
	for _, v := range models {
		if v.Kind != "vl" {
			m.models[v.ID] = v
		}
	}
	return m
}
func (m *ModelManager) Statuses() []ModelStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return []ModelStatus{clone(*m.status["chat"]), clone(*m.status["vl"])}
}
func (m *ModelManager) setStatus(kind, status, modelID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.status[kind]
	s.Status = status
	s.Model = modelID
	if err != nil {
		s.Error = err.Error()
	} else {
		s.Error = ""
	}
	if status != "loaded" {
		s.Details = nil
	}
}
func (m *ModelManager) loadedID(kind string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.status[kind].Status == "loaded" {
		return m.status[kind].Model
	}
	return ""
}
func (m *ModelManager) port(kind string) string {
	if kind == "vl" {
		return "18182"
	}
	return "18181"
}
func (m *ModelManager) base(kind string) string { return "http://127.0.0.1:" + m.port(kind) }
func (m *ModelManager) modelPath(kind, id string) (string, ModelInfo, error) {
	if kind == "vl" {
		if id != "" && id != "PaddleOCR-VL-1.6-GGUF.gguf" {
			return "", ModelInfo{}, errors.New("图像识别只能使用指定 VL 模型")
		}
		id = "PaddleOCR-VL-1.6-GGUF.gguf"
		return filepath.Join(m.root, "gguf", "vl", id), ModelInfo{ID: id, Name: id, Size: fileSize(filepath.Join(m.root, "gguf", "vl", id)) + fileSize(filepath.Join(m.root, "gguf", "vl", "PaddleOCR-VL-1.6-GGUF-mmproj.gguf"))}, nil
	}
	info, ok := m.models[id]
	if !ok {
		return "", ModelInfo{}, errors.New("所选模型不存在")
	}
	return info.Path, info, nil
}
func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}
func (m *ModelManager) Ensure(ctx context.Context, kind, modelID string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	err := m.ensureLocked(ctx, kind, modelID, false)
	if err != nil {
		log.Printf("模型检查或加载失败: kind=%s model=%s error=%v", kind, modelID, err)
	}
	return err
}
func (m *ModelManager) ensureLocked(ctx context.Context, kind, modelID string, recovery bool) error {
	if kind != "chat" && kind != "vl" {
		return errors.New("未知模型类型")
	}
	path, info, err := m.modelPath(kind, modelID)
	if err != nil {
		return err
	}
	modelID = info.ID
	if m.loadedID(kind) == modelID {
		if err = m.health(ctx, kind); err == nil {
			return nil
		}
	}
	if m.loadedID(kind) != "" {
		if err = m.unloadLocked(kind); err != nil {
			return err
		}
	}
	available := m.hardware.Sample().MemoryAvailable
	reserve := 4 * gib
	if kind == "vl" {
		reserve = 2 * gib
	}
	if available < reserve {
		other := "vl"
		if kind == "vl" {
			other = "chat"
		}
		if m.loadedID(other) != "" {
			if err = m.unloadLocked(other); err != nil {
				return fmt.Errorf("释放另一模型内存失败: %w", err)
			}
		}
	}
	exe := filepath.Join(m.root, "llamacpp", "llama-server.exe")
	if _, err = os.Stat(exe); err != nil {
		return errors.New("未找到 llamacpp/llama-server.exe，请按目录结构复制运行资源")
	}
	if _, err = os.Stat(path); err != nil {
		return fmt.Errorf("未找到模型文件 %s", path)
	}
	settings := m.store.Settings()
	ctxSize := settings.Context
	if kind == "vl" {
		ctxSize = 4096
	}
	args := []string{"--host", "127.0.0.1", "--port", m.port(kind), "--model", path, "--ctx-size", fmt.Sprint(ctxSize), "--threads", fmt.Sprint(settings.Threads), "--parallel", "1"}
	if kind == "vl" {
		mmproj := filepath.Join(m.root, "gguf", "vl", "PaddleOCR-VL-1.6-GGUF-mmproj.gguf")
		if _, err = os.Stat(mmproj); err != nil {
			return errors.New("未找到 VL 配对 mmproj 文件")
		}
		args = append(args, "--mmproj", mmproj)
	}
	m.setStatus(kind, "loading", modelID, nil)
	log.Printf("模型加载开始: kind=%s model=%s recovery=%v", kind, modelID, recovery)
	started := time.Now()
	logWriter := newDailyLogWriter(filepath.Join(m.root, "log"), kind+"-server", os.Stdout)
	cmd := exec.Command(exe, args...)
	cmd.Dir = m.root
	configureProcess(cmd)
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	run := &runningModel{cmd: cmd, kind: kind, id: modelID, done: make(chan struct{})}
	if err = cmd.Start(); err != nil {
		_ = logWriter.Close()
		m.setStatus(kind, "failed", modelID, err)
		return err
	}
	if err = assignProcess(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = logWriter.Close()
		m.setStatus(kind, "failed", modelID, err)
		return fmt.Errorf("加入进程清理组失败: %w", err)
	}
	m.mu.Lock()
	m.running[kind] = run
	m.mu.Unlock()
	go m.watch(run, logWriter)
	if err = m.waitHealthy(ctx, kind, cmd); err != nil {
		m.mu.Lock()
		run.expected = true
		m.mu.Unlock()
		_ = cmd.Process.Kill()
		<-run.done
		m.setStatus(kind, "failed", modelID, err)
		return fmt.Errorf("模型加载失败: %w", err)
	}
	props, _ := m.fetchProps(ctx, kind)
	if meta, metaErr := m.fetchModelMeta(ctx, kind); metaErr == nil {
		if props == nil {
			props = map[string]any{}
		}
		props["model_meta"] = meta
		if kind == "chat" {
			m.mu.Lock()
			info := m.models[modelID]
			for key, value := range meta {
				info.Meta[key] = value
			}
			m.models[modelID] = info
			m.mu.Unlock()
		}
	}
	m.mu.Lock()
	s := m.status[kind]
	s.Status = "loaded"
	s.Error = ""
	loadMS := time.Since(started).Milliseconds()
	s.LoadMS = loadMS
	s.Details = props
	if !recovery {
		s.RestartCount = 0
	}
	m.mu.Unlock()
	log.Printf("模型加载完成: kind=%s model=%s load_ms=%d", kind, modelID, loadMS)
	return nil
}
func (m *ModelManager) health(ctx context.Context, kind string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", m.base(kind)+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("health HTTP %d", resp.StatusCode)
	}
	return nil
}
func (m *ModelManager) waitHealthy(ctx context.Context, kind string, cmd *exec.Cmd) error {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		if err := m.health(ctx, kind); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return errors.New("llama-server 已退出，请查看 log 中的服务日志")
		}
		if time.Now().After(deadline) {
			return errors.New("等待模型就绪超时")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
func (m *ModelManager) fetchProps(ctx context.Context, kind string) (map[string]any, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", m.base(kind)+"/props", nil)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("props HTTP %d", resp.StatusCode)
	}
	var out map[string]any
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}
func (m *ModelManager) fetchModelMeta(ctx context.Context, kind string) (map[string]any, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", m.base(kind)+"/v1/models", nil)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("models HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data []struct {
			Meta map[string]any `json:"meta"`
		} `json:"data"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if len(payload.Data) == 0 {
		return nil, errors.New("模型元数据为空")
	}
	return payload.Data[0].Meta, nil
}
func (m *ModelManager) Details(kind string) (ModelStatus, error) {
	if kind != "chat" && kind != "vl" {
		return ModelStatus{}, errors.New("未知模型类型")
	}
	m.mu.RLock()
	out := clone(*m.status[kind])
	m.mu.RUnlock()
	if out.Status == "loaded" && out.Details == nil {
		props, err := m.fetchProps(context.Background(), kind)
		if err == nil {
			m.mu.Lock()
			m.status[kind].Details = props
			out.Details = props
			m.mu.Unlock()
		}
	}
	return out, nil
}
func (m *ModelManager) Unload(kind string) error {
	if kind != "chat" && kind != "vl" {
		return errors.New("未知模型类型")
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.unloadLocked(kind)
}
func (m *ModelManager) unloadLocked(kind string) error {
	m.mu.Lock()
	run := m.running[kind]
	s := m.status[kind]
	if run == nil {
		old := s.Model
		m.status[kind] = &ModelStatus{Kind: kind, Status: "unloaded", Model: old}
		m.mu.Unlock()
		return nil
	}
	run.expected = true
	s.Status = "unloading"
	s.Error = ""
	m.mu.Unlock()
	log.Printf("模型卸载开始: kind=%s model=%s", kind, run.id)
	err := run.cmd.Process.Kill()
	select {
	case <-run.done:
	case <-time.After(10 * time.Second):
		err = errors.New("终止 llama-server 超时")
	}
	m.mu.Lock()
	delete(m.running, kind)
	m.status[kind] = &ModelStatus{Kind: kind, Status: "unloaded", Model: run.id}
	m.mu.Unlock()
	if err != nil {
		log.Printf("模型卸载失败: kind=%s model=%s error=%v", kind, run.id, err)
	} else {
		log.Printf("模型卸载完成: kind=%s model=%s", kind, run.id)
	}
	return err
}
func (m *ModelManager) watch(run *runningModel, logWriter *dailyLogWriter) {
	err := run.cmd.Wait()
	if closeErr := logWriter.Close(); closeErr != nil {
		log.Printf("关闭 llama-server 日志失败: kind=%s error=%v", run.kind, closeErr)
	}
	close(run.done)
	m.mu.Lock()
	current := m.running[run.kind]
	if current == run {
		delete(m.running, run.kind)
	}
	expected := run.expected || m.closed
	if current == run && !expected {
		m.status[run.kind] = &ModelStatus{Kind: run.kind, Status: "failed", Model: run.id, Error: fmt.Sprintf("llama-server 意外退出: %v；正在自动恢复", err)}
	}
	m.mu.Unlock()
	if expected {
		return
	}
	log.Printf("llama-server 意外退出: kind=%s model=%s error=%v", run.kind, run.id, err)
	m.mu.RLock()
	closed := m.closed
	m.mu.RUnlock()
	if !closed {
		go m.recover(run.kind, run.id)
	}
}
func (m *ModelManager) recover(kind, modelID string) {
	for attempt := 1; attempt <= 3; attempt++ {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return
		}
		if m.status[kind].Status == "loaded" {
			m.mu.Unlock()
			return
		}
		m.status[kind].RestartCount = attempt
		m.status[kind].Error = fmt.Sprintf("崩溃恢复，第 %d/3 次", attempt)
		m.mu.Unlock()
		log.Printf("模型崩溃恢复开始: kind=%s model=%s attempt=%d/3", kind, modelID, attempt)
		m.opMu.Lock()
		err := m.ensureLocked(context.Background(), kind, modelID, true)
		m.opMu.Unlock()
		if err == nil {
			log.Printf("模型崩溃恢复成功: kind=%s model=%s attempt=%d/3", kind, modelID, attempt)
			return
		}
		log.Printf("模型崩溃恢复失败: kind=%s model=%s attempt=%d/3 error=%v", kind, modelID, attempt, err)
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	log.Printf("模型崩溃恢复终止: kind=%s model=%s 已连续失败三次", kind, modelID)
	m.setStatus(kind, "failed", modelID, errors.New("连续三次重启或模型恢复失败，已停止自动重试"))
}
func (m *ModelManager) Reload(kinds ...string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	loaded := map[string]string{}
	for _, kind := range kinds {
		loaded[kind] = m.loadedID(kind)
	}
	for _, kind := range kinds {
		if loaded[kind] != "" {
			if err := m.unloadLocked(kind); err != nil {
				return err
			}
			if err := m.ensureLocked(context.Background(), kind, loaded[kind], false); err != nil {
				return err
			}
		}
	}
	return nil
}
func (m *ModelManager) Close() {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	m.closed = true
	runs := []*runningModel{}
	for _, r := range m.running {
		r.expected = true
		runs = append(runs, r)
	}
	m.mu.Unlock()
	for _, r := range runs {
		_ = r.cmd.Process.Kill()
	}
	for _, r := range runs {
		<-r.done
	}
}
func (m *ModelManager) completionRequest(ctx context.Context, kind string, payload any) (*http.Response, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", m.base(kind)+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		resp.Body.Close()
		return nil, fmt.Errorf("llama-server HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp, nil
}
