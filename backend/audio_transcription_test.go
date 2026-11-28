package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAudioRecorderRoundTripAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recording.wav")
	recorder, err := newAudioRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []int16{-100, 0, 100, 200, 300}
	if err = recorder.Write(want); err != nil {
		t.Fatal(err)
	}
	if err = recorder.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := readWAV(path)
	if err != nil || len(got) != len(want) {
		t.Fatalf("完整录音读取失败: samples=%v error=%v", got, err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("完整录音样本不一致: index=%d got=%d want=%d", i, got[i], want[i])
		}
	}
	info, err := audioRecordingInfo(path)
	if err != nil || info.Status != "ready" || info.Size != int64(44+len(want)*2) {
		t.Fatalf("完整录音元数据错误: %+v error=%v", info, err)
	}

	crashedPath := filepath.Join(t.TempDir(), "recording.wav")
	crashed, err := newAudioRecorder(crashedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = crashed.Write(want); err != nil {
		t.Fatal(err)
	}
	if err = crashed.writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err = crashed.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = recoverAudioRecording(crashedPath); err != nil {
		t.Fatal(err)
	}
	got, err = readWAV(crashedPath)
	if err != nil || len(got) != len(want) {
		t.Fatalf("异常退出录音恢复失败: samples=%v error=%v", got, err)
	}
}

func TestAudioCaptureKeepsCompleteRecordingIncludingSilence(t *testing.T) {
	app := newTestApp(t)
	capture := newFakeAudioCapture(make([]int16, audioSampleRate))
	manager := installAudioManager(app, capture, audioTestWhisper())
	defer manager.Close()
	state, session := startAudio(t, manager)
	waitAudioRun(t, session)
	saved, _ := app.store.AudioMeeting(state.Meeting.ID)
	if saved.Recording == nil || saved.Recording.Status != "ready" || saved.Recording.DurationMS != 1000 {
		t.Fatalf("完整录音状态错误: %+v", saved.Recording)
	}
	samples, err := readWAV(audioRecordingPath(app.store.dir, saved.ID))
	if err != nil || len(samples) != audioSampleRate {
		t.Fatalf("包含静音的完整录音未保存: samples=%d error=%v", len(samples), err)
	}
}

