package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	audioQueued         = "queued_recognition"
	audioRecognizing    = "recognizing"
	audioPending        = "pending_translation"
	audioTranslating    = "translating"
	audioComplete       = "complete"
	audioASRError       = "recognition_error"
	audioTranslateError = "translation_error"
)

type audioEvent struct {
	name  string
	value any
}

type audioWork struct {
	id           string
	startMS      int64
	endMS        int64
	path         string
	status       string
	source       string
	recognizedAt time.Time
}

type AudioSession struct {
	mu          sync.RWMutex
	manager     *AudioManager
	meeting     AudioMeeting
	capture     audioCapture
	recorder    *audioRecorder
	ctx         context.Context
	cancel      context.CancelFunc
	taskID      string
	recording   bool
	processing  bool
	status      string
	lastError   string
	queue       []*audioWork
	order       []*audioWork
	notify      chan struct{}
	subscribers map[chan audioEvent]struct{}
	runDone     chan struct{}
	captureDone chan struct{}
}

type AudioManager struct {
	mu         sync.RWMutex
	store      *Store
	models     *ModelManager
	tasks      *TaskManager
	whisper    whisperService
	newCapture captureFactory
	sessions   map[string]*AudioSession
	wg         sync.WaitGroup
	closed     bool
}

func NewAudioManager(store *Store, models *ModelManager, tasks *TaskManager, whisper whisperService, factory captureFactory) *AudioManager {
	manager := &AudioManager{store: store, models: models, tasks: tasks, whisper: whisper, newCapture: factory, sessions: map[string]*AudioSession{}}
	manager.recoverAfterRestart()
	return manager
}

func validAudioSource(source string) bool {
	return source == "system" || source == "microphone" || source == "both"
}

func (m *AudioManager) Start(source, whisperModel, chatModel string) (AudioSessionState, error) {
	settings := m.store.Settings()
	if source == "" {
		source = settings.AudioSource
	}
	if source == "" {
		source = "system"
	}
	if whisperModel == "" {
		whisperModel = settings.WhisperModel
	}
	if chatModel == "" {
		chatModel = settings.AudioChatModel
	}
	if !validAudioSource(source) {
		return AudioSessionState{}, errors.New("音源必须是 system、microphone 或 both")
	}
	available := false
	for _, model := range m.whisper.Models() {
		available = available || model.ID == whisperModel
	}
	if !available {
		return AudioSessionState{}, errors.New("请选择可用的 Whisper 模型")
	}
	if _, ok := m.models.models[chatModel]; !ok || !isAudioTranslationModel(chatModel) {
		return AudioSessionState{}, errors.New("语音翻译只能选择可用的日译中 Chat 模型")
	}
	m.mu.RLock()
	closed := m.closed
	m.mu.RUnlock()
	if closed {
		return AudioSessionState{}, errors.New("音频服务正在退出")
	}
	capture, err := m.newCapture(source)
	if err != nil {
		return AudioSessionState{}, err
	}
	taskID, ctx, err := m.tasks.Start(context.Background(), "audio")
	if err != nil {
		return AudioSessionState{}, err
	}
	if _, err = m.store.UpdateSettings(func(s *Settings) error {
		s.AudioSource = source
		s.WhisperModel = whisperModel
		s.AudioChatModel = chatModel
		return nil
	}); err != nil {
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("音频设置保存失败")
	}
	n := now()
	meeting := AudioMeeting{ID: id(), Source: source, WhisperModel: whisperModel, ChatModel: chatModel, Segments: []AudioSegment{}, Recording: &AudioRecording{Status: "recording"}, CreatedAt: n, UpdatedAt: n}
	recorder, err := newAudioRecorder(audioRecordingPath(m.store.dir, meeting.ID))
	if err != nil {
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("完整录音创建失败: " + err.Error())
	}
	if err = m.store.AddAudioMeeting(meeting); err != nil {
		_ = recorder.Close()
		_ = os.RemoveAll(filepath.Dir(audioRecordingPath(m.store.dir, meeting.ID)))
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("音频会议保存失败")
	}
	ctx, cancel := context.WithCancel(ctx)
	session := &AudioSession{manager: m, meeting: meeting, capture: capture, recorder: recorder, ctx: ctx, cancel: cancel, taskID: taskID, recording: true, processing: true, status: "recording", notify: make(chan struct{}, 1), subscribers: map[chan audioEvent]struct{}{}, runDone: make(chan struct{}), captureDone: make(chan struct{})}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("音频服务正在退出")
	}
	m.sessions[meeting.ID] = session
	m.wg.Add(1)
	m.mu.Unlock()
	go m.run(session, true)
	log.Printf("连续音频会议开始: session=%s source=%s whisper=%s chat=%s", meeting.ID, source, whisperModel, chatModel)
	return session.snapshot(), nil
}

