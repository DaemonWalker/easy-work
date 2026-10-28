package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testModels() []ModelInfo {
	return []ModelInfo{{ID: "Hy-MT2-7B-Q4_K_M.gguf", Kind: "chat", Name: "Hy-MT2-7B-Q4_K_M.gguf", Path: "q4.gguf", MaxContext: 8192}, {ID: "HY-MT2-7B-Q6_K.gguf", Kind: "chat", Name: "HY-MT2-7B-Q6_K.gguf", Path: "q6.gguf", MaxContext: 8192}, {ID: "PaddleOCR-VL-1.6-GGUF.gguf", Kind: "vl", Name: "PaddleOCR-VL-1.6-GGUF.gguf", MaxContext: 131072}}
}
func newTestApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "html"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "html", "index.html"), []byte("<h1>offline</h1>"), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(root, "history"))
	if err != nil {
		t.Fatal(err)
	}
	hardware := newHardwareMonitor()
	models := NewModelManager(root, store, hardware, testModels())
	return &App{root: root, store: store, models: models, tasks: NewTaskManager(), hardware: hardware, modelList: testModels()}
}

type mockLlama struct {
	mu       sync.Mutex
	messages []chatMessage
	tokens   int
	block    atomic.Bool
	started  chan struct{}
	once     sync.Once
	ocrCalls atomic.Int32
	fail     atomic.Bool
}

