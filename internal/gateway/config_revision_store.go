package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
)

const (
	configRevisionStoreVersion       = 1
	DefaultGatewayConfigRevisionPath = "/opt/llmswap/state/config-revision.json"
)

// ConfigRevisionStore allocates revisions that are strictly increasing across
// every caller sharing the same backing store.
type ConfigRevisionStore interface {
	Allocate(context.Context) (int64, error)
}

type memoryConfigRevisionStore struct {
	mu       sync.Mutex
	revision int64
}

func NewMemoryConfigRevisionStore() ConfigRevisionStore {
	return &memoryConfigRevisionStore{}
}

func (s *memoryConfigRevisionStore) Allocate(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revision == math.MaxInt64 {
		return 0, fmt.Errorf("configuration revision exhausted")
	}
	s.revision++
	return s.revision, nil
}

type fileConfigRevisionStore struct {
	mu   sync.Mutex
	path string
}

type configRevisionStoreDocument struct {
	Version  int   `json:"version"`
	Revision int64 `json:"revision"`
}

func NewFileConfigRevisionStore(path string) ConfigRevisionStore {
	return &fileConfigRevisionStore{path: path}
}

func (s *fileConfigRevisionStore) Allocate(ctx context.Context) (revision int64, resultErr error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return 0, fmt.Errorf("create configuration revision store directory: %w", err)
	}
	lockFile, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open configuration revision store lock: %w", err)
	}
	if err := lockConfigRevisionFileContext(ctx, lockFile); err != nil {
		_ = lockFile.Close()
		return 0, fmt.Errorf("lock configuration revision store: %w", err)
	}
	defer func() {
		if err := unlockConfigRevisionFile(lockFile); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("unlock configuration revision store: %w", err))
		}
		if err := lockFile.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close configuration revision store lock: %w", err))
		}
	}()

	current, err := s.load()
	if err != nil {
		return 0, err
	}
	if current == math.MaxInt64 {
		return 0, fmt.Errorf("configuration revision exhausted")
	}
	next := current + 1
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := s.save(next); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *fileConfigRevisionStore) load() (int64, error) {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open configuration revision store: %w", err)
	}
	defer file.Close()

	var document configRevisionStoreDocument
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&document); err != nil {
		return 0, fmt.Errorf("decode configuration revision store: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return 0, fmt.Errorf("decode configuration revision store: trailing JSON value")
		}
		return 0, fmt.Errorf("decode configuration revision store: %w", err)
	}
	if document.Version != configRevisionStoreVersion {
		return 0, fmt.Errorf("unsupported configuration revision store version %d", document.Version)
	}
	if document.Revision < 0 {
		return 0, fmt.Errorf("invalid configuration revision %d", document.Revision)
	}
	return document.Revision, nil
}

func (s *fileConfigRevisionStore) save(revision int64) (resultErr error) {
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create configuration revision store directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".config-revision-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary configuration revision store: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if resultErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary configuration revision store: %w", err)
	}
	document := configRevisionStoreDocument{Version: configRevisionStoreVersion, Revision: revision}
	if err := json.NewEncoder(temporary).Encode(document); err != nil {
		return fmt.Errorf("encode configuration revision store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary configuration revision store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary configuration revision store: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace configuration revision store: %w", err)
	}
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}
