package main

import (
	"log"
	"os"
)

func (m *AudioManager) recoverAfterRestart() {
	for _, meeting := range m.store.AudioMeetings() {
		changed := false
		if meeting.Recording != nil && (meeting.Recording.Status == "recording" || meeting.Recording.Status == "error") {
			path := audioRecordingPath(m.store.dir, meeting.ID)
			recording, err := recoverAudioRecording(path)
			if err != nil {
				recording = AudioRecording{Status: "error"}
				log.Printf("完整录音恢复失败: session=%s error=%v", meeting.ID, err)
			} else {
				log.Printf("完整录音已恢复: session=%s size=%d", meeting.ID, recording.Size)
			}
			meeting.Recording = &recording
			if meeting.EndedAt == nil {
				n := now()
				if info, statErr := os.Stat(path); statErr == nil {
					n = info.ModTime()
				}
				meeting.EndedAt = &n
			}
			changed = true
		}
		for i := range meeting.Transcriptions {
			transcription := &meeting.Transcriptions[i]
			if transcription.Status != "recognizing" && transcription.Status != "translating" {
				continue
			}
			n := now()
			transcription.Status = "stopped"
			transcription.EndedAt = &n
			changed = true
		}
		if changed {
			if _, err := m.store.UpdateAudioMeeting(meeting.ID, func(saved *AudioMeeting) error {
				saved.Recording = meeting.Recording
				saved.Transcriptions = meeting.Transcriptions
				saved.EndedAt = meeting.EndedAt
				return nil
			}); err != nil {
				log.Printf("音频会议恢复状态保存失败: session=%s error=%v", meeting.ID, err)
			}
		}
	}
}