func (m *AudioManager) run(session *AudioSession, capture bool) {
	defer m.wg.Done()
	defer close(session.runDone)
	defer m.tasks.Finish(session.taskID)
	if capture {
		go session.runCapture()
	}
	for {
		if session.ctx.Err() != nil {
			session.stopProcessing(capture)
			return
		}
		work, kind := session.nextWork()
		if work != nil {
			if kind == "recognition" {
				session.recognize(work)
			} else {
				session.translate(work)
			}
			continue
		}
		if session.drained() {
			session.finishDrained()
			return
		}
		select {
		case <-session.ctx.Done():
			session.stopProcessing(capture)
			return
		case <-session.notify:
		}
	}
}

func (s *AudioSession) runCapture() {
	defer close(s.captureDone)
	segmenter := &audioSegmenter{}
	err := s.capture.Run(func(samples []int16) error {
		if err := s.recorder.Write(samples); err != nil {
			return err
		}
		return segmenter.Push(samples, s.enqueue)
	})
	if err == nil && s.ctx.Err() == nil {
		err = segmenter.Flush(s.enqueue)
	}
	_ = s.capture.Close()
	recordingErr := s.recorder.Close()
	recording := AudioRecording{Status: "error"}
	if recordingErr == nil {
		if savedRecording, infoErr := audioRecordingInfo(audioRecordingPath(s.manager.store.dir, s.meeting.ID)); infoErr == nil {
			recording = savedRecording
		} else {
			recordingErr = infoErr
		}
	}
	s.mu.Lock()
	s.recording = false
	if err != nil && s.ctx.Err() == nil {
		s.lastError = err.Error()
	}
	if recordingErr != nil {
		s.lastError = "完整录音保存失败: " + recordingErr.Error()
	}
	n := now()
	s.meeting.EndedAt = &n
	saved, updateErr := s.manager.store.UpdateAudioMeeting(s.meeting.ID, func(meeting *AudioMeeting) error {
		meeting.EndedAt = &n
		meeting.Recording = &recording
		return nil
	})
	if updateErr == nil {
		s.meeting = saved
	}
	s.mu.Unlock()
	if updateErr != nil {
		s.reportError("", "录音结束状态保存失败: "+updateErr.Error())
	}
	if err != nil && s.ctx.Err() == nil {
		s.reportError("", "音频采集失败: "+err.Error())
	}
	if recordingErr != nil {
		s.reportError("", "完整录音保存失败: "+recordingErr.Error())
	}
	s.signal()
	s.publishState()
}

func (s *AudioSession) enqueue(segment capturedAudio) error {
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	segmentID := id()
	path := filepath.Join(s.manager.store.dir, "audio", s.meeting.ID, segmentID+".wav")
	if err := writeWAV(path, segment.Samples); err != nil {
		return fmt.Errorf("保存临时 WAV 失败: %w", err)
	}
	work := &audioWork{id: segmentID, startMS: segment.StartMS, endMS: segment.EndMS, path: path, status: audioQueued}
	s.mu.Lock()
	if !s.recording || s.ctx.Err() != nil {
		s.mu.Unlock()
		_ = os.Remove(path)
		return errors.New("录音已停止")
	}
	s.queue = append(s.queue, work)
	s.order = append(s.order, work)
	s.mu.Unlock()
	s.signal()
	s.publishState()
	return nil
}

func (s *AudioSession) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *AudioSession) nextWork() (*audioWork, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) > 0 {
		work := s.queue[0]
		s.queue = s.queue[1:]
		work.status = audioRecognizing
		s.manager.tasks.Stage(s.taskID, "识别连续语音段")
		s.emitLocked("recognition_start", map[string]any{"session_id": s.meeting.ID, "segment_id": work.id, "start_ms": work.startMS, "end_ms": work.endMS})
		return work, "recognition"
	}
	for _, work := range s.order {
		if work.status == audioPending {
			work.status = audioTranslating
			s.manager.tasks.Stage(s.taskID, "按顺序补全中文翻译")
			s.emitLocked("translation_start", map[string]string{"session_id": s.meeting.ID, "segment_id": work.id})
			return work, "translation"
		}
	}
	return nil, ""
}

