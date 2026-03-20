package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreLoadMissingKeyReturnsNil(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "sessions.json"))

	record, err := store.Load("missing")
	if err != nil {
		t.Fatalf("load missing key: %v", err)
	}
	if record != nil {
		t.Fatalf("expected missing key to return nil, got %#v", record)
	}
}

func TestFileStoreSaveLoadDeleteRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "sessions.json")
	store := NewFileStore(path)
	now := time.Unix(123, 0)
	record := StoredSession{
		SessionID: "session-1",
		Spec: Spec{
			Model:               "gpt-4.1",
			SystemPrompt:        "Be brief.",
			SystemPromptMode:    "replace",
			ReasoningEffort:     "high",
			ProviderFingerprint: "provider-fingerprint",
		},
		CreatedAt: now,
		UpdatedAt: now.Add(time.Minute),
	}

	if err := store.Save("tenant:session", record); err != nil {
		t.Fatalf("save record: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read store dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "sessions.json" {
		t.Fatalf("expected atomic save to leave only sessions.json, got %#v", entries)
	}

	loaded, err := store.Load("tenant:session")
	if err != nil {
		t.Fatalf("load saved record: %v", err)
	}
	if loaded == nil || loaded.SessionID != record.SessionID || loaded.Spec != record.Spec || !loaded.CreatedAt.Equal(record.CreatedAt) || !loaded.UpdatedAt.Equal(record.UpdatedAt) {
		t.Fatalf("unexpected loaded record %#v", loaded)
	}

	if err := store.Delete("tenant:session"); err != nil {
		t.Fatalf("delete record: %v", err)
	}
	deleted, err := store.Load("tenant:session")
	if err != nil {
		t.Fatalf("load deleted record: %v", err)
	}
	if deleted != nil {
		t.Fatalf("expected deleted record to be absent, got %#v", deleted)
	}
}

func TestFileStoreDoesNotPersistProviderSecretFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	store := NewFileStore(path)
	record := StoredSession{
		SessionID: "session-1",
		Spec: Spec{
			Model:                     "gpt-4.1",
			ProviderFingerprint:       "public",
			ProviderSecretFingerprint: "secret",
		},
	}

	if err := store.Save("tenant:session", record); err != nil {
		t.Fatalf("save record: %v", err)
	}
	loaded, err := store.Load("tenant:session")
	if err != nil {
		t.Fatalf("load record: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected stored record")
	}
	if loaded.Spec.ProviderSecretFingerprint != "" {
		t.Fatalf("expected provider secret fingerprint to stay out of persisted spec, got %#v", loaded.Spec)
	}
}

func TestFileStoreDeleteMissingKeyIsNoop(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "sessions.json"))

	if err := store.Delete("missing"); err != nil {
		t.Fatalf("delete missing key: %v", err)
	}
}

func TestFileStoreLoadRejectsInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatalf("write invalid JSON: %v", err)
	}
	store := NewFileStore(path)

	if _, err := store.Load("broken"); err == nil {
		t.Fatal("expected invalid JSON to fail")
	}
}
