package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type StoredSession struct {
	SessionID string    `json:"session_id"`
	Spec      Spec      `json:"spec"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Store interface {
	Load(key string) (*StoredSession, error)
	Save(key string, record StoredSession) error
	Delete(key string) error
}

type FileStore struct {
	path string
	mu   sync.Mutex
}

func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

func (s *FileStore) Load(key string) (*StoredSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	record, ok := records[key]
	if !ok {
		return nil, nil
	}
	copy := record
	return &copy, nil
}

func (s *FileStore) Save(key string, record StoredSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.readAllLocked()
	if err != nil {
		return err
	}
	records[key] = record
	return s.writeAllLocked(records)
}

func (s *FileStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.readAllLocked()
	if err != nil {
		return err
	}
	if _, ok := records[key]; !ok {
		return nil
	}
	delete(records, key)
	return s.writeAllLocked(records)
}

func (s *FileStore) readAllLocked() (map[string]StoredSession, error) {
	if s == nil || s.path == "" {
		return map[string]StoredSession{}, nil
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]StoredSession{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]StoredSession{}, nil
	}

	var records map[string]StoredSession
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	if records == nil {
		records = map[string]StoredSession{}
	}
	return records, nil
}

func (s *FileStore) writeAllLocked(records map[string]StoredSession) error {
	if s == nil || s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.path)
	temp, err := os.CreateTemp(dir, "sessions-*.json")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}()

	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, s.path)
}