func (s *AudioSession) recognize(work *audioWork) {
	s.mu.RLock()
	ctx := s.ctx
	model := s.meeting.WhisperModel
	s.mu.RUnlock()
	if err := s.manager.whisper.Ensure(ctx, model); err != nil {
		s.recognitionFailed(work, err)
		return
	}
	text, stats, err := s.manager.whisper.Infer(ctx, work.path)
	if err != nil {
		s.recognitionFailed(work, err)
		return
	}
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	work.source = text
	work.status = audioPending
	work.recognizedAt = now()
	segment := AudioSegment{ID: work.id, StartMS: work.startMS, EndMS: work.endMS, Source: text, Status: audioPending, RecognitionStats: stats, RecognizedAt: work.recognizedAt}
	saved, err := s.manager.store.UpdateAudioMeeting(s.meeting.ID, func(meeting *AudioMeeting) error {
		meeting.Segments = append(meeting.Segments, segment)
		return nil
	})
	if err != nil {
		work.source = ""
		work.status = audioASRError
		s.lastError = err.Error()
		s.emitLocked("error", map[string]string{"segment_id": work.id, "error": "完整原文保存失败: " + err.Error()})
		s.mu.Unlock()
		return
	}
	s.meeting = saved
	s.emitLocked("recognized", segment)
	s.mu.Unlock()
	s.publishState()
}

func (s *AudioSession) recognitionFailed(work *audioWork, err error) {
	s.mu.Lock()
	work.status = audioASRError
	if s.ctx.Err() == nil {
		s.lastError = err.Error()
		s.emitLocked("error", map[string]string{"segment_id": work.id, "error": "语音识别失败，原文尚未生成，可在会后补全: " + err.Error()})
	}
	s.mu.Unlock()
	if s.ctx.Err() == nil {
		log.Printf("语音段识别失败: session=%s segment=%s error=%v", s.meeting.ID, work.id, err)
	}
	s.signal()
}

func (s *AudioSession) translate(work *audioWork) {
	s.mu.RLock()
	ctx := s.ctx
	model := s.meeting.ChatModel
	source := work.source
	s.mu.RUnlock()
	if err := s.manager.models.Ensure(ctx, "chat", model); err != nil {
		s.translationFailed(work, err)
		return
	}
	messages := []chatMessage{{Role: "user", Content: userPrompt(TabJa2zh, model, source)}}
	result, err := s.manager.models.stream(ctx, "chat", model, messages, generationFor(model, s.manager.store.Settings().Generation), func(delta string) error {
		s.emit("translation_delta", map[string]string{"session_id": s.meeting.ID, "segment_id": work.id, "text": delta})
		return ctx.Err()
	})
	if err != nil {
		s.translationFailed(work, err)
		return
	}
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	n := now()
	segment := AudioSegment{}
	saved, err := s.manager.store.UpdateAudioMeeting(s.meeting.ID, func(meeting *AudioMeeting) error {
		for i := range meeting.Segments {
			if meeting.Segments[i].ID == work.id {
				meeting.Segments[i].Translation = result.Text
				meeting.Segments[i].TranslationStats = result.Stats
				meeting.Segments[i].Status = audioComplete
				meeting.Segments[i].TranslatedAt = &n
				segment = clone(meeting.Segments[i])
				return nil
			}
		}
		return errors.New("待翻译语音段不存在")
	})
	if err != nil {
		work.status = audioTranslateError
		s.lastError = err.Error()
		s.emitLocked("error", map[string]string{"segment_id": work.id, "error": "完整译文保存失败: " + err.Error()})
		s.mu.Unlock()
		return
	}
	work.status = audioComplete
	s.meeting = saved
	s.emitLocked("translated", segment)
	s.mu.Unlock()
	if err = os.Remove(work.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("临时 WAV 清理失败: path=%s error=%v", work.path, err)
	}
	s.publishState()
}

func (s *AudioSession) translationFailed(work *audioWork, err error) {
	s.mu.Lock()
	work.status = audioTranslateError
	if s.ctx.Err() == nil {
		s.lastError = err.Error()
		s.emitLocked("error", map[string]string{"segment_id": work.id, "error": "翻译失败，完整原文已保留，可继续补全: " + err.Error()})
	}
	s.mu.Unlock()
	if s.ctx.Err() == nil {
		log.Printf("语音段翻译失败: session=%s segment=%s error=%v", s.meeting.ID, work.id, err)
	}
	s.signal()
}