func newMockLlama() *mockLlama { return &mockLlama{tokens: 10, started: make(chan struct{})} }
func (m *mockLlama) handler(kind string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(200)
		case "/props":
			writeJSON(w, 200, map[string]any{"total_slots": 1, "build_info": "b11898", "default_generation_settings": map[string]any{"n_ctx": 4096}})
		case "/apply-template":
			var body struct {
				Messages []chatMessage `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			m.mu.Lock()
			m.messages = body.Messages
			m.mu.Unlock()
			writeJSON(w, 200, map[string]string{"prompt": "rendered prompt"})
		case "/tokenize":
			tokens := make([]int, m.tokens)
			writeJSON(w, 200, map[string]any{"tokens": tokens})
		case "/v1/chat/completions":
			if kind == "vl" {
				m.ocrCalls.Add(1)
			}
			if m.block.Load() {
				m.once.Do(func() { close(m.started) })
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			if m.fail.Load() {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"不完整\"}}]}\n\n")
				flusher.Flush()
				return
			}
			text := "完成回复"
			if kind == "vl" {
				text = "日文\nEnglish 123"
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", text)
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":3},\"timings\":{\"prompt_n\":8,\"cache_n\":2,\"predicted_n\":3,\"prompt_ms\":12.5,\"predicted_ms\":30,\"prompt_per_second\":640,\"predicted_per_second\":100}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
		default:
			http.NotFound(w, r)
		}
	})
}
func startMock(t *testing.T, port string, m *mockLlama, kind string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: m.handler(kind)}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
}
func loaded(app *App, kind, id string) {
	app.models.status[kind] = &ModelStatus{Kind: kind, Status: "loaded", Model: id, Details: map[string]any{}}
}
func createConversation(t *testing.T, app *App, tab string) Conversation {
	t.Helper()
	c, err := app.store.NewConversation(tab, "Hy-MT2-7B-Q4_K_M.gguf")
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func chatBody(text string) string {
	data, _ := json.Marshal(map[string]any{"model": "Hy-MT2-7B-Q4_K_M.gguf", "text": text, "romaji_mode": "spaced"})
	return string(data)
}
func performChat(app *App, c Conversation, body, endpoint string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/chat/"+c.ID+"/"+endpoint, strings.NewReader(body))
	req.SetPathValue("id", c.ID)
	rec := httptest.NewRecorder()
	if endpoint == "send" {
		app.handleChatSend(rec, req)
	} else {
		app.handleRegenerate(rec, req)
	}
	return rec
}

func TestPromptBaseline(t *testing.T) {
	for _, tab := range []string{TabColleague, TabDocument, TabTestcase} {
		result := userPrompt(tab, "原文")
		if !strings.HasPrefix(result, "请将以下文本翻译为日语。\n注意翻译的风格要严格符合【"+prompts[tab]+"】\n\n原文") {
			t.Fatalf("Style 结构错误: %q", result)
		}
	}
	checks := []string{"礼貌铺垫", "动词基本形", "～ことを確認する", "期待值必须以「～こと」结尾", "输出前仅自检一次",
		"レビュー会議", "同一句的体和句尾必须一致",
		"全文使用常体，任何句尾都不允许出现です・ます・ません・ください"}
	all := prompts[TabColleague] + prompts[TabDocument] + prompts[TabTestcase]
	for _, check := range checks {
		if !strings.Contains(all, check) {
			t.Errorf("Prompt 缺少 %q", check)
		}
	}
	if got := userPrompt(TabCustom, "完整 Prompt"); got != "完整 Prompt" {
		t.Fatal("自由 Chat 被附加固定内容")
	}
	if got := userPrompt(TabJa2zh, "日文"); got != "【待翻译文本】\n日文\n\n【翻译任务】\n"+prompts[TabJa2zh] {
		t.Fatal("日译中未保留原 Personalization 结构")
	}
}
func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings := store.Settings()
	settings.Theme = "midnight"
	settings.ImageTranslate = true
	settings.TabModels[TabDocument] = "q4"
	if _, err = store.UpdateSettings(func(s *Settings) error { *s = settings; return nil }); err != nil {
		t.Fatal(err)
	}
	c, err := store.NewConversation(TabDocument, "q4")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateConversation(c.ID, func(v *Conversation) error {
		v.Messages = append(v.Messages, Message{ID: "u", Role: "user", Content: "原文", CreatedAt: now()})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Settings().Theme != "midnight" || !reopened.Settings().ImageTranslate {
		t.Fatal("设置未持久化")
	}
	saved, ok := reopened.Conversation(c.ID)
	if !ok || len(saved.Messages) != 1 || saved.Messages[0].Content != "原文" {
		t.Fatal("对话未持久化")
	}
}
func TestTaskMutualExclusionAndCancellation(t *testing.T) {
	tasks := NewTaskManager()
	id1, ctx, err := tasks.Start(context.Background(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = tasks.Start(context.Background(), "ocr"); err == nil {
		t.Fatal("任务被排队或并行")
	}
	if !tasks.Stop() {
		t.Fatal("停止失败")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("停止未取消推理上下文")
	}
	tasks.Finish(id1)
	if _, _, err = tasks.Start(context.Background(), "ocr"); err != nil {
		t.Fatalf("结束后无法启动: %v", err)
	}
}
func TestSettingsAndModelSelectionValidation(t *testing.T) {
	app := newTestApp(t)
	settings := app.store.Settings()
	settings.Context = 9000
	data, _ := json.Marshal(settings)
	req := httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(data))
	rec := httptest.NewRecorder()
	app.handleSettings(rec, req)
	if rec.Code != 400 {
		t.Fatalf("超模型上下文未被拒绝: %d", rec.Code)
	}
	req = httptest.NewRequest("PUT", "/api/preferences", strings.NewReader(`{"tab":"colleague","model":"other.gguf"}`))
	rec = httptest.NewRecorder()
	app.handlePreferences(rec, req)
	if rec.Code != 400 {
		t.Fatal("不存在模型被接受")
	}
	app.models.models["other.gguf"] = ModelInfo{ID: "other.gguf", Kind: "chat"}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("PUT", "/api/preferences", strings.NewReader(`{"tab":"colleague","model":"other.gguf"}`))
	app.handlePreferences(rec, req)
	if rec.Code != 400 {
		t.Fatal("固定场景接受非翻译模型")
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("PUT", "/api/preferences", strings.NewReader(`{"tab":"custom","model":"other.gguf"}`))
	app.handlePreferences(rec, req)
	if rec.Code != 200 {
		t.Fatalf("自由 Chat 应接受非 VL 模型: %s", rec.Body.String())
	}
}
func TestChatCompletionHistoryAndStats(t *testing.T) {
	mock := newMockLlama()
	startMock(t, "18181", mock, "chat")
	app := newTestApp(t)
	loaded(app, "chat", "Hy-MT2-7B-Q4_K_M.gguf")
	c := createConversation(t, app, TabColleague)
	rec := performChat(app, c, chatBody("请确认"), "send")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "event: complete") {
		t.Fatalf("流式回复失败: %s", rec.Body.String())
	}
	saved, _ := app.store.Conversation(c.ID)
	if len(saved.Messages) != 2 || saved.Messages[1].Content != "完成回复" {
		t.Fatalf("完整回复未保存: %+v", saved.Messages)
	}
	stats := saved.Messages[1].Stats
	if stats["total_context"] != float64(13) || stats["cache_n"] != float64(2) {
		t.Fatalf("统计错误: %+v", stats)
	}
	mock.mu.Lock()
	if !strings.Contains(mock.messages[0].Content.(string), "注意翻译的风格要严格符合【") {
		t.Fatal("模型未收到 Style 场景 Prompt")
	}
	mock.mu.Unlock()
	rec = performChat(app, c, chatBody("更礼貌一些"), "send")
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	saved, _ = app.store.Conversation(c.ID)
	if len(saved.Messages) != 4 {
		t.Fatal("多轮历史未保存")
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.messages) != 3 || mock.messages[1].Role != "assistant" {
		t.Fatalf("历史未实际发送给模型: %+v", mock.messages)
	}
}
func TestChatStopKeepsUserWithoutPartialReply(t *testing.T) {
	mock := newMockLlama()
	mock.block.Store(true)
	startMock(t, "18181", mock, "chat")
	app := newTestApp(t)
	loaded(app, "chat", "Hy-MT2-7B-Q4_K_M.gguf")
	c := createConversation(t, app, TabCustom)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- performChat(app, c, chatBody("等待停止"), "send") }()
	select {
	case <-mock.started:
	case <-time.After(3 * time.Second):
		t.Fatal("推理未开始")
	}
	req := httptest.NewRequest("POST", "/api/chat/"+c.ID+"/send", strings.NewReader(chatBody("不应进入")))
	req.SetPathValue("id", c.ID)
	rec := httptest.NewRecorder()
	app.handleChatSend(rec, req)
	if rec.Code != 409 {
		t.Fatalf("互斥请求状态=%d", rec.Code)
	}
	app.tasks.Stop()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("停止未结束处理")
	}
	saved, _ := app.store.Conversation(c.ID)
	if len(saved.Messages) != 1 || saved.Messages[0].Role != "user" {
		t.Fatalf("停止后历史错误: %+v", saved.Messages)
	}
}
func TestContextLimitRejectsWithoutTrimming(t *testing.T) {
	mock := newMockLlama()
	mock.tokens = 5000
	startMock(t, "18181", mock, "chat")
	app := newTestApp(t)
	loaded(app, "chat", "Hy-MT2-7B-Q4_K_M.gguf")
	c := createConversation(t, app, TabCustom)
	rec := performChat(app, c, chatBody("超长上下文"), "send")
	if !strings.Contains(rec.Body.String(), "请新建对话") {
		t.Fatalf("未明确阻断: %s", rec.Body.String())
	}
	saved, _ := app.store.Conversation(c.ID)
	if len(saved.Messages) != 1 {
		t.Fatal("上下文超限不应裁剪或生成")
	}
}
func TestEditAndRegenerate(t *testing.T) {
	mock := newMockLlama()
	startMock(t, "18181", mock, "chat")
	app := newTestApp(t)
	loaded(app, "chat", "Hy-MT2-7B-Q4_K_M.gguf")
	c := createConversation(t, app, TabCustom)
	c, _ = app.store.UpdateConversation(c.ID, func(v *Conversation) error {
		v.Messages = []Message{{ID: "u1", Role: "user", Content: "一"}, {ID: "a1", Role: "assistant", Content: "旧一", Stats: map[string]any{"old": 1}}, {ID: "u2", Role: "user", Content: "二"}, {ID: "a2", Role: "assistant", Content: "旧二"}}
		return nil
	})
	body := chatBody("编辑一")
	var payload map[string]any
	_ = json.Unmarshal([]byte(body), &payload)
	payload["edit_id"] = "u1"
	raw, _ := json.Marshal(payload)
	rec := performChat(app, c, string(raw), "send")
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	saved, _ := app.store.Conversation(c.ID)
	if len(saved.Messages) != 2 || saved.Messages[0].Content != "编辑一" || saved.Messages[1].Content != "完成回复" {
		t.Fatalf("编辑未截断后续: %+v", saved.Messages)
	}
	regen, _ := json.Marshal(map[string]any{"model": "Hy-MT2-7B-Q4_K_M.gguf", "romaji_mode": "spaced"})
	rec = performChat(app, saved, string(regen), "regenerate")
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	saved, _ = app.store.Conversation(c.ID)
	if len(saved.Messages) != 2 || saved.Messages[1].Stats["old"] == 1 {
		t.Fatal("重生成未删除旧回复及统计")
	}
}
func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func uploadImage(app *App, data []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("image", "test.png")
	_, _ = part.Write(data)
	_ = writer.Close()
	req := httptest.NewRequest("POST", "/api/images", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	app.handleImageUpload(rec, req)
	return rec
}
func TestImageValidation(t *testing.T) {
	if _, _, err := validateImage(pngBytes(t, 4000, 100)); err == nil {
		t.Fatal("横屏宽度超限未拒绝")
	}
	if _, _, err := validateImage(pngBytes(t, 100, 4000)); err == nil {
		t.Fatal("竖屏高度超限未拒绝")
	}
	if _, mime, err := validateImage(pngBytes(t, 2160, 10)); err != nil || mime != "image/png" {
		t.Fatalf("合法竖屏被拒绝: %v", err)
	}
	app := newTestApp(t)
	rec := uploadImage(app, bytes.Repeat([]byte{1}, maxImageBytes+1))
	if rec.Code != 400 {
		t.Fatalf("超过 4 MB 未直接拒绝: %d", rec.Code)
	}
	if len(app.store.Images()) != 0 {
		t.Fatal("非法图片被保存")
	}
}
func TestImageOCRTranslationAndFailurePreservation(t *testing.T) {
	vl := newMockLlama()
	chat := newMockLlama()
	startMock(t, "18182", vl, "vl")
	startMock(t, "18181", chat, "chat")
	app := newTestApp(t)
	loaded(app, "vl", "PaddleOCR-VL-1.6-GGUF.gguf")
	loaded(app, "chat", "Hy-MT2-7B-Q4_K_M.gguf")
	settings := app.store.Settings()
	settings.ImageModel = "Hy-MT2-7B-Q4_K_M.gguf"
	_, _ = app.store.UpdateSettings(func(s *Settings) error { *s = settings; return nil })
	original := pngBytes(t, 32, 20)
	rec := uploadImage(app, original)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ocr_complete") {
		t.Fatalf("OCR 失败: %s", rec.Body.String())
	}
	images := app.store.Images()
	if len(images) != 1 || images[0].OCR != "日文\nEnglish 123" {
		t.Fatalf("OCR 未完整保存: %+v", images)
	}
	path := filepath.Join(app.store.dir, "images", images[0].ID+".png")
	stored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(stored, original) {
		t.Fatal("原图被压缩、转换或修改")
	}
	req := httptest.NewRequest("POST", "/api/images/"+images[0].ID+"/translate", strings.NewReader(`{"model":"Hy-MT2-7B-Q4_K_M.gguf"}`))
	req.SetPathValue("id", images[0].ID)
	rec = httptest.NewRecorder()
	app.handleImageTranslate(rec, req, images[0].ID)
	if !strings.Contains(rec.Body.String(), "translation_complete") {
		t.Fatalf("翻译失败: %s", rec.Body.String())
	}
	if vl.ocrCalls.Load() != 1 {
		t.Fatal("翻译重新执行了 OCR")
	}
	saved, _ := app.store.Image(images[0].ID)
	if saved.Translation != "完成回复" {
		t.Fatal("完整翻译未保存")
	}
	chat.fail.Store(true)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/images/"+images[0].ID+"/translate", strings.NewReader(`{"model":"Hy-MT2-7B-Q4_K_M.gguf"}`))
	req.SetPathValue("id", images[0].ID)
	app.handleImageTranslate(rec, req, images[0].ID)
	saved, _ = app.store.Image(images[0].ID)
	if saved.OCR != "日文\nEnglish 123" || saved.Translation != "完成回复" {
		t.Fatal("失败的不完整翻译覆盖了历史完整结果")
	}
	reopened, err := NewStore(app.store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if persisted, ok := reopened.Image(images[0].ID); !ok || persisted.OCR == "" {
		t.Fatal("图片任务重启后不可用")
	}
}
func TestStaticAndHardware(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	app.routes().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "offline") {
		t.Fatal("静态页面不可访问")
	}
	hardware := app.hardware.Sample()
	if hardware.MemoryTotal == 0 || hardware.RefreshSeconds != 2 {
		t.Fatalf("硬件监控无效: %+v", hardware)
	}
}
func TestActualGGUFMetadata(t *testing.T) {
	paths := []string{filepath.Join("..", "gguf", "chat", "Hy-MT2-7B-Q4_K_M.gguf"), filepath.Join("..", "gguf", "vl", "PaddleOCR-VL-1.6-GGUF.gguf")}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Skip("开发模型不存在")
		}
		info, err := readGGUF(path)
		if err != nil {
			t.Fatalf("读取 %s: %v", path, err)
		}
		if info.MaxContext <= 0 || numberValue(info.Meta["n_vocab"]) <= 0 || numberValue(info.Meta["n_embd"]) <= 0 {
			t.Fatalf("元数据不完整: %+v", info)
		}
		t.Logf("%s context=%d params=%.0f", info.Name, info.MaxContext, numberValue(info.Meta["n_params"]))
	}
}
