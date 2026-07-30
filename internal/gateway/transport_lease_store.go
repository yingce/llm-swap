package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const transportLeaseStoreVersion = 1

// TransportLeaseStore persists the complete lease set. Implementations must
// replace their previous snapshot only when Save succeeds.
type TransportLeaseStore interface {
	Load() ([]TransportLease, error)
	Save([]TransportLease) error
}

type memoryTransportLeaseStore struct {
	mu     sync.Mutex
	leases []TransportLease
}

func NewMemoryTransportLeaseStore() TransportLeaseStore {
	return &memoryTransportLeaseStore{}
}

func (s *memoryTransportLeaseStore) Load() ([]TransportLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneTransportLeases(s.leases), nil
}

func (s *memoryTransportLeaseStore) Save(leases []TransportLease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leases = cloneTransportLeases(leases)
	return nil
}

type fileTransportLeaseStore struct {
	path string
}

type transportLeaseStoreDocument struct {
	Version int              `json:"version"`
	Leases  []TransportLease `json:"leases"`
}

func NewFileTransportLeaseStore(path string) TransportLeaseStore {
	return &fileTransportLeaseStore{path: path}
}

func (s *fileTransportLeaseStore) Load() ([]TransportLease, error) {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open transport lease store: %w", err)
	}
	defer file.Close()

	var document transportLeaseStoreDocument
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode transport lease store: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("decode transport lease store: trailing JSON value")
		}
		return nil, fmt.Errorf("decode transport lease store: %w", err)
	}
	if document.Version != transportLeaseStoreVersion {
		return nil, fmt.Errorf("unsupported transport lease store version %d", document.Version)
	}
	return cloneTransportLeases(document.Leases), nil
}

func (s *fileTransportLeaseStore) Save(leases []TransportLease) (resultErr error) {
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create transport lease store directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".transport-leases-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary transport lease store: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if resultErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary transport lease store: %w", err)
	}
	document := transportLeaseStoreDocument{Version: transportLeaseStoreVersion, Leases: cloneTransportLeases(leases)}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("encode transport lease store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary transport lease store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary transport lease store: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace transport lease store: %w", err)
	}
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}

func cloneTransportLeases(leases []TransportLease) []TransportLease {
	if leases == nil {
		return nil
	}
	cloned := make([]TransportLease, len(leases))
	copy(cloned, leases)
	return cloned
}
