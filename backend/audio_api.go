package main

import (
	"errors"
	"net/http"
)

func (a *App) handleAudioModels(w http.ResponseWriter, r *http.Request) {
	if a.audio == nil {
		fail(w, http.StatusServiceUnavailable, "音频服务未初始化")
		return
	}
	chat := []ModelInfo{}
	for _, model := range a.modelList {
		if model.Kind == "chat" && isAudioTranslationModel(model.ID) {
			chat = append(chat, model)
		}
	}
	writeJSON(w, 200, map[string]any{"whisper": a.audio.whisper.Models(), "chat": chat, "sources": []string{"system", "microphone", "both"}})
}
func (a *App) handleAudioMeetings(w http.ResponseWriter, r *http.Request) {
	if a.audio == nil {
		fail(w, http.StatusServiceUnavailable, "音频服务未初始化")
		return
	}
	writeJSON(w, 200, a.audio.Meetings())
}
func (a *App) handleAudioStart(w http.ResponseWriter, r *http.Request) {
	if a.audio == nil {
		fail(w, http.StatusServiceUnavailable, "音频服务未初始化")
		return
	}
	var body struct {
		Source       string `json:"source"`
		WhisperModel string `json:"whisper_model"`
		ChatModel    string `json:"chat_model"`
	}
	if decodeJSON(r, &body) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	state, err := a.audio.Start(body.Source, body.WhisperModel, body.ChatModel)
	if err != nil {
		status := 400
		if errors.Is(err, errBusy) {
			status = 409
		}
		fail(w, status, err.Error())
		return
	}
	writeJSON(w, 201, state)
}
func (a *App) handleAudioSession(w http.ResponseWriter, r *http.Request) {
	if a.audio == nil {
		fail(w, http.StatusServiceUnavailable, "音频服务未初始化")
		return
	}
	state, ok := a.audio.Session(r.PathValue("id"))
	if !ok {
		fail(w, 404, "音频会议不存在")
		return
	}
	writeJSON(w, 200, state)
}
func (a *App) handleAudioEvents(w http.ResponseWriter, r *http.Request) {
	if a.audio == nil {
		fail(w, http.StatusServiceUnavailable, "音频服务未初始化")
		return
	}
	events, cancel, state, ok := a.audio.Subscribe(r.PathValue("id"))
	if !ok {
		fail(w, 404, "音频会议不存在")
		return
	}
	defer cancel()
	send, err := openSSE(w)
	if err != nil {
		return
	}
	send("snapshot", state)
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			send(event.name, event.value)
		}
	}
}
func (a *App) handleAudioStopRecording(w http.ResponseWriter, r *http.Request) {
	if a.audio == nil {
		fail(w, http.StatusServiceUnavailable, "音频服务未初始化")
		return
	}
	state, ok := a.audio.StopRecording(r.PathValue("id"))
	if !ok {
		fail(w, 404, "音频会议不存在或已退出当前进程")
		return
	}
	writeJSON(w, 200, state)
}
func (a *App) handleAudioStop(w http.ResponseWriter, r *http.Request) {
	if a.audio == nil {
		fail(w, http.StatusServiceUnavailable, "音频服务未初始化")
		return
	}
	state, ok := a.audio.Stop(r.PathValue("id"))
	if !ok {
		fail(w, 404, "音频会议不存在或已退出当前进程")
		return
	}
	writeJSON(w, 200, state)
}
func (a *App) handleAudioResume(w http.ResponseWriter, r *http.Request) {
	if a.audio == nil {
		fail(w, http.StatusServiceUnavailable, "音频服务未初始化")
		return
	}
	state, err := a.audio.Resume(r.PathValue("id"))
	if err != nil {
		status := 400
		if errors.Is(err, errBusy) {
			status = 409
		}
		fail(w, status, err.Error())
		return
	}
	writeJSON(w, 200, state)
}
