// Package delegation persists the identity of coding-agent delegations.
package delegation

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound      = errors.New("delegation not found")
	ErrAgentMismatch = errors.New("delegation agent mismatch")
)

// Record identifies one coding-agent delegation.
type Record struct {
	DelegationID string     `json:"delegation_id"`
	Agent        string     `json:"agent"`
	Cwd          string     `json:"cwd"`
	OwnerSession string     `json:"owner_session"`
	SessionName  string     `json:"session_name"`
	PaneID       string     `json:"pane_id"`
	WindowID     string     `json:"window_id"`
	CreatedAt    time.Time  `json:"created_at"`
	ThreadID     string     `json:"thread_id"`
	BoundAt      *time.Time `json:"bound_at"`
}

// CreateParams contains the identity known when a delegation is spawned.
type CreateParams struct {
	Agent        string
	Cwd          string
	OwnerSession string
	SessionName  string
	PaneID       string
	WindowID     string
}

// NewID mints a delegation ID without persisting a record.
func NewID() (string, error) {
	return newUUID()
}

// Store persists each record as a separate JSON file in dir.
type Store struct {
	dir string
	mu  sync.Mutex
	now func() time.Time
}

// New creates dir when needed and opens a delegation store there.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create delegation store %s: %w", dir, err)
	}
	return &Store{dir: dir, now: time.Now}, nil
}

// Create mints and persists a new delegation record.
func (s *Store) Create(params CreateParams) (Record, error) {
	id, err := NewID()
	if err != nil {
		return Record{}, fmt.Errorf("mint delegation ID: %w", err)
	}
	return s.CreateWithID(id, params)
}

// CreateWithID persists a new delegation record with a previously minted ID.
func (s *Store) CreateWithID(delegationID string, params CreateParams) (Record, error) {
	if !validUUID(delegationID) {
		return Record{}, errors.New("delegation ID is not a UUID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	record := Record{
		DelegationID: delegationID,
		Agent:        params.Agent,
		Cwd:          params.Cwd,
		OwnerSession: params.OwnerSession,
		SessionName:  params.SessionName,
		PaneID:       params.PaneID,
		WindowID:     params.WindowID,
		CreatedAt:    s.now().UTC(),
	}
	if err := s.write(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Get returns the record with delegationID.
func (s *Store) Get(delegationID string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(delegationID)
}

// Bind associates an unbound delegation with its agent thread.
func (s *Store) Bind(delegationID, agent, threadID string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, err := s.read(delegationID)
	if err != nil {
		return Record{}, err
	}
	if record.Agent != agent {
		return Record{}, fmt.Errorf("%w: record has %q, bind requested %q", ErrAgentMismatch, record.Agent, agent)
	}
	if record.ThreadID != "" {
		if record.ThreadID == threadID {
			return record, nil
		}
		return Record{}, fmt.Errorf("delegation %s is already bound to thread %s", delegationID, record.ThreadID)
	}
	if threadID == "" {
		return Record{}, errors.New("thread ID is empty")
	}
	boundAt := s.now().UTC()
	record.ThreadID = threadID
	record.BoundAt = &boundAt
	if err := s.write(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

// FindUnboundByPane returns the newest unbound record for a tmux pane.
func (s *Store) FindUnboundByPane(sessionName, paneID string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.list()
	if err != nil {
		return Record{}, err
	}
	for i := len(records) - 1; i >= 0; i-- {
		record := records[i]
		if record.SessionName == sessionName && record.PaneID == paneID && record.ThreadID == "" {
			return record, nil
		}
	}
	return Record{}, ErrNotFound
}

// List returns all records ordered by creation time and delegation ID.
func (s *Store) List() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list()
}

// Sweep removes records strictly older than maxAge and returns the number removed.
func (s *Store) Sweep(maxAge time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.list()
	if err != nil {
		return 0, err
	}
	cutoff := s.now().UTC().Add(-maxAge)
	removed := 0
	for _, record := range records {
		if !record.CreatedAt.Before(cutoff) {
			continue
		}
		if err := os.Remove(s.path(record.DelegationID)); err != nil {
			return removed, fmt.Errorf("remove delegation %s: %w", record.DelegationID, err)
		}
		removed++
	}
	return removed, nil
}

func (s *Store) list() ([]Record, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read delegation store %s: %w", s.dir, err)
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		record, err := s.read(id)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	slices.SortFunc(records, func(a, b Record) int {
		if order := a.CreatedAt.Compare(b.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.DelegationID, b.DelegationID)
	})
	return records, nil
}

func (s *Store) read(delegationID string) (Record, error) {
	if !validUUID(delegationID) {
		return Record{}, ErrNotFound
	}
	raw, err := os.ReadFile(s.path(delegationID))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("read delegation %s: %w", delegationID, err)
	}
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return Record{}, fmt.Errorf("decode delegation %s: %w", delegationID, err)
	}
	if record.DelegationID != delegationID {
		return Record{}, fmt.Errorf("decode delegation %s: record ID is %q", delegationID, record.DelegationID)
	}
	return record, nil
}

func (s *Store) write(record Record) error {
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode delegation %s: %w", record.DelegationID, err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(s.dir, ".delegation-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary delegation file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("set temporary delegation permissions: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary delegation: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary delegation: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary delegation: %w", err)
	}
	if err := os.Rename(tmpName, s.path(record.DelegationID)); err != nil {
		return fmt.Errorf("replace delegation %s: %w", record.DelegationID, err)
	}
	return nil
}

func (s *Store) path(delegationID string) string {
	return filepath.Join(s.dir, delegationID+".json")
}

func newUUID() (string, error) {
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		return "", err
	}
	uuid[6] = uuid[6]&0x0f | 0x40
	uuid[8] = uuid[8]&0x3f | 0x80
	var encoded [36]byte
	hex.Encode(encoded[0:8], uuid[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], uuid[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], uuid[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], uuid[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], uuid[10:16])
	return string(encoded[:]), nil
}

func validUUID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	for i, char := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}
