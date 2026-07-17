// Package session TaskSession 管理：内存索引 + JSON 文件持久化（不入库）。
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/nucleagent/nucleagent-shared/a2a"
)

// Store TaskSession 存储（并发安全）。
type Store struct {
	mu       sync.RWMutex
	sessions map[string]a2a.TaskSession
	file     string // 为空则仅内存，不落盘
}

// NewStore 创建存储；file 为空表示仅内存。创建时尝试从文件加载已有会话。
func NewStore(file string) *Store {
	s := &Store{
		sessions: make(map[string]a2a.TaskSession),
		file:     file,
	}
	_ = s.load()
	return s
}

// Create 创建并保存会话（status 为空时默认 running）。
func (s *Store) Create(sess a2a.TaskSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess.Status == "" {
		sess.Status = "running"
	}
	s.sessions[sess.ID] = sess
	return s.persistLocked()
}

// Get 读取会话。
func (s *Store) Get(id string) (a2a.TaskSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

// Update 更新会话状态。
func (s *Store) Update(id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return fmt.Errorf("session %q not found", id)
	}
	sess.Status = status
	s.sessions[id] = sess
	return s.persistLocked()
}

// persistLocked 持久化到 JSON 文件（调用方已持锁）。
func (s *Store) persistLocked() error {
	if s.file == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.sessions, "", "  ")
	if err != nil {
		return fmt.Errorf("session persist: %w", err)
	}
	return os.WriteFile(s.file, b, 0o644)
}

// load 从 JSON 文件加载（文件不存在视为空）。
func (s *Store) load() error {
	if s.file == "" {
		return nil
	}
	b, err := os.ReadFile(s.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, &s.sessions)
}
