package main

import (
	"context"
	"strings"
	"testing"
)

type fixedHardware struct {
	available uint64
}

func (h fixedHardware) Sample() Hardware {
	return Hardware{MemoryAvailable: h.available}
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
