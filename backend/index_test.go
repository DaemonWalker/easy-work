package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testHyModel    = "Hy-MT2-7B-Q4_K_M.gguf"
	testIndexModel = "Index-Translate-9B.Q4_K_M.gguf"
)

func TestIndexPromptComposition(t *testing.T) {
	scenes := map[string]string{
		TabColleague: indexColleaguePrompt,
		TabDocument:  indexDocumentPrompt,
		TabTestcase:  indexTestcasePrompt,
	}
	for tab, scene := range scenes {
		got := indexPrompts[tab]
		want := indexCommonPrompt + "\n" + scene
		if got != want {
			t.Errorf("%s 未按通用段和场景段拼装", tab)
		}
		if strings.Contains(got, "示例（只示范语气、句式和逻辑") {
			t.Errorf("%s 不应包含 few-shot 示例", tab)
		}
	}
}

func TestIndexPromptBaselineAndRouting(t *testing.T) {
	for _, tab := range []string{TabColleague, TabDocument, TabTestcase} {
		for _, model := range []string{"Index-Translate-9B.Q4_K_M.gguf", "Index-Translate-9B.Q5_K_M.gguf"} {
			want := indexUserPrompt(tab, "原文")
			got := userPrompt(tab, model, "原文")
			if got != want {
				t.Errorf("%s/%s 未使用 Index 包装", tab, model)
			}
			source := strings.Index(got, "【源文】")
			constraints := strings.Index(got, "【约束要求】")
			if source < 0 || constraints < source || !strings.HasSuffix(got, "只输出译文，不要有任何额外说明。") {
				t.Errorf("%s/%s 未使用 instTrans 结构", tab, model)
			}
		}
		if got := userPrompt(tab, testHyModel, "原文"); !strings.HasPrefix(got, "请将以下文本翻译为日语。\n注意翻译的风格要严格符合【") {
			t.Errorf("%s 的 Hy-MT2 Prompt 未保持 Style", tab)
		}
	}
	if got := userPrompt(TabJa2zh, testIndexModel, "日文"); got != "【待翻译文本】\n日文\n\n【翻译任务】\n"+prompts[TabJa2zh] {
		t.Fatal("日译中路由不应复用中译日 Prompt")
	}
	if got := userPrompt(TabCustom, testIndexModel, "完整 Prompt"); got != "完整 Prompt" {
		t.Fatal("自由 Chat 应原文直传")
	}
	for _, prompt := range indexPrompts {
		if !strings.HasPrefix(prompt, indexCommonPrompt) {
			t.Fatal("Index 中译日 Prompt 缺少通用段")
		}
	}
}

func TestModelCapability(t *testing.T) {
	for _, tab := range []string{TabColleague, TabDocument, TabTestcase, TabJa2zh} {
		for _, model := range []string{testHyModel, "HY-MT2-7B-Q6_K.gguf", "Index-Translate-9B.Q4_K_M.gguf", "Index-Translate-9B.Q5_K_M.gguf"} {
			if !allowsModel(tab, model) {
				t.Errorf("%s 应允许 %s", tab, model)
			}
		}
	}
	for _, model := range []string{"Index-Translate-9B.Q4_K_M.gguf", "Index-Translate-9B.Q5_K_M.gguf"} {
		if !isJaZhModel(model) || !isImageTranslationModel(model) || !isImageOCRModel(model) {
			t.Errorf("日译中、图像翻译和图像识别应开放 %s", model)
		}
		if !allowsModel(TabCustom, model) {
			t.Errorf("自由 Chat 应允许 %s", model)
		}
	}
	if !isJaZhModel(testHyModel) || !isImageTranslationModel(testHyModel) || allowsModel("unknown", testHyModel) {
		t.Fatal("模型能力边界错误")
	}
}

