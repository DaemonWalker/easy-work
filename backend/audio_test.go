package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAudioCapture struct {
	chunks [][]int16
	wait   bool
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
}

func newFakeAudioCapture(chunks ...[]int16) *fakeAudioCapture {
	return &fakeAudioCapture{chunks: chunks, stop: make(chan struct{}), done: make(chan struct{})}
}
func (c *fakeAudioCapture) Run(emit func([]int16) error) error {
	defer close(c.done)
	for _, chunk := range c.chunks {
		select {
		case <-c.stop:
			return nil
		default:
		}
		if err := emit(chunk); err != nil {
			return err
		}
	}
	if c.wait {
		<-c.stop
	}
	return nil
}
func (c *fakeAudioCapture) Close() error {
	c.once.Do(func() { close(c.stop) })
	return nil
}

type fakeWhisper struct {
	mu       sync.Mutex
	models   []WhisperModel
	infer    func(context.Context, int, string) (string, error)
	calls    int
	ensure   int
	stopped  int
	closed   int
	reloads  int
	reloadCh chan struct{}
}

func (w *fakeWhisper) Models() []WhisperModel { return w.models }
func (w *fakeWhisper) Ensure(context.Context, string) error {
	w.mu.Lock()
	w.ensure++
	w.mu.Unlock()
	return nil
}
func (w *fakeWhisper) Infer(ctx context.Context, path string) (string, map[string]any, error) {
	w.mu.Lock()
	call := w.calls
	w.calls++
	infer := w.infer
	w.mu.Unlock()
	if infer == nil {
		return fmt.Sprintf("原文%d", call+1), map[string]any{"elapsed_ms": 1}, nil
	}
	text, err := infer(ctx, call, path)
	return text, map[string]any{"elapsed_ms": 1}, err
}
func (w *fakeWhisper) Status() ModelStatus {
	return ModelStatus{Kind: "whisper", Status: "loaded", Model: "ggml-small.bin"}
}
func (w *fakeWhisper) Reload() error {
	w.mu.Lock()
	w.reloads++
	channel := w.reloadCh
	w.mu.Unlock()
	if channel != nil {
		select {
		case channel <- struct{}{}:
		default:
		}
	}
	return nil
}
func (w *fakeWhisper) Stop() error {
	w.mu.Lock()
	w.stopped++
	w.mu.Unlock()
	return nil
}
func (w *fakeWhisper) Close() { w.mu.Lock(); w.closed++; w.mu.Unlock() }

func audioTestWhisper() *fakeWhisper {
	return &fakeWhisper{models: []WhisperModel{{ID: "ggml-small.bin", Name: "ggml-small.bin", Path: "small.bin"}}}
}
func speechChunk(value int16) []int16 {
	samples := make([]int16, audioSampleRate)
	for i := 0; i < audioSampleRate/5; i++ {
		samples[i] = value
	}
	return samples
}
func installAudioManager(app *App, capture audioCapture, whisper whisperService) *AudioManager {
	manager := NewAudioManager(app.store, app.models, app.tasks, whisper, func(string) (audioCapture, error) { return capture, nil })
	app.audio = manager
	return manager
}
func startAudio(t *testing.T, manager *AudioManager) (AudioSessionState, *AudioSession) {
	t.Helper()
	state, err := manager.Start("both", "ggml-small.bin", "Hy-MT2-7B-Q4_K_M.gguf")
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.RLock()
	session := manager.sessions[state.Meeting.ID]
	manager.mu.RUnlock()
	return state, session
}
func waitAudioRun(t *testing.T, session *AudioSession) {
	t.Helper()
	session.mu.RLock()
	done := session.runDone
	session.mu.RUnlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("音频会话未在期限内结束")
	}
}

