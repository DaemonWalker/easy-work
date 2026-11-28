package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

func (m *AudioManager) StartTranscription(meetingID, whisperModel string) (AudioSessionState, error) {
	meeting, ok := m.store.AudioMeeting(meetingID)
	if !ok {
		return AudioSessionState{}, errors.New("音频会议不存在")
	}
	if meeting.Recording == nil || meeting.Recording.Status != "ready" {
		return AudioSessionState{}, errors.New("该会议没有可用的完整录音")
	}
	if whisperModel == "" {
		whisperModel = m.store.Settings().AudioTranscriptionModel
	}
	if whisperModel == "" {
		whisperModel = meeting.WhisperModel
	}
	available := false
	for _, model := range m.whisper.Models() {
		available = available || model.ID == whisperModel
	}
	if !available {
		return AudioSessionState{}, errors.New("请选择可用的会后转录 Whisper 模型")
	}
	if _, err := os.Stat(audioRecordingPath(m.store.dir, meetingID)); err != nil {
		return AudioSessionState{}, errors.New("完整录音文件不可用")
	}
	m.mu.RLock()
	session := m.sessions[meetingID]
	closed := m.closed
	m.mu.RUnlock()
	if closed {
		return AudioSessionState{}, errors.New("音频服务正在退出")
	}
	if session != nil {
		state := session.snapshot()
		if state.Recording || state.Processing {
			return AudioSessionState{}, errors.New("会议仍在录音或处理中")
		}
	}
	taskID, ctx, err := m.tasks.Start(context.Background(), "audio_transcription")
	if err != nil {
		return AudioSessionState{}, err
	}
	if _, err = m.store.UpdateSettings(func(settings *Settings) error {
		settings.AudioTranscriptionModel = whisperModel
		return nil
	}); err != nil {
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("会后转录模型设置保存失败")
	}
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("音频服务正在退出")
	}
	if session == nil {
		session = &AudioSession{manager: m, meeting: meeting, status: "saved", notify: make(chan struct{}, 1), subscribers: map[chan audioEvent]struct{}{}}
		m.sessions[meetingID] = session
	}
	m.mu.Unlock()
	session.mu.Lock()
	if session.recording || session.processing {
		session.mu.Unlock()
		cancel()
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("会议状态已变化，请重试")
	}
	n := now()
	transcription := AudioTranscription{ID: id(), WhisperModel: whisperModel, Status: "recognizing", Segments: []AudioSegment{}, CreatedAt: n, UpdatedAt: n}
	saved, err := m.store.UpdateAudioMeeting(meetingID, func(saved *AudioMeeting) error {
		saved.Transcriptions = append(saved.Transcriptions, transcription)
		return nil
	})
	if err != nil {
		session.mu.Unlock()
		cancel()
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("高精度转录版本保存失败: " + err.Error())
	}
	session.meeting = saved
	session.taskID = taskID
	session.ctx = ctx
	session.cancel = cancel
	session.processing = true
	session.status = "transcribing"
	session.lastError = ""
	session.runDone = make(chan struct{})
	state := session.snapshotLocked()
	session.mu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		session.mu.Lock()
		session.processing = false
		session.status = "transcription_stopped"
		session.mu.Unlock()
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("音频服务正在退出")
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go m.runTranscription(session, transcription.ID)
	log.Printf("会后高精度转录开始: session=%s transcription=%s whisper=%s", meetingID, transcription.ID, whisperModel)
	return state, nil
}