func (s *AudioSession) drained() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.recording || len(s.queue) > 0 {
		return false
	}
	for _, work := range s.order {
		if work.status == audioPending {
			return false
		}
	}
	return true
}

func (s *AudioSession) finishDrained() {
	s.mu.Lock()
	s.processing = false
	pending := false
	for _, work := range s.order {
		if work.status != audioComplete {
			pending = true
			break
		}
	}
	if pending {
		s.status = "awaiting_completion"
	} else {
		s.status = "complete"
	}
	state := s.snapshotLocked()
	s.emitLocked("complete", state)
	s.mu.Unlock()
	log.Printf("连续音频处理暂停或完成: session=%s status=%s", state.Meeting.ID, state.Status)
}

func (s *AudioSession) stopProcessing(hadCapture bool) {
	if hadCapture {
		_ = s.capture.Close()
		<-s.captureDone
	}
	s.mu.Lock()
	s.recording = false
	s.processing = false
	s.status = "stopped"
	n := now()
	s.meeting.EndedAt = &n
	state := s.snapshotLocked()
	s.emitLocked("complete", state)
	s.mu.Unlock()
	_, _ = s.manager.store.UpdateAudioMeeting(s.meeting.ID, func(meeting *AudioMeeting) error { meeting.EndedAt = &n; return nil })
	log.Printf("连续音频会议已停止，完整结果已保留: session=%s", s.meeting.ID)
}

func (s *AudioSession) StopRecording() AudioSessionState {
	s.mu.RLock()
	capture := s.capture
	s.mu.RUnlock()
	if capture != nil {
		_ = capture.Close()
	}
	return s.snapshot()
}

func (s *AudioSession) Stop() AudioSessionState {
	s.mu.Lock()
	s.cancel()
	capture := s.capture
	processing := s.processing
	if !processing {
		s.status = "stopped"
		s.recording = false
	}
	s.mu.Unlock()
	if capture != nil {
		_ = capture.Close()
	}
	s.signal()
	return s.snapshot()
}