func TestAudioContinuousRecognitionPrecedesBacklogTranslation(t *testing.T) {
	chat := newMockLlama()
	startMock(t, "18181", chat, "chat")
	app := newTestApp(t)
	loaded(app, "chat", "Hy-MT2-7B-Q4_K_M.gguf")
	capture := newFakeAudioCapture(speechChunk(4000), speechChunk(5000), speechChunk(6000))
	whisper := audioTestWhisper()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	whisper.infer = func(ctx context.Context, call int, path string) (string, error) {
		if chat.chatCalls.Load() != 0 {
			return "", errors.New("积压原文尚未识别就开始翻译")
		}
		once.Do(func() { close(started) })
		select {
		case <-release:
			return fmt.Sprintf("日文%d", call+1), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	manager := installAudioManager(app, capture, whisper)
	defer manager.Close()
	state, session := startAudio(t, manager)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("首段识别未开始")
	}
	select {
	case <-capture.done:
	case <-time.After(3 * time.Second):
		t.Fatal("连续采集未结束")
	}
	if _, _, err := app.tasks.Start(context.Background(), "chat"); !errors.Is(err, errBusy) {
		t.Fatalf("连续会议未保持全局互斥: %v", err)
	}
	close(release)
	waitAudioRun(t, session)
	saved, ok := app.store.AudioMeeting(state.Meeting.ID)
	if !ok || len(saved.Segments) != 3 {
		t.Fatalf("连续语音段丢失: %+v", saved)
	}
	for i, segment := range saved.Segments {
		want := fmt.Sprintf("日文%d", i+1)
		if segment.Source != want || segment.Translation != "完成回复" || segment.Status != audioComplete {
			t.Fatalf("语音段 %d 未完成: %+v", i, segment)
		}
		if _, err := os.Stat(filepath.Join(app.store.dir, "audio", saved.ID, segment.ID+".wav")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("完成后临时 WAV 未清理: %v", err)
		}
	}
	if chat.chatCalls.Load() != 3 {
		t.Fatalf("译文没有逐段补全: %d", chat.chatCalls.Load())
	}
	reopened, err := NewStore(app.store.dir)
	if err != nil {
		t.Fatal(err)
	}
	persisted, _ := reopened.AudioMeeting(saved.ID)
	if len(persisted.Segments) != 3 || persisted.Segments[2].Translation == "" {
		t.Fatal("完整会议未持久化")
	}
	if _, _, err = app.tasks.Start(context.Background(), "chat"); err != nil {
		t.Fatalf("会议完成后全局任务未释放: %v", err)
	} else {
		app.tasks.Finish(app.tasks.State().TaskID)
	}
}

func TestAudioTranslationFailureKeepsOriginalAndResume(t *testing.T) {
	chat := newMockLlama()
	chat.fail.Store(true)
	startMock(t, "18181", chat, "chat")
	app := newTestApp(t)
	loaded(app, "chat", "Hy-MT2-7B-Q4_K_M.gguf")
	capture := newFakeAudioCapture(speechChunk(4000))
	manager := installAudioManager(app, capture, audioTestWhisper())
	defer manager.Close()
	state, session := startAudio(t, manager)
	waitAudioRun(t, session)
	current, _ := manager.Session(state.Meeting.ID)
	if current.Status != "awaiting_completion" || current.Processing || current.Recording {
		t.Fatalf("失败后的会议状态错误: %+v", current)
	}
	saved, _ := app.store.AudioMeeting(state.Meeting.ID)
	if len(saved.Segments) != 1 || saved.Segments[0].Source == "" || saved.Segments[0].Translation != "" || saved.Segments[0].TranslationStats != nil {
		t.Fatalf("不完整译文被保存或完整原文丢失: %+v", saved.Segments)
	}
	raw, err := os.ReadFile(filepath.Join(app.store.dir, "audio.json"))
	if err != nil || strings.Contains(string(raw), "不完整") {
		t.Fatalf("不完整流式内容被持久化: %s", raw)
	}
	chat.fail.Store(false)
	if _, err = manager.Resume(saved.ID); err != nil {
		t.Fatal(err)
	}
	waitAudioRun(t, session)
	saved, _ = app.store.AudioMeeting(saved.ID)
	if saved.Segments[0].Translation != "完成回复" || saved.Segments[0].Status != audioComplete {
		t.Fatalf("会后补全失败: %+v", saved.Segments[0])
	}
}

func TestAudioStopDoesNotPersistPartialAndCanResume(t *testing.T) {
	chat := newMockLlama()
	startMock(t, "18181", chat, "chat")
	app := newTestApp(t)
	loaded(app, "chat", "Hy-MT2-7B-Q4_K_M.gguf")
	capture := newFakeAudioCapture(speechChunk(4000))
	whisper := audioTestWhisper()
	started := make(chan struct{})
	var once sync.Once
	blocked := true
	whisper.infer = func(ctx context.Context, call int, path string) (string, error) {
		whisper.mu.Lock()
		wait := blocked
		whisper.mu.Unlock()
		if wait {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "停止后补全原文", nil
	}
	manager := installAudioManager(app, capture, whisper)
	defer manager.Close()
	state, session := startAudio(t, manager)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("识别未开始")
	}
	if !app.tasks.Stop() {
		t.Fatal("全局停止未接受")
	}
	waitAudioRun(t, session)
	saved, _ := app.store.AudioMeeting(state.Meeting.ID)
	if len(saved.Segments) != 0 {
		t.Fatalf("停止后的不完整识别被保存: %+v", saved.Segments)
	}
	whisper.mu.Lock()
	blocked = false
	whisper.mu.Unlock()
	if _, err := manager.Resume(saved.ID); err != nil {
		t.Fatal(err)
	}
	waitAudioRun(t, session)
	saved, _ = app.store.AudioMeeting(saved.ID)
	if len(saved.Segments) != 1 || saved.Segments[0].Source != "停止后补全原文" || saved.Segments[0].Translation != "完成回复" {
		t.Fatalf("停止后的临时 WAV 未用于补全: %+v", saved.Segments)
	}
}

func TestAudioAPIAndSSE(t *testing.T) {
	app := newTestApp(t)
	loaded(app, "chat", "Hy-MT2-7B-Q4_K_M.gguf")
	capture := newFakeAudioCapture(speechChunk(4000))
	manager := installAudioManager(app, capture, audioTestWhisper())
	defer manager.Close()
	server := httptest.NewServer(app.routes())
	defer server.Close()
	response, err := http.Post(server.URL+"/api/audio/sessions", "application/json", strings.NewReader(`{"source":"system","whisper_model":"ggml-small.bin","chat_model":"Hy-MT2-7B-Q4_K_M.gguf"}`))
	if err != nil {
		t.Fatal(err)
	}
	var state AudioSessionState
	if err = json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 201 {
		t.Fatalf("创建会议状态=%d", response.StatusCode)
	}
	request, err := http.NewRequest("POST", server.URL+"/api/audio/sessions/"+state.Meeting.ID+"/recording/stop", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("停止录音 API 失败: status=%d error=%v", response.StatusCode, err)
	}
	response.Body.Close()
	manager.mu.RLock()
	session := manager.sessions[state.Meeting.ID]
	manager.mu.RUnlock()
	waitAudioRun(t, session)
	ctx, cancel := context.WithCancel(context.Background())
	request, err = http.NewRequestWithContext(ctx, "GET", server.URL+"/api/audio/sessions/"+state.Meeting.ID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	event, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(event, "event: snapshot") {
		t.Fatalf("SSE 快照事件错误: %q %v", event, err)
	}
	cancel()
	response.Body.Close()
	response, err = http.Get(server.URL + "/api/audio/models")
	if err != nil {
		t.Fatal(err)
	}
	var models map[string]json.RawMessage
	if err = json.NewDecoder(response.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !bytes.Contains(models["whisper"], []byte("ggml-small.bin")) || !bytes.Contains(models["chat"], []byte("Hy-MT2")) {
		t.Fatalf("音频模型 API 不完整: %+v", models)
	}
}

func TestAudioCloseEndsSSEAndRejectsResume(t *testing.T) {
	app := newTestApp(t)
	capture := newFakeAudioCapture()
	capture.wait = true
	manager := installAudioManager(app, capture, audioTestWhisper())
	defer manager.Close()
	state, _ := startAudio(t, manager)
	server := httptest.NewServer(app.routes())
	defer server.Close()
	response, err := http.Get(server.URL + "/api/audio/sessions/" + state.Meeting.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for i := 0; i < 3; i++ {
		if _, err = reader.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
	}
	readDone := make(chan error, 1)
	go func() {
		for {
			if _, err := reader.ReadString('\n'); err != nil {
				readDone <- err
				return
			}
		}
	}()
	closeDone := make(chan struct{})
	go func() {
		manager.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("关闭音频管理器时发生阻塞")
	}
	select {
	case err = <-readDone:
		if err == nil {
			t.Fatal("SSE 在管理器关闭后仍未结束")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("关闭音频管理器未结束 SSE")
	}
	if _, err = manager.Resume(state.Meeting.ID); err == nil {
		t.Fatal("关闭后仍允许启动补全")
	}
}

func TestAudioIndex2BModelSelection(t *testing.T) {
	app := newTestApp(t)
	capture := newFakeAudioCapture()
	capture.wait = true
	manager := installAudioManager(app, capture, audioTestWhisper())
	defer manager.Close()
	state, err := manager.Start("system", "ggml-small.bin", "Index-Translate-2B.Q4_K_M.gguf")
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.RLock()
	session := manager.sessions[state.Meeting.ID]
	manager.mu.RUnlock()
	if state.Meeting.ChatModel != "Index-Translate-2B.Q4_K_M.gguf" {
		t.Fatalf("音频 2B 模型未保留: %s", state.Meeting.ChatModel)
	}
	payload := generationPayload(state.Meeting.ChatModel, generationFor(state.Meeting.ChatModel, defaultGeneration()))
	if payload["temperature"] != 0.0 || payload["chat_template_kwargs"].(map[string]any)["enable_thinking"] != false {
		t.Fatalf("2B 未使用 Index 生成参数: %+v", payload)
	}
	if allowsModel(TabJa2zh, state.Meeting.ChatModel) {
		t.Fatal("音频 2B 兼容不应扩大普通日译中页面白名单")
	}
	session.Stop()
	waitAudioRun(t, session)
}

func TestAudioCurrentUsesConsistentSessionSnapshots(t *testing.T) {
	app := newTestApp(t)
	manager := NewAudioManager(app.store, app.models, app.tasks, audioTestWhisper(), nil)
	session := &AudioSession{meeting: AudioMeeting{ID: "current", Segments: []AudioSegment{}, CreatedAt: now(), UpdatedAt: now()}, status: "processing", processing: true}
	manager.sessions[session.meeting.ID] = session
	const iterations = 200
	done := make(chan struct{}, 2)
	go func() {
		for i := 0; i < iterations; i++ {
			session.mu.Lock()
			session.processing = i%2 == 0
			session.meeting.UpdatedAt = time.Now()
			session.mu.Unlock()
		}
		done <- struct{}{}
	}()
	go func() {
		for i := 0; i < iterations; i++ {
			state := manager.Current()
			if state == nil || state.Meeting.ID != session.meeting.ID {
				t.Error("Current 返回了错误会话快照")
				break
			}
		}
		done <- struct{}{}
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("Current 与状态写入发生锁死")
		}
	}
	session.mu.Lock()
	session.processing = true
	session.mu.Unlock()
	if state := manager.Current(); state == nil || !state.Processing {
		t.Fatal("Current 未返回一致的最新处理状态")
	}
}

func TestSettingsThreadsReloadsWhisperAndNilAudioIsSafe(t *testing.T) {
	saveThreads := func(app *App, threads int) *httptest.ResponseRecorder {
		t.Helper()
		settings := app.store.Settings()
		settings.Threads = threads
		data, _ := json.Marshal(settings)
		req := httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(data))
		rec := httptest.NewRecorder()
		app.handleSettings(rec, req)
		return rec
	}
	withoutAudio := newTestApp(t)
	if rec := saveThreads(withoutAudio, withoutAudio.store.Settings().Threads+1); rec.Code != 200 {
		t.Fatalf("nil audio 时修改线程失败: %d %s", rec.Code, rec.Body.String())
	}
	app := newTestApp(t)
	whisper := audioTestWhisper()
	whisper.reloadCh = make(chan struct{}, 1)
	manager := installAudioManager(app, newFakeAudioCapture(), whisper)
	defer manager.Close()
	if rec := saveThreads(app, app.store.Settings().Threads+1); rec.Code != 200 {
		t.Fatalf("修改线程失败: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case <-whisper.reloadCh:
	case <-time.After(3 * time.Second):
		t.Fatal("设置变更未触发 Whisper Reload")
	}
	whisper.mu.Lock()
	reloads := whisper.reloads
	whisper.mu.Unlock()
	if reloads != 1 {
		t.Fatalf("Whisper Reload 次数错误: %d", reloads)
	}
	taskID, _, err := app.tasks.Start(context.Background(), "audio")
	if err != nil {
		t.Fatal(err)
	}
	if rec := saveThreads(app, app.store.Settings().Threads+1); rec.Code != 409 {
		t.Fatalf("全局任务期间允许修改线程: %d", rec.Code)
	}
	app.tasks.Finish(taskID)
	whisper.mu.Lock()
	defer whisper.mu.Unlock()
	if whisper.reloads != 1 {
		t.Fatalf("忙碌期间意外重载 Whisper: %d", whisper.reloads)
	}
}