func (m *AudioManager) runTranscription(session *AudioSession, transcriptionID string) {
	defer m.wg.Done()
	defer close(session.runDone)
	defer m.tasks.Finish(session.taskID)
	session.mu.RLock()
	ctx := session.ctx
	model := ""
	for _, transcription := range session.meeting.Transcriptions {
		if transcription.ID == transcriptionID {
			model = transcription.WhisperModel
			break
		}
	}
	session.mu.RUnlock()
	workDir := filepath.Join(m.store.dir, "audio", session.meeting.ID, "transcription-"+transcriptionID)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		m.finishTranscription(session, transcriptionID, "failed", err)
		return
	}
	defer os.RemoveAll(workDir)
	count := 0
	err := forEachRecordedSegment(ctx, audioRecordingPath(m.store.dir, session.meeting.ID), func(segment capturedAudio) error {
		count++
		m.tasks.Stage(session.taskID, fmt.Sprintf("高精度识别第 %d 段", count))
		return m.transcribeSegment(ctx, session, transcriptionID, model, workDir, segment)
	})
	if err != nil {
		if ctx.Err() != nil {
			m.finishTranscription(session, transcriptionID, "stopped", nil)
		} else {
			m.finishTranscription(session, transcriptionID, "failed", err)
		}
		return
	}
	m.finishTranscription(session, transcriptionID, "complete", nil)
}

func (m *AudioManager) transcribeSegment(ctx context.Context, session *AudioSession, transcriptionID, model string, workDir string, captured capturedAudio) error {
	if err := m.whisper.Ensure(ctx, model); err != nil {
		return err
	}
	path := filepath.Join(workDir, id()+".wav")
	if err := writeWAV(path, captured.Samples); err != nil {
		return fmt.Errorf("保存高精度转录临时 WAV 失败: %w", err)
	}
	text, stats, err := m.whisper.Infer(ctx, path)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	segment := AudioSegment{ID: id(), StartMS: captured.StartMS, EndMS: captured.EndMS, Source: text, Status: audioPending, RecognitionStats: stats, RecognizedAt: now()}
	session.mu.Lock()
	saved, err := m.store.UpdateAudioMeeting(session.meeting.ID, func(meeting *AudioMeeting) error {
		for i := range meeting.Transcriptions {
			if meeting.Transcriptions[i].ID == transcriptionID {
				meeting.Transcriptions[i].Segments = append(meeting.Transcriptions[i].Segments, segment)
				meeting.Transcriptions[i].UpdatedAt = now()
				return nil
			}
		}
		return errors.New("高精度转录版本不存在")
	})
	if err != nil {
		session.mu.Unlock()
		return err
	}
	session.meeting = saved
	session.emitLocked("transcription_recognized", map[string]any{"transcription_id": transcriptionID, "segment": segment})
	state := session.snapshotLocked()
	session.emitLocked("state", state)
	session.mu.Unlock()
	return nil
}

