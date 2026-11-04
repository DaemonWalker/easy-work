package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

type fixedHardware struct {
	available uint64
}

func (h fixedHardware) Sample() Hardware {
	return Hardware{MemoryAvailable: h.available}
}

func TestIndexVLModelPath(t *testing.T) {
	root := t.TempDir()
	modelPath := filepath.Join(root, "gguf", "chat", testIndexModel)
	manager := NewModelManager(root, nil, fixedHardware{available: 16 * gib}, []ModelInfo{{ID: testIndexModel, Kind: "chat", Path: modelPath, Size: 100}})
	path, info, err := manager.modelPath("vl", testIndexModel)
	if err != nil {
		t.Fatal(err)
	}
	if path != modelPath || info.Kind != "vl" {
		t.Fatalf("Index VL 未复用 Chat 主模型: path=%s info=%+v", path, info)
	}
	if _, _, err = manager.modelPath("vl", "unknown.gguf"); err == nil {
		t.Fatal("未知 VL 模型未被拒绝")
	}
}

func TestEnsureSwitchesSameKindAndKeepsOtherKindWithMemory(t *testing.T) {
	models := []ModelInfo{{ID: testHyModel, Kind: "chat", Path: "hy.gguf"}, {ID: testIndexModel, Kind: "chat", Path: "index.gguf"}}
	tests := []struct {
		name       string
		targetKind string
		targetID   string
		otherKind  string
		otherID    string
	}{
		{name: "Hy Chat 切换为 Index Chat 并保留 VL", targetKind: "chat", targetID: testIndexModel, otherKind: "vl", otherID: paddleOCRModel},
		{name: "Paddle VL 切换为 Index VL 并保留 Chat", targetKind: "vl", targetID: testIndexModel, otherKind: "chat", otherID: testHyModel},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewModelManager(t.TempDir(), nil, fixedHardware{available: 16 * gib}, models)
			manager.status["chat"] = &ModelStatus{Kind: "chat", Status: "loaded", Model: testHyModel}
			manager.status["vl"] = &ModelStatus{Kind: "vl", Status: "loaded", Model: paddleOCRModel}
			if err := manager.Ensure(context.Background(), test.targetKind, test.targetID); err == nil || !strings.Contains(err.Error(), "未找到 llamacpp/llama-server.exe") {
				t.Fatalf("Ensure() error = %v", err)
			}
			if got := manager.loadedID(test.targetKind); got != "" {
				t.Fatalf("同类旧模型未卸载: %s", got)
			}
			if got := manager.loadedID(test.otherKind); got != test.otherID {
				t.Fatalf("内存充足时另一类模型未保留: got=%s want=%s", got, test.otherID)
			}
		})
	}
}

func TestEnsureDoesNotRejectLowMemory(t *testing.T) {
	tests := []struct {
		name      string
		kind      string
		modelID   string
		available uint64
	}{
		{name: "chat below threshold", kind: "chat", modelID: "chat.gguf", available: 4*gib - 1},
		{name: "chat at threshold", kind: "chat", modelID: "chat.gguf", available: 4 * gib},
		{name: "vl below threshold", kind: "vl", available: 2*gib - 1},
		{name: "vl at threshold", kind: "vl", available: 2 * gib},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			models := []ModelInfo{{ID: "chat.gguf", Kind: "chat", Path: "chat.gguf", Size: int64(100 * gib)}}
			manager := NewModelManager(t.TempDir(), nil, fixedHardware{available: test.available}, models)
			err := manager.Ensure(context.Background(), test.kind, test.modelID)
			wantError := "未找到 llamacpp/llama-server.exe"
			if err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("Ensure() error = %v, want error containing %q", err, wantError)
			}
		})
	}
}
