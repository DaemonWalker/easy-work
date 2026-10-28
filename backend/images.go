package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
)

const maxImageBytes = 4 * 1024 * 1024

func validateImage(data []byte) (image.Config, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return cfg, "", errors.New("图片无法解码，仅支持有效 PNG、JPEG、WebP")
	}
	if format != "png" && format != "jpeg" && format != "webp" {
		return cfg, "", errors.New("仅支持 PNG、JPEG、WebP 图片")
	}
	landscape := cfg.Width <= 3840 && cfg.Height <= 2160
	portrait := cfg.Width <= 2160 && cfg.Height <= 3840
	if !landscape && !portrait {
		return cfg, "", errors.New("图片分辨率超限：横屏最大 3840×2160，竖屏最大 2160×3840")
	}
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return cfg, "", errors.New("图片内容损坏或无法处理")
	}
	mime := "image/" + format
	if format == "jpeg" {
		mime = "image/jpeg"
	}
	return cfg, mime, nil
}
func imageExtension(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	}
	return ".jpg"
}
func (a *App) handleImageUpload(w http.ResponseWriter, r *http.Request) {
	taskID, ctx, err := a.tasks.Start(r.Context(), "ocr")
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	defer a.tasks.Finish(taskID)
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes+1024*1024)
	if err = r.ParseMultipartForm(maxImageBytes + 1024*1024); err != nil {
		fail(w, 400, "图片不能超过 4 MB，且每次只能上传一张")
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["image"]
	if len(files) != 1 {
		fail(w, 400, "每个图像任务只能包含一张图片")
		return
	}
	file, err := files[0].Open()
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, maxImageBytes+1))
	file.Close()
	if err != nil {
		fail(w, 400, "读取图片失败")
		return
	}
	if len(data) > maxImageBytes {
		fail(w, 400, "原始图片不能超过 4 MB")
		return
	}
	cfg, mime, err := validateImage(data)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	n := now()
	task := ImageTask{ID: id(), Filename: filepath.Base(files[0].Filename), Mime: mime, Width: cfg.Width, Height: cfg.Height, CreatedAt: n, UpdatedAt: n}
	path := filepath.Join(a.store.dir, "images", task.ID+imageExtension(mime))
	if err = os.WriteFile(path, data, 0644); err != nil {
		fail(w, 500, "保存原图失败")
		return
	}
	if err = a.store.AddImage(task); err != nil {
		fail(w, 500, "保存图片任务失败")
		return
	}
	send, err := openSSE(w)
	if err != nil {
		return
	}
	send("image", task)
	a.runOCR(ctx, taskID, task, mime, data, send)
}
func (a *App) runOCR(ctx context.Context, taskID string, task ImageTask, mime string, data []byte, send func(string, any)) {
	a.tasks.Stage(taskID, "加载 VL 模型")
	send("status", map[string]string{"stage": "加载 VL 模型"})
	if err := a.models.Ensure(ctx, "vl", ""); err != nil {
		send("error", map[string]string{"error": err.Error()})
		return
	}
	a.tasks.Stage(taskID, "OCR 识别")
	send("status", map[string]string{"stage": "OCR 识别"})
	log.Printf("OCR 开始: task=%s image=%s filename=%s mime=%s width=%d height=%d bytes=%d", taskID, task.ID, task.Filename, mime, task.Width, task.Height, len(data))
	url := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	messages := []chatMessage{{Role: "user", Content: []any{map[string]any{"type": "text", "text": "OCR:"}, map[string]any{"type": "image_url", "image_url": map[string]string{"url": url}}}}}
	started := time.Now()
	result, err := a.models.stream(ctx, "vl", messages, a.store.Settings().Generation, func(delta string) error { send("delta", map[string]string{"text": delta}); return nil })
	if err != nil {
		send("error", map[string]string{"error": taskError(err)})
		return
	}
	result.Stats["ocr_ms"] = time.Since(started).Milliseconds()
	saved, err := a.store.UpdateImage(task.ID, func(v *ImageTask) error { v.OCR = result.Text; v.OCRStats = result.Stats; return nil })
	if err != nil {
		log.Printf("OCR 结果保存失败: task=%s image=%s error=%v", taskID, task.ID, err)
		send("error", map[string]string{"error": "OCR 结果保存失败"})
		return
	}
	log.Printf("OCR 完成并保存: task=%s image=%s ocr_ms=%d", taskID, task.ID, result.Stats["ocr_ms"])
	send("ocr_complete", saved)
	if a.store.Settings().ImageTranslate {
		model := a.store.Settings().ImageModel
		if model == "" {
			log.Printf("OCR 后的自动翻译未执行: task=%s image=%s 未选择翻译模型", taskID, task.ID)
			send("error", map[string]string{"error": "OCR 已完成；请选择图像翻译模型"})
			return
		}
		a.runTranslation(ctx, taskID, saved, model, send)
	}
}
func (a *App) runTranslation(ctx context.Context, taskID string, task ImageTask, model string, send func(string, any)) {
	if task.OCR == "" {
		log.Printf("图像翻译失败: task=%s image=%s 没有完整 OCR 结果", taskID, task.ID)
		send("error", map[string]string{"error": "没有可翻译的完整 OCR 结果"})
		return
	}
	if !isTranslationModel(model) {
		log.Printf("图像翻译失败: task=%s image=%s 非法模型=%s", taskID, task.ID, model)
		send("error", map[string]string{"error": "图像翻译只能使用指定 Chat 翻译模型"})
		return
	}
	a.tasks.Stage(taskID, "加载翻译模型")
	send("status", map[string]string{"stage": "加载翻译模型"})
	if err := a.models.Ensure(ctx, "chat", model); err != nil {
		send("error", map[string]string{"error": err.Error()})
		return
	}
	a.tasks.Stage(taskID, "翻译 OCR 全文")
	send("status", map[string]string{"stage": "翻译 OCR 全文"})
	messages := []chatMessage{{Role: "user", Content: userPrompt(TabJa2zh, task.OCR)}}
	result, err := a.models.stream(ctx, "chat", messages, a.store.Settings().Generation, func(delta string) error { send("translation_delta", map[string]string{"text": delta}); return nil })
	if err != nil {
		send("error", map[string]string{"error": taskError(err)})
		return
	}
	saved, err := a.store.UpdateImage(task.ID, func(v *ImageTask) error { v.Translation = result.Text; v.TranslationStats = result.Stats; return nil })
	if err != nil {
		log.Printf("翻译结果保存失败: task=%s image=%s error=%v", taskID, task.ID, err)
		send("error", map[string]string{"error": "翻译结果保存失败"})
		return
	}
	log.Printf("图像翻译完成并保存: task=%s image=%s model=%s", taskID, task.ID, model)
	send("translation_complete", saved)
}
func (a *App) handleImageRetry(w http.ResponseWriter, r *http.Request, imageID string) {
	task, ok := a.store.Image(imageID)
	if !ok {
		fail(w, 404, "图像任务不存在")
		return
	}
	path := filepath.Join(a.store.dir, "images", task.ID+imageExtension(task.Mime))
	data, err := os.ReadFile(path)
	if err != nil {
		fail(w, 404, "原始图片不存在")
		return
	}
	taskID, ctx, err := a.tasks.Start(r.Context(), "ocr")
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	defer a.tasks.Finish(taskID)
	send, err := openSSE(w)
	if err != nil {
		return
	}
	a.runOCR(ctx, taskID, task, task.Mime, data, send)
}
func (a *App) handleImageTranslate(w http.ResponseWriter, r *http.Request, imageID string) {
	task, ok := a.store.Image(imageID)
	if !ok {
		fail(w, 404, "图像任务不存在")
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if decodeJSON(r, &body) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	taskID, ctx, err := a.tasks.Start(r.Context(), "image_translate")
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	defer a.tasks.Finish(taskID)
	send, err := openSSE(w)
	if err != nil {
		return
	}
	a.runTranslation(ctx, taskID, task, body.Model, send)
}
func (a *App) serveImage(w http.ResponseWriter, r *http.Request, imageID string) {
	task, ok := a.store.Image(imageID)
	if !ok {
		fail(w, 404, "图片不存在")
		return
	}
	path := filepath.Join(a.store.dir, "images", task.ID+imageExtension(task.Mime))
	w.Header().Set("Content-Type", task.Mime)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, path)
}
func taskError(err error) string {
	if errors.Is(err, context.Canceled) {
		return "任务已停止；未保存不完整结果"
	}
	return fmt.Sprintf("任务失败：%v；未保存不完整结果", err)
}
func validImageMime(s string) bool { return strings.HasPrefix(s, "image/") }