func TestIndexModelPreferences(t *testing.T) {
	app := newTestApp(t)
	for _, tab := range []string{TabColleague, TabDocument, TabTestcase, TabJa2zh} {
		body := `{"tab":` + strconvQuote(tab) + `,"model":` + strconvQuote(testIndexModel) + `}`
		req := httptest.NewRequest("PUT", "/api/preferences", strings.NewReader(body))
		rec := httptest.NewRecorder()
		app.handlePreferences(rec, req)
		if rec.Code != 200 {
			t.Errorf("%s 不应拒绝 Index: %s", tab, rec.Body.String())
		}
	}
	for _, body := range []string{`{"image_model":"Index-Translate-9B.Q4_K_M.gguf"}`, `{"image_ocr_model":"Index-Translate-9B.Q4_K_M.gguf"}`} {
		req := httptest.NewRequest("PUT", "/api/preferences", strings.NewReader(body))
		rec := httptest.NewRecorder()
		app.handlePreferences(rec, req)
		if rec.Code != 200 {
			t.Errorf("图像设置不应拒绝 Index: %s", rec.Body.String())
		}
	}
	req := httptest.NewRequest("PUT", "/api/preferences", strings.NewReader(`{"image_ocr_model":"unknown.gguf"}`))
	rec := httptest.NewRecorder()
	app.handlePreferences(rec, req)
	if rec.Code != 400 {
		t.Errorf("图像识别不应接受未知模型: %s", rec.Body.String())
	}
}

func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestGenerationForIndex(t *testing.T) {
	original := defaultGeneration()
	settings := generationFor(testIndexModel, original)
	if settings.Temperature == nil || *settings.Temperature != 0 {
		t.Fatal("Index 未固定 temperature=0")
	}
	if settings.TopP != nil || settings.TopK != nil || settings.MinP != nil || settings.RepeatPenalty != nil {
		t.Fatalf("Index 应让其余采样参数使用服务端默认值: %+v", settings)
	}
	if settings.MaxTokens != original.MaxTokens {
		t.Fatal("Index 不应改变最大输出 Token")
	}
	payload := generationPayload(testIndexModel, settings)
	kwargs, ok := payload["chat_template_kwargs"].(map[string]any)
	if !ok || kwargs["enable_thinking"] != false {
		t.Fatalf("Index 未关闭 thinking: %+v", payload)
	}
	hyPayload := generationPayload(testHyModel, generationFor(testHyModel, original))
	if hyPayload["temperature"] != 0.7 || hyPayload["top_p"] != 0.6 || hyPayload["top_k"] != 20 || hyPayload["repeat_penalty"] != 1.05 {
		t.Fatalf("Hy-MT2 采样参数被改变: %+v", hyPayload)
	}
	if _, exists := hyPayload["chat_template_kwargs"]; exists {
		t.Fatal("Hy-MT2 不应携带 Index 的模板参数")
	}
}

func TestCountTokensIndexKwargs(t *testing.T) {
	mock := newMockLlama()
	startMock(t, "18181", mock, "chat")
	app := newTestApp(t)
	messages := []chatMessage{{Role: "user", Content: "原文"}}
	if _, err := app.models.countTokens(context.Background(), testIndexModel, messages); err != nil {
		t.Fatal(err)
	}
	mock.mu.Lock()
	kwargs := mock.templateKwargs
	mock.mu.Unlock()
	if kwargs["enable_thinking"] != false {
		t.Fatalf("apply-template 未关闭 thinking: %+v", kwargs)
	}
	if _, err := app.models.countTokens(context.Background(), testHyModel, messages); err != nil {
		t.Fatal(err)
	}
	mock.mu.Lock()
	kwargs = mock.templateKwargs
	mock.mu.Unlock()
	if kwargs != nil {
		t.Fatalf("Hy-MT2 apply-template 不应带 kwargs: %+v", kwargs)
	}
}