func (m *AudioManager) StartTranslation(meetingID, transcriptionID, chatModel string) (AudioSessionState, error) {
	meeting, ok := m.store.AudioMeeting(meetingID)
	if !ok {
		return AudioSessionState{}, errors.New("音频会议不存在")
	}
	pending := false
	found := false
	for _, transcription := range meeting.Transcriptions {
		if transcription.ID != transcriptionID {
			continue
		}
		found = true
		if transcription.Status == "recognizing" || transcription.Status == "translating" {
			return AudioSessionState{}, errors.New("高精度转录或翻译仍在处理中")
		}
		if !transcription.Translate && transcription.Status != "complete" {
			return AudioSessionState{}, errors.New("高精度转录尚未完整完成，不能开始翻译")
		}
		for _, segment := range transcription.Segments {
			pending = pending || segment.Status != audioComplete
		}
	}
	if !found {
		return AudioSessionState{}, errors.New("高精度转录版本不存在")
	}
	if !pending {
		return AudioSessionState{}, errors.New("该版本没有待翻译的语音段")
	}
	if chatModel == "" {
		chatModel = m.store.Settings().AudioTranscriptionChatModel
	}
	if chatModel == "" {
		chatModel = meeting.ChatModel
	}
	if _, ok = m.models.models[chatModel]; !ok || !isAudioTranslationModel(chatModel) {
		return AudioSessionState{}, errors.New("请选择可用的日译中 Chat 模型")
	}
	m.mu.RLock()
	session := m.sessions[meetingID]
	closed := m.closed
	m.mu.RUnlock()
	if closed {
		return AudioSessionState{}, errors.New("音频服务正在退出")
	}
	if session != nil {
		state := session.snapshot()
		if state.Recording || state.Processing {
			return AudioSessionState{}, errors.New("会议仍在录音或处理中")
		}
	}
	taskID, ctx, err := m.tasks.Start(context.Background(), "audio_transcription_translation")
	if err != nil {
		return AudioSessionState{}, err
	}
	if _, err = m.store.UpdateSettings(func(settings *Settings) error {
		settings.AudioTranscriptionChatModel = chatModel
		return nil
	}); err != nil {
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("会后翻译模型设置保存失败")
	}
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("音频服务正在退出")
	}
	if session == nil {
		session = &AudioSession{manager: m, meeting: meeting, status: "saved", notify: make(chan struct{}, 1), subscribers: map[chan audioEvent]struct{}{}}
		m.sessions[meetingID] = session
	}
	m.mu.Unlock()
	session.mu.Lock()
	if session.recording || session.processing {
		session.mu.Unlock()
		cancel()
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("会议状态已变化，请重试")
	}
	saved, err := m.store.UpdateAudioMeeting(meetingID, func(saved *AudioMeeting) error {
		for i := range saved.Transcriptions {
			if saved.Transcriptions[i].ID == transcriptionID {
				saved.Transcriptions[i].ChatModel = chatModel
				saved.Transcriptions[i].Translate = true
				saved.Transcriptions[i].Status = "translating"
				saved.Transcriptions[i].EndedAt = nil
				saved.Transcriptions[i].UpdatedAt = now()
				return nil
			}
		}
		return errors.New("高精度转录版本不存在")
	})
	if err != nil {
		session.mu.Unlock()
		cancel()
		m.tasks.Finish(taskID)
		return AudioSessionState{}, err
	}
	session.meeting = saved
	session.taskID = taskID
	session.ctx = ctx
	session.cancel = cancel
	session.processing = true
	session.status = "transcribing"
	session.lastError = ""
	session.runDone = make(chan struct{})
	state := session.snapshotLocked()
	session.mu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		session.mu.Lock()
		session.processing = false
		session.status = "transcription_stopped"
		session.mu.Unlock()
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("音频服务正在退出")
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go m.runTranscriptionTranslation(session, transcriptionID)
	log.Printf("高精度结果翻译开始: session=%s transcription=%s chat=%s", meetingID, transcriptionID, chatModel)
	return state, nil
}

func (m *AudioManager) runTranscriptionTranslation(session *AudioSession, transcriptionID string) {
	defer m.wg.Done()
	defer close(session.runDone)
	defer m.tasks.Finish(session.taskID)
	session.mu.RLock()
	ctx := session.ctx
	session.mu.RUnlock()
	err := m.translateTranscription(ctx, session, transcriptionID)
	if err != nil {
		if ctx.Err() != nil {
			m.finishTranscription(session, transcriptionID, "stopped", nil)
		} else {
			m.finishTranscription(session, transcriptionID, "failed", err)
		}
		return
	}
	m.finishTranscription(session, transcriptionID, "complete", nil)
}

