package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}
type streamResult struct {
	Text  string
	Stats map[string]any
}
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage   map[string]any `json:"usage"`
	Timings map[string]any `json:"timings"`
}

func (m *ModelManager) postJSON(ctx context.Context, kind, path string, payload any, target any) error {
	data, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", m.base(kind)+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return fmt.Errorf("llama-server HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(target)
}
func (m *ModelManager) countTokens(ctx context.Context, messages []chatMessage) (int, error) {
	var applied struct {
		Prompt string `json:"prompt"`
	}
	if err := m.postJSON(ctx, "chat", "/apply-template", map[string]any{"messages": messages}, &applied); err != nil {
		return 0, fmt.Errorf("应用 Chat Template 失败: %w", err)
	}
	var tokenized struct {
		Tokens []any `json:"tokens"`
	}
	if err := m.postJSON(ctx, "chat", "/tokenize", map[string]any{"content": applied.Prompt, "with_special": true}, &tokenized); err != nil {
		return 0, fmt.Errorf("Token 检查失败: %w", err)
	}
	return len(tokenized.Tokens), nil
}
func generationPayload(s GenerationSettings) map[string]any {
	out := map[string]any{"max_tokens": s.MaxTokens}
	if s.Temperature != nil {
		out["temperature"] = *s.Temperature
	}
	if s.TopP != nil {
		out["top_p"] = *s.TopP
	}
	if s.TopK != nil {
		out["top_k"] = *s.TopK
	}
	if s.MinP != nil {
		out["min_p"] = *s.MinP
	}
	if s.RepeatPenalty != nil {
		out["repeat_penalty"] = *s.RepeatPenalty
	}
	return out
}
func (m *ModelManager) stream(ctx context.Context, kind string, messages []chatMessage, settings GenerationSettings, onDelta func(string) error) (streamResult, error) {
	payload := generationPayload(settings)
	payload["messages"] = messages
	payload["stream"] = true
	payload["stream_options"] = map[string]any{"include_usage": true}
	payload["timings_per_token"] = true
	resp, err := m.completionRequest(ctx, kind, payload)
	if err != nil {
		return streamResult{}, err
	}
	defer resp.Body.Close()
	started := time.Now()
	var text strings.Builder
	var usage, timings map[string]any
	var firstMS int64
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var data strings.Builder
	sawDone := false
	process := func() error {
		if data.Len() == 0 {
			return nil
		}
		raw := data.String()
		data.Reset()
		if raw == "[DONE]" {
			sawDone = true
			return io.EOF
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			return fmt.Errorf("解析模型流: %w", err)
		}
		if len(chunk.Usage) > 0 {
			usage = chunk.Usage
		}
		if len(chunk.Timings) > 0 {
			timings = chunk.Timings
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				if firstMS == 0 {
					firstMS = time.Since(started).Milliseconds()
				}
				text.WriteString(choice.Delta.Content)
				if onDelta != nil {
					if err := onDelta(choice.Delta.Content); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := process(); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return streamResult{}, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return streamResult{}, err
	}
	if ctx.Err() != nil {
		return streamResult{}, ctx.Err()
	}
	if err := process(); err != nil && !errors.Is(err, io.EOF) {
		return streamResult{}, err
	}
	if !sawDone {
		return streamResult{}, io.ErrUnexpectedEOF
	}
	stats := map[string]any{"ttft_ms": firstMS, "elapsed_ms": time.Since(started).Milliseconds()}
	for k, v := range usage {
		stats[k] = v
	}
	for k, v := range timings {
		stats[k] = v
	}
	promptN := statNumber(stats, "prompt_n", stats["prompt_tokens"])
	cacheN := statNumber(stats, "cache_n", float64(0))
	predictedN := statNumber(stats, "predicted_n", stats["completion_tokens"])
	stats["prompt_n"] = promptN
	stats["cache_n"] = cacheN
	stats["predicted_n"] = predictedN
	stats["total_context"] = promptN + cacheN + predictedN
	for _, status := range m.Statuses() {
		if status.Kind == kind {
			stats["load_ms"] = status.LoadMS
		}
	}
	return streamResult{Text: text.String(), Stats: stats}, nil
}
func statNumber(stats map[string]any, key string, fallback any) float64 {
	if v, ok := stats[key]; ok {
		return numberValue(v)
	}
	return numberValue(fallback)
}
func openSSE(w http.ResponseWriter) (func(string, any), error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("服务器不支持流式响应")
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	return func(event string, value any) {
		data, _ := json.Marshal(value)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}, nil
}
