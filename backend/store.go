package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
)

type Store struct {
	mu            sync.RWMutex
	dir           string
	settings      Settings
	conversations []Conversation
	images        []ImageTask
}

func defaultSettings() Settings {
	tabs := map[string]string{}
	for _, tab := range modelTabs {
		tabs[tab] = ""
	}
	return Settings{Theme: "cloud", Threads: runtime.NumCPU(), Context: 4096, Generation: GenerationSettings{MaxTokens: 2048}, TabModels: tabs}
}

func NewStore(dir string) (*Store, error) {
	s := &Store{dir: dir, settings: defaultSettings()}
	for _, sub := range []string{"", "images"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			return nil, err
		}
	}
	if err := s.read("settings.json", &s.settings); err != nil {
		return nil, err
	}
	if err := s.read("conversations.json", &s.conversations); err != nil {
		return nil, err
	}
	if err := s.read("images.json", &s.images); err != nil {
		return nil, err
	}
	if s.settings.TabModels == nil {
		s.settings.TabModels = defaultSettings().TabModels
	}
	for _, tab := range modelTabs {
		if _, ok := s.settings.TabModels[tab]; !ok {
			s.settings.TabModels[tab] = ""
		}
	}
	if s.settings.Threads < 1 {
		s.settings.Threads = runtime.NumCPU()
	}
	if s.settings.Context < 1 {
		s.settings.Context = 4096
	}
	if s.settings.Generation.MaxTokens < 1 {
		s.settings.Generation.MaxTokens = 2048
	}
	return s, nil
}
func (s *Store) read(name string, target any) error {
	path := filepath.Join(s.dir, name)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		log.Printf("运行数据读取失败: path=%s error=%v", path, err)
		return err
	}
	if len(data) == 0 {
		return nil
	}
	if err = json.Unmarshal(data, target); err != nil {
		log.Printf("运行数据解析失败: path=%s error=%v", path, err)
		return fmt.Errorf("读取 %s: %w", name, err)
	}
	return nil
}
func atomicWrite(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		log.Printf("运行数据序列化失败: path=%s error=%v", path, err)
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("运行数据临时文件写入失败: path=%s error=%v", tmp, err)
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		if retry := os.Rename(tmp, path); retry != nil {
			log.Printf("运行数据保存失败: path=%s error=%v", path, retry)
			return err
		}
	}
	return nil
}
func (s *Store) saveLocked(name string, value any) error {
	return atomicWrite(filepath.Join(s.dir, name), value)
}
func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.settings
	out.TabModels = map[string]string{}
	for k, v := range s.settings.TabModels {
		out.TabModels[k] = v
	}
	return out
}
func (s *Store) UpdateSettings(fn func(*Settings) error) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.settings
	next.TabModels = map[string]string{}
	for k, v := range s.settings.TabModels {
		next.TabModels[k] = v
	}
	if err := fn(&next); err != nil {
		return Settings{}, err
	}
	if err := s.saveLocked("settings.json", next); err != nil {
		return Settings{}, err
	}
	s.settings = next
	out := next
	out.TabModels = next.TabModels
	return out, nil
}
func (s *Store) Conversations(tab string) []Conversation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Conversation{}
	for _, c := range s.conversations {
		if c.Tab == tab {
			out = append(out, clone(c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}
func (s *Store) Conversation(id string) (Conversation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.conversations {
		if c.ID == id {
			return clone(c), true
		}
	}
	return Conversation{}, false
}
func clone[T any](v T) T {
	data, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(data, &out)
	return out
}
func (s *Store) NewConversation(tab, model string) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := now()
	c := Conversation{ID: id(), Tab: tab, Title: "新对话", Model: model, Messages: []Message{}, CreatedAt: n, UpdatedAt: n}
	s.conversations = append(s.conversations, c)
	if err := s.saveLocked("conversations.json", s.conversations); err != nil {
		return Conversation{}, err
	}
	return clone(c), nil
}
func (s *Store) UpdateConversation(id string, fn func(*Conversation) error) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.conversations {
		if s.conversations[i].ID == id {
			next := clone(s.conversations[i])
			if err := fn(&next); err != nil {
				return Conversation{}, err
			}
			next.UpdatedAt = now()
			all := append([]Conversation{}, s.conversations...)
			all[i] = next
			if err := s.saveLocked("conversations.json", all); err != nil {
				return Conversation{}, err
			}
			s.conversations = all
			return clone(next), nil
		}
	}
	return Conversation{}, os.ErrNotExist
}
func (s *Store) Images() []ImageTask {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ImageTask, len(s.images))
	for i, v := range s.images {
		out[i] = clone(v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
func (s *Store) Image(id string) (ImageTask, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.images {
		if v.ID == id {
			return clone(v), true
		}
	}
	return ImageTask{}, false
}
func (s *Store) AddImage(v ImageTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := append([]ImageTask{v}, s.images...)
	if err := s.saveLocked("images.json", all); err != nil {
		return err
	}
	s.images = all
	return nil
}
func (s *Store) UpdateImage(id string, fn func(*ImageTask) error) (ImageTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.images {
		if s.images[i].ID == id {
			next := clone(s.images[i])
			if err := fn(&next); err != nil {
				return ImageTask{}, err
			}
			next.UpdatedAt = now()
			all := append([]ImageTask{}, s.images...)
			all[i] = next
			if err := s.saveLocked("images.json", all); err != nil {
				return ImageTask{}, err
			}
			s.images = all
			return clone(next), nil
		}
	}
	return ImageTask{}, os.ErrNotExist
}
