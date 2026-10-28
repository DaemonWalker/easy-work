package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type App struct {
	root      string
	store     *Store
	models    *ModelManager
	tasks     *TaskManager
	hardware  *hardwareMonitor
	modelList []ModelInfo
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", a.handleState)
	mux.HandleFunc("GET /api/hardware", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, a.hardware.Sample()) })
	mux.HandleFunc("PUT /api/settings", a.handleSettings)
	mux.HandleFunc("PUT /api/preferences", a.handlePreferences)
	mux.HandleFunc("POST /api/stop", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"stopped": a.tasks.Stop()})
	})
	mux.HandleFunc("GET /api/conversations", a.handleConversations)
	mux.HandleFunc("POST /api/conversations", a.handleNewConversation)
	mux.HandleFunc("POST /api/chat/{id}/send", a.handleChatSend)
	mux.HandleFunc("POST /api/chat/{id}/regenerate", a.handleRegenerate)
	mux.HandleFunc("GET /api/models/{kind}/details", a.handleModelDetails)
	mux.HandleFunc("POST /api/models/{kind}/unload", a.handleUnload)
	mux.HandleFunc("GET /api/images", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, a.store.Images()) })
	mux.HandleFunc("POST /api/images", a.handleImageUpload)
	mux.HandleFunc("POST /api/images/{id}/retry", func(w http.ResponseWriter, r *http.Request) { a.handleImageRetry(w, r, r.PathValue("id")) })
	mux.HandleFunc("POST /api/images/{id}/translate", func(w http.ResponseWriter, r *http.Request) { a.handleImageTranslate(w, r, r.PathValue("id")) })
	mux.HandleFunc("GET /images/{id}/original", func(w http.ResponseWriter, r *http.Request) { a.serveImage(w, r, r.PathValue("id")) })
	mux.HandleFunc("/", a.serveStatic)
	return securityHeaders(mux)
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}
func (a *App) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		fail(w, 405, "方法不允许")
		return
	}
	path := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if path == "." {
		path = "index.html"
	}
	full := filepath.Join(a.root, "html", path)
	if !strings.HasPrefix(full, filepath.Join(a.root, "html")+string(os.PathSeparator)) && path != "index.html" {
		fail(w, 403, "禁止访问")
		return
	}
	if st, err := os.Stat(full); err == nil && !st.IsDir() {
		http.ServeFile(w, r, full)
		return
	}
	http.ServeFile(w, r, filepath.Join(a.root, "html", "index.html"))
}
func (a *App) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, StateResponse{Settings: a.store.Settings(), Models: a.modelList, Status: a.models.Statuses(), Task: a.tasks.State(), Hardware: a.hardware.Sample()})
}

var validThemes = map[string]bool{"cloud": true, "paper": true, "green": true, "ice": true, "graphite": true, "midnight": true, "black": true, "sakura": true, "lavender": true, "sunset": true, "contrast-light": true, "contrast-dark": true}