func TestAudioTranscriptionAfterRestartAndIndependentTranslation(t *testing.T) {
	chat := newMockLlama()
	startMock(t, "18181", chat, "chat")
	app := newTestApp(t)
	loaded(app, "chat", "Hy-MT2-7B-Q4_K_M.gguf")
	n := now()
	meeting := AudioMeeting{ID: id(), Source: "system", WhisperModel: "ggml-small.bin", ChatModel: "Hy-MT2-7B-Q4_K_M.gguf", Segments: []AudioSegment{{ID: "realtime", Source: "实时原文", Translation: "实时译文", Status: audioComplete}}, Recording: &AudioRecording{Status: "ready"}, CreatedAt: n, UpdatedAt: n}
	if err := app.store.AddAudioMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	if err := writeWAV(audioRecordingPath(app.store.dir, meeting.ID), speechChunk(4000)); err != nil {
		t.Fatal(err)
	}
	whisper := audioTestWhisper()
	whisper.models = append(whisper.models, WhisperModel{ID: "ggml-large.bin", Name: "ggml-large.bin"})
	manager := NewAudioManager(app.store, app.models, app.tasks, whisper, nil)
	app.audio = manager
	defer manager.Close()
	state, err := manager.StartTranscription(meeting.ID, "ggml-large.bin")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Meeting.Transcriptions) != 1 || state.Meeting.Transcriptions[0].WhisperModel != "ggml-large.bin" {
		t.Fatalf("高精度版本未独立创建: %+v", state.Meeting.Transcriptions)
	}
	manager.mu.RLock()
	session := manager.sessions[meeting.ID]
	manager.mu.RUnlock()
	waitAudioRun(t, session)
	saved, _ := app.store.AudioMeeting(meeting.ID)
	if len(saved.Segments) != 1 || saved.Segments[0].Source != "实时原文" || saved.Segments[0].Translation != "实时译文" {
		t.Fatalf("实时结果被覆盖: %+v", saved.Segments)
	}
	if len(saved.Transcriptions) != 1 || len(saved.Transcriptions[0].Segments) != 1 || saved.Transcriptions[0].Status != "complete" {
		t.Fatalf("重启后的完整录音未能转录: %+v", saved.Transcriptions)
	}
	transcription := saved.Transcriptions[0]
	if transcription.Translate || transcription.Segments[0].Translation != "" || transcription.Segments[0].Status != audioPending {
		t.Fatalf("转录阶段意外生成中文: %+v", transcription)
	}
	if chat.chatCalls.Load() != 0 {
		t.Fatal("高精度转录阶段不应调用 Chat")
	}
	loaded(app, "chat", "HY-MT2-7B-Q6_K.gguf")
	if _, err = manager.StartTranslation(meeting.ID, transcription.ID, "HY-MT2-7B-Q6_K.gguf"); err != nil {
		t.Fatal(err)
	}
	waitAudioRun(t, session)
	saved, _ = app.store.AudioMeeting(meeting.ID)
	transcription = saved.Transcriptions[0]
	if !transcription.Translate || transcription.ChatModel != "HY-MT2-7B-Q6_K.gguf" || transcription.Segments[0].Translation != "完成回复" || transcription.Segments[0].Status != audioComplete {
		t.Fatalf("独立模型翻译未完成: %+v", transcription)
	}
	if chat.chatCalls.Load() != 1 {
		t.Fatalf("高精度译文调用次数错误: %d", chat.chatCalls.Load())
	}
	if _, err = manager.DeleteRecording(meeting.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(audioRecordingPath(app.store.dir, meeting.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("完整录音未删除: %v", err)
	}
	saved, _ = app.store.AudioMeeting(meeting.ID)
	if saved.Recording != nil || len(saved.Transcriptions) != 1 || saved.Transcriptions[0].Segments[0].Source == "" {
		t.Fatalf("删除录音破坏了文本历史: %+v", saved)
	}
	if _, err = manager.StartTranscription(meeting.ID, "ggml-large.bin"); err == nil {
		t.Fatal("删除录音后仍能启动高精度转录")
	}
	if _, _, err = app.tasks.Start(context.Background(), "chat"); err != nil {
		t.Fatalf("任务结束后全局互斥未释放: %v", err)
	} else {
		app.tasks.Finish(app.tasks.State().TaskID)
	}
}

func TestAudioTranscriptionStopKeepsOnlyCompleteSegments(t *testing.T) {
	app := newTestApp(t)
	n := now()
	meeting := AudioMeeting{ID: id(), Source: "system", WhisperModel: "ggml-small.bin", ChatModel: "Hy-MT2-7B-Q4_K_M.gguf", Segments: []AudioSegment{}, Recording: &AudioRecording{Status: "ready"}, CreatedAt: n, UpdatedAt: n}
	if err := app.store.AddAudioMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	recording := append(speechChunk(4000), speechChunk(5000)...)
	if err := writeWAV(audioRecordingPath(app.store.dir, meeting.ID), recording); err != nil {
		t.Fatal(err)
	}
	whisper := audioTestWhisper()
	started := make(chan struct{})
	whisper.infer = func(ctx context.Context, call int, path string) (string, error) {
		if call == 0 {
			return "已完整识别", nil
		}
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	manager := NewAudioManager(app.store, app.models, app.tasks, whisper, nil)
	defer manager.Close()
	if _, err := manager.StartTranscription(meeting.ID, "ggml-small.bin"); err != nil {
		t.Fatal(err)
	}
	manager.mu.RLock()
	session := manager.sessions[meeting.ID]
	manager.mu.RUnlock()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("第二段高精度识别未开始")
	}
	if !app.tasks.Stop() {
		t.Fatal("高精度转录停止请求未接受")
	}
	waitAudioRun(t, session)
	saved, _ := app.store.AudioMeeting(meeting.ID)
	if len(saved.Transcriptions) != 1 || saved.Transcriptions[0].Status != "stopped" || len(saved.Transcriptions[0].Segments) != 1 {
		t.Fatalf("停止后保存了不完整识别或丢失完整段: %+v", saved.Transcriptions)
	}
	if saved.Transcriptions[0].Segments[0].Source != "已完整识别" || saved.Recording == nil || saved.Recording.Status != "ready" {
		t.Fatalf("停止后完整结果或录音被破坏: %+v", saved)
	}
}

func TestAudioCanResumeDoesNotDependOnTranscriptionStatus(t *testing.T) {
	session := &AudioSession{meeting: AudioMeeting{ID: "meeting", CreatedAt: now()}, status: "transcription_complete", order: []*audioWork{{status: audioASRError}}}
	if !session.snapshotLocked().CanResume {
		t.Fatal("高精度任务状态隐藏了实时会议的会后补全能力")
	}
	session.processing = true
	if session.snapshotLocked().CanResume {
		t.Fatal("处理期间不应允许启动实时会后补全")
	}
}

func TestAudioRecordingDownloadAndDeleteAPI(t *testing.T) {
	app := newTestApp(t)
	n := now()
	meeting := AudioMeeting{ID: id(), Source: "system", WhisperModel: "ggml-small.bin", ChatModel: "Hy-MT2-7B-Q4_K_M.gguf", Segments: []AudioSegment{}, Recording: &AudioRecording{Status: "ready"}, CreatedAt: n, UpdatedAt: n}
	if err := app.store.AddAudioMeeting(meeting); err != nil {
		t.Fatal(err)
	}
	if err := writeWAV(audioRecordingPath(app.store.dir, meeting.ID), speechChunk(4000)); err != nil {
		t.Fatal(err)
	}
	manager := NewAudioManager(app.store, app.models, app.tasks, audioTestWhisper(), nil)
	app.audio = manager
	defer manager.Close()
	server := httptest.NewServer(app.routes())
	defer server.Close()
	response, err := http.Get(server.URL + "/api/audio/meetings/" + meeting.ID + "/recording")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || len(data) != 44+audioSampleRate*2 || response.Header.Get("Content-Disposition") != "attachment" {
		t.Fatalf("完整录音下载失败: status=%d bytes=%d error=%v", response.StatusCode, len(data), err)
	}
	request, err := http.NewRequest(http.MethodDelete, server.URL+"/api/audio/meetings/"+meeting.ID+"/recording", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("完整录音删除 API 状态=%d", response.StatusCode)
	}
	saved, _ := app.store.AudioMeeting(meeting.ID)
	if saved.Recording != nil {
		t.Fatal("删除 API 未更新录音元数据")
	}
}