func TestPromptUsesConversationModel(t *testing.T) {
	mock := newMockLlama()
	startMock(t, "18181", mock, "chat")
	app := newTestApp(t)
	loaded(app, "chat", testIndexModel)
	c := createConversation(t, app, TabDocument)
	body := `{"model":"Index-Translate-9B.Q4_K_M.gguf","text":"原文一","romaji_mode":"spaced"}`
	if rec := performChat(app, c, body, "send"); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	saved, _ := app.store.Conversation(c.ID)
	if saved.Model != testIndexModel {
		t.Fatalf("空对话未锁定 Index: %s", saved.Model)
	}
	mock.mu.Lock()
	first := mock.messages[0].Content.(string)
	completion := mock.completion
	mock.mu.Unlock()
	if first != userPrompt(TabDocument, testIndexModel, "原文一") {
		t.Fatal("首条消息未使用对话模型的 Index Prompt")
	}
	if completion["temperature"] != float64(0) || completion["chat_template_kwargs"].(map[string]any)["enable_thinking"] != false {
		t.Fatalf("Index 实际请求参数错误: %+v", completion)
	}
	for _, key := range []string{"top_p", "top_k", "min_p", "repeat_penalty"} {
		if _, exists := completion[key]; exists {
			t.Errorf("Index 实际请求不应携带 %s", key)
		}
	}
	body = `{"model":"Index-Translate-9B.Q4_K_M.gguf","text":"原文二","romaji_mode":"spaced"}`
	if rec := performChat(app, saved, body, "send"); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.messages) != 3 {
		t.Fatalf("历史消息数错误: %+v", mock.messages)
	}
	for i, text := range []string{"原文一", "原文二"} {
		if got := mock.messages[i*2].Content.(string); got != userPrompt(TabDocument, testIndexModel, text) {
			t.Errorf("历史消息 %d 未统一使用对话模型 Prompt", i)
		}
	}
}