func validateSettings(s Settings, models []ModelInfo) error {
	if !validThemes[s.Theme] {
		return errors.New("未知主题")
	}
	if s.Threads < 1 || s.Threads > 256 {
		return errors.New("线程数必须为 1–256 的正整数")
	}
	if s.Context < 1 {
		return errors.New("Chat 上下文必须是正整数")
	}
	if s.Generation.MaxTokens < 1 || s.Generation.MaxTokens > 4096 {
		return errors.New("最大输出 Token 必须为 1–4096")
	}
	g := s.Generation
	if g.Temperature != nil && (*g.Temperature < 0 || *g.Temperature > 2) {
		return errors.New("temperature 必须在 0–2 之间")
	}
	if g.TopP != nil && (*g.TopP < 0 || *g.TopP > 1) {
		return errors.New("top_p 必须在 0–1 之间")
	}
	if g.TopK != nil && *g.TopK < 0 {
		return errors.New("top_k 不能为负数")
	}
	if g.MinP != nil && (*g.MinP < 0 || *g.MinP > 1) {
		return errors.New("min_p 必须在 0–1 之间")
	}
	if g.RepeatPenalty != nil && *g.RepeatPenalty <= 0 {
		return errors.New("repeat_penalty 必须大于 0")
	}
	for _, m := range models {
		if s.Context > m.MaxContext && m.MaxContext > 0 {
			return fmt.Errorf("Chat 上下文不能超过模型 %s 声明的最大上下文 %d", m.Name, m.MaxContext)
		}
	}
	return nil
}
func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	var next Settings
	if err := decodeJSON(r, &next); err != nil {
		fail(w, 400, "设置格式错误")
		return
	}
	if next.TabModels == nil {
		next.TabModels = a.store.Settings().TabModels
	}
	if err := validateSettings(next, a.modelList); err != nil {
		fail(w, 400, err.Error())
		return
	}
	old := a.store.Settings()
	runtimeChanged := old.Threads != next.Threads || old.Context != next.Context
	if runtimeChanged && a.tasks.State().Busy {
		fail(w, 409, "模型任务期间不能修改线程数或 Chat 上下文")
		return
	}
	saved, err := a.store.UpdateSettings(func(s *Settings) error { *s = next; return nil })
	if err != nil {
		fail(w, 500, "设置保存失败")
		return
	}
	if runtimeChanged {
		go func() {
			var kinds []string
			if old.Threads != next.Threads {
				kinds = []string{"chat", "vl"}
			} else {
				kinds = []string{"chat"}
			}
			log.Printf("设置变更触发模型重载: kinds=%v", kinds)
			if err := a.models.Reload(kinds...); err != nil {
				log.Printf("设置重载失败: %v", err)
			}
		}()
	}
	writeJSON(w, 200, saved)
}
func (a *App) handlePreferences(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tab            string  `json:"tab"`
		Model          *string `json:"model"`
		ImageModel     *string `json:"image_model"`
		ImageTranslate *bool   `json:"image_translate"`
	}
	if decodeJSON(r, &body) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if body.Tab != "" && !validTab(body.Tab) {
		fail(w, 400, "未知页面")
		return
	}
	if body.Model != nil {
		if *body.Model != "" {
			if _, ok := a.models.models[*body.Model]; !ok {
				fail(w, 400, "模型不存在")
				return
			}
			if body.Tab != TabCustom && !isTranslationModel(*body.Model) {
				fail(w, 400, "固定翻译场景只能使用指定翻译模型")
			}
		}
	}
	if body.ImageModel != nil && *body.ImageModel != "" && !isTranslationModel(*body.ImageModel) {
		fail(w, 400, "图像翻译只能使用指定翻译模型")
	}
	saved, err := a.store.UpdateSettings(func(s *Settings) error {
		if body.Tab != "" && body.Model != nil {
			s.TabModels[body.Tab] = *body.Model
		}
		if body.ImageModel != nil {
			s.ImageModel = *body.ImageModel
		}
		if body.ImageTranslate != nil {
			s.ImageTranslate = *body.ImageTranslate
		}
		return nil
	})
	if err != nil {
		fail(w, 500, "设置保存失败")
		return
	}
	writeJSON(w, 200, saved)
}
func (a *App) handleConversations(w http.ResponseWriter, r *http.Request) {
	tab := r.URL.Query().Get("tab")
	if !validTab(tab) {
		fail(w, 400, "未知页面")
		return
	}
	writeJSON(w, 200, a.store.Conversations(tab))
}
func (a *App) handleNewConversation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tab string `json:"tab"`
	}
	if decodeJSON(r, &body) != nil || !validTab(body.Tab) {
		fail(w, 400, "未知页面")
		return
	}
	c, err := a.store.NewConversation(body.Tab, a.store.Settings().TabModels[body.Tab])
	if err != nil {
		fail(w, 500, "创建对话失败")
		return
	}
	writeJSON(w, 201, c)
}
func (a *App) handleModelDetails(w http.ResponseWriter, r *http.Request) {
	details, err := a.models.Details(r.PathValue("kind"))
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, details)
}
func (a *App) handleUnload(w http.ResponseWriter, r *http.Request) {
	if a.tasks.State().Busy {
		fail(w, 409, "任务期间不能卸载模型")
		return
	}
	if err := a.models.Unload(r.PathValue("kind")); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, a.models.Statuses())
}

