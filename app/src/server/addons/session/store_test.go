package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nucleagent/nucleagent-shared/a2a"
)

// TestStoreCreateGetUpdate 验证内存 CRUD。
func TestStoreCreateGetUpdate(t *testing.T) {
	s := NewStore("") // 仅内存
	sess := a2a.TaskSession{
		ID:             "sess-1",
		ConversationID: 42,
		StepID:         "step-1",
		Backend:        "mock-llm",
	}
	if err := s.Create(sess); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, ok := s.Get("sess-1")
	if !ok {
		t.Fatal("Get: not found")
	}
	if got.Status != "running" {
		t.Errorf("default status = %q, want running", got.Status)
	}
	if got.Backend != "mock-llm" {
		t.Errorf("Backend = %q, want mock-llm", got.Backend)
	}

	if err := s.Update("sess-1", "completed"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = s.Get("sess-1")
	if got.Status != "completed" {
		t.Errorf("after Update, status = %q, want completed", got.Status)
	}
}

// TestStoreUpdateMissing 验证更新不存在的 session 报错。
func TestStoreUpdateMissing(t *testing.T) {
	s := NewStore("")
	if err := s.Update("nope", "done"); err == nil {
		t.Error("Update on missing session should fail")
	}
}

// TestStorePersistenceRoundTrip 验证 JSON 文件持久化往返。
func TestStorePersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sessions.json")

	s1 := NewStore(file)
	sess := a2a.TaskSession{
		ID:             "sess-2",
		ConversationID: 7,
		StepID:         "step-2",
		Backend:        "mock-llm",
		Status:         "running",
	}
	if err := s1.Create(sess); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 新 store 从同一文件加载，应能读到 session。
	s2 := NewStore(file)
	got, ok := s2.Get("sess-2")
	if !ok {
		t.Fatal("reload: session not found")
	}
	if got.ConversationID != 7 || got.Backend != "mock-llm" {
		t.Errorf("reload mismatch: %+v", got)
	}
}

// TestStoreLoadMissingFile 验证文件不存在时正常启动（不报错）。
func TestStoreLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "does-not-exist.json")
	s := NewStore(file)
	if _, ok := s.Get("anything"); ok {
		t.Error("expected not found on fresh store")
	}
}

// TestStoreLoadEmptyFile 验证空文件不报错。
func TestStoreLoadEmptyFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(file, []byte{}, 0o644); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	s := NewStore(file) // 不应 panic / 报错
	if _, ok := s.Get("x"); ok {
		t.Error("expected not found")
	}
}
