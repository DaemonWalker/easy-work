package main

import "time"

const (
	TabColleague = "colleague"
	TabDocument  = "document"
	TabTestcase  = "testcase"
	TabJa2zh     = "ja2zh"
	TabCustom    = "custom"
)

var modelTabs = []string{TabColleague, TabDocument, TabTestcase, TabJa2zh, TabCustom}

type GenerationSettings struct {
	Temperature   *float64 `json:"temperature"`
	TopP          *float64 `json:"top_p"`
	TopK          *int     `json:"top_k"`
	MinP          *float64 `json:"min_p"`
	RepeatPenalty *float64 `json:"repeat_penalty"`
	MaxTokens     int      `json:"max_tokens"`
}

type Settings struct {
	Theme             string             `json:"theme"`
	Threads           int                `json:"threads"`
	Context           int                `json:"context"`
	Generation        GenerationSettings `json:"generation"`
	TabModels         map[string]string  `json:"tab_models"`
	ImageModel        string             `json:"image_model"`
	ImageOCRModel     string             `json:"image_ocr_model"`
	ImageTranslate    bool               `json:"image_translate"`
	TranslationReview bool               `json:"translation_review"`
	AudioSource       string             `json:"audio_source"`
	WhisperModel      string             `json:"whisper_model"`
	AudioChatModel    string             `json:"audio_chat_model"`
}

const MessageReview = "review"

type Message struct {
	ID         string         `json:"id"`
	Role       string         `json:"role"`
	Kind       string         `json:"kind,omitempty"`
	Content    string         `json:"content"`
	Model      string         `json:"model,omitempty"`
	RomajiMode string         `json:"romaji_mode,omitempty"`
	Stats      map[string]any `json:"stats,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

type Conversation struct {
	ID          string    `json:"id"`
	Tab         string    `json:"tab"`
	Title       string    `json:"title"`
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	ContextUsed int       `json:"context_used"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ImageTask struct {
	ID               string         `json:"id"`
	Filename         string         `json:"filename"`
	Mime             string         `json:"mime"`
	Width            int            `json:"width"`
	Height           int            `json:"height"`
	OCR              string         `json:"ocr"`
	Translation      string         `json:"translation"`
	OCRStats         map[string]any `json:"ocr_stats,omitempty"`
	TranslationStats map[string]any `json:"translation_stats,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

type AudioSegment struct {
	ID               string         `json:"id"`
	StartMS          int64          `json:"start_ms"`
	EndMS            int64          `json:"end_ms"`
	Source           string         `json:"source"`
	Translation      string         `json:"translation"`
	Status           string         `json:"status"`
	RecognitionStats map[string]any `json:"recognition_stats,omitempty"`
	TranslationStats map[string]any `json:"translation_stats,omitempty"`
	RecognizedAt     time.Time      `json:"recognized_at"`
	TranslatedAt     *time.Time     `json:"translated_at,omitempty"`
}

type AudioMeeting struct {
	ID           string         `json:"id"`
	Source       string         `json:"source"`
	WhisperModel string         `json:"whisper_model"`
	ChatModel    string         `json:"chat_model"`
	Segments     []AudioSegment `json:"segments"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	EndedAt      *time.Time     `json:"ended_at,omitempty"`
}

type AudioSessionState struct {
	Meeting            AudioMeeting `json:"meeting"`
	Status             string       `json:"status"`
	Recording          bool         `json:"recording"`
	Processing         bool         `json:"processing"`
	PendingRecognition int          `json:"pending_recognition"`
	PendingTranslation int          `json:"pending_translation"`
	RecognitionDelayMS int64        `json:"recognition_delay_ms"`
	TranslationDelayMS int64        `json:"translation_delay_ms"`
	Error              string       `json:"error,omitempty"`
}

type WhisperModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type ModelInfo struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Name       string         `json:"name"`
	Path       string         `json:"path"`
	Size       int64          `json:"size"`
	MaxContext int            `json:"max_context"`
	Meta       map[string]any `json:"meta,omitempty"`
}

type ModelStatus struct {
	Kind         string         `json:"kind"`
	Model        string         `json:"model"`
	Status       string         `json:"status"`
	Error        string         `json:"error,omitempty"`
	RestartCount int            `json:"restart_count"`
	LoadMS       int64          `json:"load_ms"`
	Details      map[string]any `json:"details,omitempty"`
}

type GlobalState struct {
	Busy   bool   `json:"busy"`
	TaskID string `json:"task_id"`
	Kind   string `json:"kind"`
	Stage  string `json:"stage"`
}

type StateResponse struct {
	Settings      Settings           `json:"settings"`
	Models        []ModelInfo        `json:"models"`
	Status        []ModelStatus      `json:"status"`
	Task          GlobalState        `json:"task"`
	Hardware      Hardware           `json:"hardware"`
	WhisperModels []WhisperModel     `json:"whisper_models"`
	Whisper       ModelStatus        `json:"whisper"`
	Audio         *AudioSessionState `json:"audio,omitempty"`
}

type Hardware struct {
	CPU             float64 `json:"cpu"`
	MemoryUsed      uint64  `json:"memory_used"`
	MemoryTotal     uint64  `json:"memory_total"`
	MemoryAvailable uint64  `json:"memory_available"`
	RefreshSeconds  int     `json:"refresh_seconds"`
}
