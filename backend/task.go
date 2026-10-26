package main

import (
	"context"
	"errors"
	"sync"
)

var errBusy = errors.New("另一项模型任务正在执行，请停止或等待其完成；请求不会排队")

type TaskManager struct {
	mu     sync.RWMutex
	state  GlobalState
	cancel context.CancelFunc
}

func NewTaskManager() *TaskManager { return &TaskManager{state: GlobalState{}} }
func (t *TaskManager) Start(parent context.Context, kind string) (string, context.Context, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state.Busy {
		return "", nil, errBusy
	}
	ctx, cancel := context.WithCancel(parent)
	taskID := id()
	t.state = GlobalState{Busy: true, TaskID: taskID, Kind: kind, Stage: "检查模型"}
	t.cancel = cancel
	return taskID, ctx, nil
}
func (t *TaskManager) Stage(taskID, stage string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state.TaskID == taskID {
		t.state.Stage = stage
	}
}
func (t *TaskManager) Finish(taskID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state.TaskID == taskID {
		t.state = GlobalState{}
		t.cancel = nil
	}
}
func (t *TaskManager) Stop() bool {
	t.mu.RLock()
	cancel := t.cancel
	busy := t.state.Busy
	t.mu.RUnlock()
	if busy && cancel != nil {
		cancel()
		return true
	}
	return false
}
func (t *TaskManager) State() GlobalState { t.mu.RLock(); defer t.mu.RUnlock(); return t.state }
