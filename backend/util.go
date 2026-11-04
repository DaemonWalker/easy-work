package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

func id() string     { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func now() time.Time { return time.Now().UTC() }
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	log.Printf("API 错误: status=%d error=%s", status, message)
	writeJSON(w, status, map[string]string{"error": message})
}
func decodeJSON(r *http.Request, value any) error {
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(value)
}
func validTab(tab string) bool {
	for _, item := range modelTabs {
		if tab == item {
			return true
		}
	}
	return false
}

const (
	paddleOCRModel = "PaddleOCR-VL-1.6-GGUF.gguf"
	paddleMMProj   = "PaddleOCR-VL-1.6-GGUF-mmproj.gguf"
	indexMMProj    = "Index-Translate-9B.mmproj-Q8_0.gguf"
)

func isHyMT2Model(name string) bool {
	return name == "Hy-MT2-7B-Q4_K_M.gguf" || name == "HY-MT2-7B-Q6_K.gguf"
}
func isIndexModel(name string) bool {
	return name == "Index-Translate-9B.Q4_K_M.gguf" || name == "Index-Translate-9B.Q5_K_M.gguf"
}
func isZhJaModel(name string) bool {
	return isHyMT2Model(name) || isIndexModel(name)
}
func isJaZhModel(name string) bool {
	return isHyMT2Model(name) || isIndexModel(name)
}
func isImageOCRModel(name string) bool {
	return name == paddleOCRModel || isIndexModel(name)
}
func isImageTranslationModel(name string) bool {
	return isJaZhModel(name)
}
func allowsModel(tab, model string) bool {
	switch tab {
	case TabColleague, TabDocument, TabTestcase:
		return isZhJaModel(model)
	case TabJa2zh:
		return isJaZhModel(model)
	case TabCustom:
		return true
	default:
		return false
	}
}
func trimmedTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 36 {
		return string(r[:36]) + "…"
	}
	if s == "" {
		return "新对话"
	}
	return s
}