func TestIndexJa2zhChat(t *testing.T) {
	mock := newMockLlama()
	startMock(t, "18181", mock, "chat")
	app := newTestApp(t)
	loaded(app, "chat", testIndexModel)
	c := createConversation(t, app, TabJa2zh)
	body := `{"model":"Index-Translate-9B.Q4_K_M.gguf","text":"日文","romaji_mode":"spaced"}`
	if rec := performChat(app, c, body, "send"); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	saved, _ := app.store.Conversation(c.ID)
	if saved.Model != testIndexModel {
		t.Fatalf("日译中对话未锁定 Index: %s", saved.Model)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	want := "【待翻译文本】\n日文\n\n【翻译任务】\n" + prompts[TabJa2zh]
	if got := mock.messages[0].Content; got != want {
		t.Fatalf("Index 日译中未使用独立 Prompt: %v", got)
	}
	if mock.completion["temperature"] != float64(0) || mock.completion["chat_template_kwargs"].(map[string]any)["enable_thinking"] != false {
		t.Fatalf("Index 日译中实际请求参数错误: %+v", mock.completion)
	}
}

func TestConversationModelLocked(t *testing.T) {
	mock := newMockLlama()
	startMock(t, "18181", mock, "chat")
	app := newTestApp(t)
	loaded(app, "chat", testIndexModel)
	c := createConversation(t, app, TabDocument)
	body := `{"model":"Index-Translate-9B.Q4_K_M.gguf","text":"原文","romaji_mode":"spaced"}`
	if rec := performChat(app, c, body, "send"); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	saved, _ := app.store.Conversation(c.ID)
	for _, test := range []struct {
		name     string
		endpoint string
		body     string
	}{
		{"发送时切换", "send", `{"model":"Hy-MT2-7B-Q4_K_M.gguf","text":"切换"}`},
		{"发送时为空", "send", `{"model":"","text":"空模型"}`},
		{"编辑时切换", "send", `{"model":"Hy-MT2-7B-Q4_K_M.gguf","text":"编辑","edit_id":"` + saved.Messages[0].ID + `"}`},
		{"重生成时切换", "regenerate", `{"model":"Hy-MT2-7B-Q4_K_M.gguf","romaji_mode":"spaced"}`},
		{"重生成时为空", "regenerate", `{"model":"","romaji_mode":"spaced"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := performChat(app, saved, test.body, test.endpoint)
			if rec.Code != 400 {
				t.Fatalf("状态=%d body=%s", rec.Code, rec.Body.String())
			}
			current, _ := app.store.Conversation(c.ID)
			if current.Model != testIndexModel || len(current.Messages) != 2 {
				t.Fatalf("被拒绝的请求改变了对话: %+v", current)
			}
		})
	}
	rec := performChat(app, saved, `{"model":"Index-Translate-9B.Q4_K_M.gguf","romaji_mode":"spaced"}`, "regenerate")
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	saved, _ = app.store.Conversation(c.ID)
	if saved.Model != testIndexModel || len(saved.Messages) != 2 {
		t.Fatalf("同模型重生成失败: %+v", saved)
	}
}

func TestLegacyConversationModelBackfill(t *testing.T) {
	mock := newMockLlama()
	startMock(t, "18181", mock, "chat")
	app := newTestApp(t)
	loaded(app, "chat", testHyModel)
	c := createConversation(t, app, TabCustom)
	c, err := app.store.UpdateConversation(c.ID, func(v *Conversation) error {
		v.Model = ""
		v.Messages = []Message{{ID: "u", Role: "user", Content: "旧消息"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec := performChat(app, c, chatBody("新消息"), "send"); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	saved, _ := app.store.Conversation(c.ID)
	if saved.Model != testHyModel {
		t.Fatalf("旧对话未回写模型: %q", saved.Model)
	}
}

func TestIndexImageOCRAndTranslation(t *testing.T) {
	vl := newMockLlama()
	chat := newMockLlama()
	startMock(t, "18182", vl, "vl")
	startMock(t, "18181", chat, "chat")
	app := newTestApp(t)
	loaded(app, "vl", testIndexModel)
	loaded(app, "chat", testIndexModel)
	settings := app.store.Settings()
	settings.ImageOCRModel = testIndexModel
	settings.ImageModel = testIndexModel
	if _, err := app.store.UpdateSettings(func(s *Settings) error { *s = settings; return nil }); err != nil {
		t.Fatal(err)
	}
	if rec := uploadImage(app, pngBytes(t, 32, 20)); rec.Code != 200 || !strings.Contains(rec.Body.String(), "ocr_complete") {
		t.Fatalf("Index OCR 失败: %s", rec.Body.String())
	}
	vl.mu.Lock()
	ocrPayload := vl.completion
	vl.mu.Unlock()
	if ocrPayload["temperature"] != float64(0) || ocrPayload["chat_template_kwargs"].(map[string]any)["enable_thinking"] != false {
		t.Fatalf("Index OCR 参数错误: %+v", ocrPayload)
	}
	ocrMessages := ocrPayload["messages"].([]any)
	ocrContent := ocrMessages[0].(map[string]any)["content"].([]any)
	wantOCRPrompt := "请完整、准确地提取图片中的全部文字，保持原文的语言、内容、顺序和换行，直接输出识别结果，不要进行任何解释。\n\n只输出图片中的文字，不得重复、遗漏或添加任何文字，不要输出分析、总结、翻译或其他额外内容。"
	if got := ocrContent[0].(map[string]any)["text"]; got != wantOCRPrompt {
		t.Fatalf("Index OCR Prompt 错误: %q", got)
	}
	images := app.store.Images()
	if len(images) != 1 {
		t.Fatal("Index OCR 结果未保存")
	}
	completed := false
	app.runTranslation(context.Background(), "test", images[0], testIndexModel, func(name string, value any) {
		if name == "translation_complete" {
			completed = true
		}
	})
	if !completed {
		t.Fatal("Index 图像翻译未完成")
	}
	chat.mu.Lock()
	defer chat.mu.Unlock()
	want := "【待翻译文本】\n" + images[0].OCR + "\n\n【翻译任务】\n" + prompts[TabJa2zh]
	if got := chat.messages; len(got) != 0 {
		t.Fatalf("图像翻译不应调用 apply-template: %+v", got)
	}
	if got := chat.completion["messages"].([]any); len(got) != 1 || got[0].(map[string]any)["content"] != want {
		t.Fatalf("Index 图像翻译 Prompt 错误: %+v", chat.completion["messages"])
	}
}