func (m *AudioManager) Resume(id string) (AudioSessionState, error) {
	m.mu.RLock()
	session := m.sessions[id]
	closed := m.closed
	m.mu.RUnlock()
	if session == nil {
		return AudioSessionState{}, errors.New("只有当前进程中的未完成会议可以继续补全")
	}
	if closed {
		return AudioSessionState{}, errors.New("音频服务正在退出")
	}
	session.mu.RLock()
	busy := session.recording || session.processing
	pending := false
	for _, work := range session.order {
		pending = pending || work.status != audioComplete
	}
	session.mu.RUnlock()
	if busy {
		return AudioSessionState{}, errors.New("会议仍在录音或处理中")
	}
	if !pending {
		return AudioSessionState{}, errors.New("会议没有待补全的语音段")
	}
	taskID, ctx, err := m.tasks.Start(context.Background(), "audio_completion")
	if err != nil {
		return AudioSessionState{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	session.mu.Lock()
	if session.recording || session.processing {
		session.mu.Unlock()
		cancel()
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("会议状态已变化，请重试")
	}
	session.taskID = taskID
	session.ctx = ctx
	session.cancel = cancel
	session.processing = true
	session.status = "processing"
	session.lastError = ""
	session.runDone = make(chan struct{})
	session.queue = nil
	for _, work := range session.order {
		switch work.status {
		case audioQueued, audioRecognizing, audioASRError:
			work.status = audioQueued
			session.queue = append(session.queue, work)
		case audioTranslating, audioTranslateError:
			work.status = audioPending
		}
	}
	state := session.snapshotLocked()
	session.mu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		session.mu.Lock()
		session.processing = false
		session.status = "stopped"
		session.mu.Unlock()
		m.tasks.Finish(taskID)
		return AudioSessionState{}, errors.New("音频服务正在退出")
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go m.run(session, false)
	return state, nil
}

func (s *AudioSession) snapshot() AudioSessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}
func (s *AudioSession) snapshotLocked() AudioSessionState {
	state := AudioSessionState{Meeting: clone(s.meeting), Status: s.status, Recording: s.recording, Processing: s.processing, Error: s.lastError}
	n := time.Now()
	for _, work := range s.order {
		switch work.status {
		case audioQueued, audioRecognizing:
			state.PendingRecognition++
			delay := n.Sub(s.meeting.CreatedAt.Add(time.Duration(work.endMS) * time.Millisecond)).Milliseconds()
			if delay > state.RecognitionDelayMS {
				state.RecognitionDelayMS = delay
			}
		case audioPending, audioTranslating, audioTranslateError:
			state.PendingTranslation++
			if !work.recognizedAt.IsZero() {
				delay := n.Sub(work.recognizedAt).Milliseconds()
				if delay > state.TranslationDelayMS {
					state.TranslationDelayMS = delay
				}
			}
		}
	}
	if !s.recording && !s.processing {
		for _, work := range s.order {
			if work.status != audioComplete {
				state.CanResume = true
				break
			}
		}
	}
	return state
}

func (s *AudioSession) publishState() {
	s.mu.Lock()
	state := s.snapshotLocked()
	s.emitLocked("state", state)
	s.mu.Unlock()
}
func (s *AudioSession) reportError(segmentID, message string) {
	s.mu.Lock()
	s.lastError = message
	s.emitLocked("error", map[string]string{"segment_id": segmentID, "error": message})
	s.mu.Unlock()
}
func (s *AudioSession) emit(name string, value any) {
	s.mu.Lock()
	s.emitLocked(name, value)
	s.mu.Unlock()
}
func (s *AudioSession) emitLocked(name string, value any) {
	for subscriber := range s.subscribers {
		select {
		case subscriber <- audioEvent{name: name, value: value}:
		default:
			close(subscriber)
			delete(s.subscribers, subscriber)
		}
	}
}
func (s *AudioSession) subscribe() (<-chan audioEvent, func()) {
	channel := make(chan audioEvent, 256)
	s.mu.Lock()
	s.subscribers[channel] = struct{}{}
	s.mu.Unlock()
	return channel, func() {
		s.mu.Lock()
		if _, ok := s.subscribers[channel]; ok {
			delete(s.subscribers, channel)
			close(channel)
		}
		s.mu.Unlock()
	}
}

func (m *AudioManager) Session(id string) (AudioSessionState, bool) {
	m.mu.RLock()
	session := m.sessions[id]
	m.mu.RUnlock()
	if session != nil {
		return session.snapshot(), true
	}
	meeting, ok := m.store.AudioMeeting(id)
	if !ok {
		return AudioSessionState{}, false
	}
	return AudioSessionState{Meeting: meeting, Status: "saved"}, true
}
func (m *AudioManager) Meetings() []AudioMeeting { return m.store.AudioMeetings() }
func (m *AudioManager) Current() *AudioSessionState {
	m.mu.RLock()
	sessions := make([]*AudioSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.RUnlock()
	var current *AudioSessionState
	for _, session := range sessions {
		state := session.snapshot()
		if current == nil || state.Processing || state.Meeting.UpdatedAt.After(current.Meeting.UpdatedAt) {
			current = &state
		}
	}
	return current
}
func (m *AudioManager) Stop(id string) (AudioSessionState, bool) {
	m.mu.RLock()
	session := m.sessions[id]
	m.mu.RUnlock()
	if session == nil {
		return AudioSessionState{}, false
	}
	return session.Stop(), true
}
func (m *AudioManager) StopRecording(id string) (AudioSessionState, bool) {
	m.mu.RLock()
	session := m.sessions[id]
	m.mu.RUnlock()
	if session == nil {
		return AudioSessionState{}, false
	}
	return session.StopRecording(), true
}
func (m *AudioManager) Subscribe(id string) (<-chan audioEvent, func(), AudioSessionState, bool) {
	m.mu.RLock()
	session := m.sessions[id]
	m.mu.RUnlock()
	if session == nil {
		return nil, nil, AudioSessionState{}, false
	}
	events, cancel := session.subscribe()
	return events, cancel, session.snapshot(), true
}
func (m *AudioManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	sessions := make([]*AudioSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.Unlock()
	for _, session := range sessions {
		session.Stop()
	}
	m.wg.Wait()
	for _, session := range sessions {
		session.mu.Lock()
		for subscriber := range session.subscribers {
			close(subscriber)
			delete(session.subscribers, subscriber)
		}
		session.mu.Unlock()
	}
	m.whisper.Close()
}