type chatRequest struct {
	Model      string `json:"model"`
	Text       string `json:"text"`
	EditID     string `json:"edit_id,omitempty"`
	RomajiMode string `json:"romaji_mode,omitempty"`
}

func validRomajiMode(mode string) bool {
	return mode == "" || mode == "normal" || mode == "spaced" || mode == "okurigana" || mode == "furigana"
}
func (a *App) validateChatModel(tab, model string) error {
	if _, ok := a.models.models[model]; !ok {
		return errors.New("请选择可用模型")
	}
	if tab != TabCustom && !isTranslationModel(model) {
		return errors.New("固定翻译场景只能使用指定翻译模型")
	}
	return nil
}
func (a *App) handleChatSend(w http.ResponseWriter, r *http.Request) {
	conversationID := r.PathValue("id")
	c, ok := a.store.Conversation(conversationID)
	if !ok {
		fail(w, 404, "对话不存在")
		return
	}
	var body chatRequest
	if decodeJSON(r, &body) != nil || strings.TrimSpace(body.Text) == "" || !validRomajiMode(body.RomajiMode) {
		fail(w, 400, "输入不能为空或格式错误")
		return
	}
	if err := a.validateChatModel(c.Tab, body.Model); err != nil {
		fail(w, 400, err.Error())
		return
	}
	body.Text = strings.TrimSpace(body.Text)
	log.Printf("用户输入: conversation=%s tab=%s model=%s edit_id=%s\n%s", conversationID, c.Tab, body.Model, body.EditID, body.Text)
	taskID, ctx, err := a.tasks.Start(r.Context(), "chat")
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	defer a.tasks.Finish(taskID)
	c, err = a.store.UpdateConversation(c.ID, func(v *Conversation) error {
		v.Model = body.Model
		if body.EditID != "" {
			for i, m := range v.Messages {
				if m.ID == body.EditID {
					if m.Role != "user" {
						return errors.New("只能编辑用户消息")
					}
					v.Messages = v.Messages[:i+1]
					v.Messages[i].Content = body.Text
					return nil
				}
			}
			return errors.New("待编辑用户消息不存在")
		}
		v.Messages = append(v.Messages, Message{ID: id(), Role: "user", Content: body.Text, Model: body.Model, CreatedAt: now()})
		if v.Title == "新对话" {
			v.Title = trimmedTitle(body.Text)
		}
		return nil
	})
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	send, err := openSSE(w)
	if err != nil {
		return
	}
	a.runChat(ctx, taskID, c, body.Model, body.RomajiMode, send)
}
func (a *App) handleRegenerate(w http.ResponseWriter, r *http.Request) {
	c, ok := a.store.Conversation(r.PathValue("id"))
	if !ok {
		fail(w, 404, "对话不存在")
		return
	}
	var body struct {
		Model      string `json:"model"`
		RomajiMode string `json:"romaji_mode"`
	}
	if decodeJSON(r, &body) != nil || !validRomajiMode(body.RomajiMode) {
		fail(w, 400, "请求格式错误")
		return
	}
	if err := a.validateChatModel(c.Tab, body.Model); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(c.Messages) == 0 || c.Messages[len(c.Messages)-1].Role != "assistant" {
		fail(w, 400, "只有最后一条模型回复可以重新生成")
		return
	}
	taskID, ctx, err := a.tasks.Start(r.Context(), "chat")
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	defer a.tasks.Finish(taskID)
	c, err = a.store.UpdateConversation(c.ID, func(v *Conversation) error {
		v.Messages = v.Messages[:len(v.Messages)-1]
		v.Model = body.Model
		return nil
	})
	if err != nil {
		fail(w, 500, "更新对话失败")
		return
	}
	send, err := openSSE(w)
	if err != nil {
		return
	}
	a.runChat(ctx, taskID, c, body.Model, body.RomajiMode, send)
}
func (a *App) runChat(ctx context.Context, taskID string, c Conversation, model, romajiMode string, send func(string, any)) {
	a.tasks.Stage(taskID, "加载 Chat 模型")
	send("status", map[string]string{"stage": "加载 Chat 模型"})
	info := a.models.models[model]
	if info.MaxContext > 0 && a.store.Settings().Context > info.MaxContext {
		log.Printf("Chat 上下文配置错误: task=%s conversation=%s context=%d max_context=%d", taskID, c.ID, a.store.Settings().Context, info.MaxContext)
		send("error", map[string]string{"error": "当前 Chat 上下文超过模型最大能力，请修改设置"})
		return
	}
	if err := a.models.Ensure(ctx, "chat", model); err != nil {
		send("error", map[string]string{"error": err.Error()})
		return
	}
	messages := make([]chatMessage, 0, len(c.Messages))
	for _, m := range c.Messages {
		content := m.Content
		if m.Role == "user" {
			content = userPrompt(c.Tab, m.Content)
		}
		messages = append(messages, chatMessage{Role: m.Role, Content: content})
	}
	a.tasks.Stage(taskID, "检查上下文")
	send("status", map[string]string{"stage": "检查上下文"})
	tokens, err := a.models.countTokens(ctx, messages)
	if err != nil {
		log.Printf("Chat Token 检查失败: task=%s conversation=%s error=%v", taskID, c.ID, err)
		send("error", map[string]string{"error": err.Error()})
		return
	}
	contextSize := a.store.Settings().Context
	if tokens > contextSize {
		log.Printf("Chat 输入超过上下文: task=%s conversation=%s tokens=%d context=%d", taskID, c.ID, tokens, contextSize)
		send("error", map[string]string{"error": fmt.Sprintf("输入共 %d Token，超过当前实际上下文 %d；请新建对话。不会自动裁剪历史", tokens, contextSize)})
		return
	}
	if _, err = a.store.UpdateConversation(c.ID, func(v *Conversation) error { v.ContextUsed = tokens; return nil }); err != nil {
		log.Printf("上下文统计保存失败: task=%s conversation=%s error=%v", taskID, c.ID, err)
		send("error", map[string]string{"error": "上下文统计保存失败"})
		return
	}
	a.tasks.Stage(taskID, "生成回复")
	send("status", map[string]string{"stage": "生成回复"})
	result, err := a.models.stream(ctx, "chat", messages, a.store.Settings().Generation, func(delta string) error { send("delta", map[string]string{"text": delta}); return nil })
	if err != nil {
		send("error", map[string]string{"error": taskError(err)})
		return
	}
	assistant := Message{ID: id(), Role: "assistant", Content: result.Text, Model: model, RomajiMode: romajiMode, Stats: result.Stats, CreatedAt: time.Now().UTC()}
	saved, err := a.store.UpdateConversation(c.ID, func(v *Conversation) error {
		v.Messages = append(v.Messages, assistant)
		v.ContextUsed = int(statNumber(result.Stats, "total_context", tokens))
		return nil
	})
	if err != nil {
		log.Printf("完整回复保存失败: task=%s conversation=%s error=%v", taskID, c.ID, err)
		send("error", map[string]string{"error": "完整回复保存失败"})
		return
	}
	log.Printf("Chat 回复已保存: task=%s conversation=%s message=%s", taskID, c.ID, assistant.ID)
	send("complete", saved)
}
