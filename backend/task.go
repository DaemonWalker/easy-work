package main

import (
	"context"
	"errors"
	"log"
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
		log.Printf("任务拒绝：另一任务执行中: kind=%s running_task=%s running_kind=%s", kind, t.state.TaskID, t.state.Kind)
		return "", nil, errBusy
	}
	ctx, cancel := context.WithCancel(parent)
	taskID := id()
	t.state = GlobalState{Busy: true, TaskID: taskID, Kind: kind, Stage: "检查模型"}
	t.cancel = cancel
	log.Printf("任务开始: id=%s kind=%s", taskID, kind)
	return taskID, ctx, nil
}
func (t *TaskManager) Stage(taskID, stage string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state.TaskID == taskID {
		t.state.Stage = stage
		log.Printf("任务阶段: id=%s kind=%s stage=%s", taskID, t.state.Kind, stage)
	}
}
func (t *TaskManager) Finish(taskID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state.TaskID == taskID {
		log.Printf("任务结束: id=%s kind=%s last_stage=%s", taskID, t.state.Kind, t.state.Stage)
		t.state = GlobalState{}
		t.cancel = nil
	}
}
func (t *TaskManager) Stop() bool {
	t.mu.RLock()
	cancel := t.cancel
	busy := t.state.Busy
	taskID := t.state.TaskID
	kind := t.state.Kind
	t.mu.RUnlock()
	if busy && cancel != nil {
		log.Printf("任务停止请求: id=%s kind=%s", taskID, kind)
		cancel()
		return true
	}
	log.Printf("任务停止请求：当前无运行任务")
	return false
}
func (t *TaskManager) State() GlobalState { t.mu.RLock(); defer t.mu.RUnlock(); return t.state }