func (m *AudioManager) translateTranscription(ctx context.Context, session *AudioSession, transcriptionID string) error {
	session.mu.RLock()
	var segments []AudioSegment
	model := ""
	for _, transcription := range session.meeting.Transcriptions {
		if transcription.ID == transcriptionID {
			segments = clone(transcription.Segments)
			model = transcription.ChatModel
			break
		}
	}
	session.mu.RUnlock()
	for i, segment := range segments {
		if segment.Status == audioComplete {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		m.tasks.Stage(session.taskID, fmt.Sprintf("高精度译文补全 %d/%d", i+1, len(segments)))
		if err := m.models.Ensure(ctx, "chat", model); err != nil {
			return err
		}
		session.emit("transcription_translation_start", map[string]string{"transcription_id": transcriptionID, "segment_id": segment.ID})
		messages := []chatMessage{{Role: "user", Content: userPrompt(TabJa2zh, model, segment.Source)}}
		result, err := m.models.stream(ctx, "chat", model, messages, generationFor(model, m.store.Settings().Generation), func(delta string) error {
			session.emit("transcription_delta", map[string]string{"transcription_id": transcriptionID, "segment_id": segment.ID, "text": delta})
			return ctx.Err()
		})
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		n := now()
		session.mu.Lock()
		saved, err := m.store.UpdateAudioMeeting(session.meeting.ID, func(meeting *AudioMeeting) error {
			for i := range meeting.Transcriptions {
				if meeting.Transcriptions[i].ID != transcriptionID {
					continue
				}
				for j := range meeting.Transcriptions[i].Segments {
					if meeting.Transcriptions[i].Segments[j].ID == segment.ID {
						meeting.Transcriptions[i].Segments[j].Translation = result.Text
						meeting.Transcriptions[i].Segments[j].TranslationStats = result.Stats
						meeting.Transcriptions[i].Segments[j].Status = audioComplete
						meeting.Transcriptions[i].Segments[j].TranslatedAt = &n
						meeting.Transcriptions[i].UpdatedAt = n
						segment = clone(meeting.Transcriptions[i].Segments[j])
						return nil
					}
				}
			}
			return errors.New("待翻译的高精度语音段不存在")
		})
		if err != nil {
			session.mu.Unlock()
			return err
		}
		session.meeting = saved
		session.emitLocked("transcription_translated", map[string]any{"transcription_id": transcriptionID, "segment": segment})
		state := session.snapshotLocked()
		session.emitLocked("state", state)
		session.mu.Unlock()
	}
	return nil
}

func (m *AudioManager) finishTranscription(session *AudioSession, transcriptionID, status string, runErr error) {
	session.mu.Lock()
	n := now()
	saved, err := m.store.UpdateAudioMeeting(session.meeting.ID, func(meeting *AudioMeeting) error {
		for i := range meeting.Transcriptions {
			if meeting.Transcriptions[i].ID == transcriptionID {
				meeting.Transcriptions[i].Status = status
				meeting.Transcriptions[i].UpdatedAt = n
				meeting.Transcriptions[i].EndedAt = &n
				return nil
			}
		}
		return errors.New("高精度转录版本不存在")
	})
	if err == nil {
		session.meeting = saved
	}
	session.processing = false
	session.status = "transcription_" + status
	session.lastError = ""
	if runErr != nil {
		session.lastError = "高精度转录失败: " + runErr.Error()
	}
	if err != nil {
		session.status = "transcription_failed"
		session.lastError = "高精度转录状态保存失败: " + err.Error()
	}
	state := session.snapshotLocked()
	if session.lastError != "" {
		session.emitLocked("error", map[string]string{"error": session.lastError})
	}
	session.emitLocked("complete", state)
	session.mu.Unlock()
	log.Printf("会后高精度转录结束: session=%s transcription=%s status=%s error=%v", session.meeting.ID, transcriptionID, status, runErr)
}

func (m *AudioManager) DeleteRecording(id string) (AudioSessionState, error) {
	m.mu.RLock()
	session := m.sessions[id]
	m.mu.RUnlock()
	if session != nil {
		state := session.snapshot()
		if state.Recording || state.Processing {
			return AudioSessionState{}, errors.New("录音或会后转录仍在处理中，不能删除录音")
		}
	}
	if _, ok := m.store.AudioMeeting(id); !ok {
		return AudioSessionState{}, errors.New("音频会议不存在")
	}
	path := audioRecordingPath(m.store.dir, id)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return AudioSessionState{}, err
	}
	if err := os.Remove(path + ".tmp"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return AudioSessionState{}, err
	}
	if session != nil {
		session.mu.Lock()
		defer session.mu.Unlock()
	}
	saved, err := m.store.UpdateAudioMeeting(id, func(meeting *AudioMeeting) error {
		meeting.Recording = nil
		return nil
	})
	if err != nil {
		return AudioSessionState{}, err
	}
	if session != nil {
		session.meeting = saved
		return session.snapshotLocked(), nil
	}
	return AudioSessionState{Meeting: saved, Status: "saved"}, nil
}
