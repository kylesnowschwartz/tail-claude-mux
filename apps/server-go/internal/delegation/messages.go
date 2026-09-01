package delegation

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const maxMessageTextLen = 64 * 1024

// Message is one item in a delegation's message log.
type Message struct {
	ID          string     `json:"id"`
	TS          time.Time  `json:"ts"`
	From        string     `json:"from"`
	Kind        string     `json:"kind"`
	Text        string     `json:"text"`
	ReplyTo     string     `json:"reply_to,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

// AppendMessage adds msg to the delegation's message log.
func (s *Store) AppendMessage(delegationID string, msg Message) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.read(delegationID); err != nil {
		return Message{}, err
	}
	if err := validateMessage(msg); err != nil {
		return Message{}, err
	}

	id, err := newUUID()
	if err != nil {
		return Message{}, fmt.Errorf("mint message ID: %w", err)
	}
	msg.ID = id
	msg.TS = s.now().UTC()
	msg.DeliveredAt = nil

	messages, err := s.readMessages(delegationID)
	if err != nil {
		return Message{}, err
	}
	messages = append(messages, msg)
	if err := s.writeMessages(delegationID, messages); err != nil {
		return Message{}, err
	}
	return msg, nil
}

// Messages returns a delegation's messages in file order.
func (s *Store) Messages(delegationID string) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readMessages(delegationID)
}

// UndeliveredFromDelegate returns delegate messages that have not been handed to the orchestrator.
func (s *Store) UndeliveredFromDelegate(delegationID string) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	messages, err := s.readMessages(delegationID)
	if err != nil {
		return nil, err
	}
	undelivered := make([]Message, 0)
	for _, msg := range messages {
		if msg.From == "delegate" && msg.DeliveredAt == nil {
			undelivered = append(undelivered, msg)
		}
	}
	return undelivered, nil
}

// MarkDelivered records which delegate messages have been handed to the orchestrator.
func (s *Store) MarkDelivered(delegationID string, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	messages, err := s.readMessages(delegationID)
	if err != nil {
		return err
	}
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	deliveredAt := s.now().UTC()
	changed := false
	for i := range messages {
		if _, ok := wanted[messages[i].ID]; !ok || messages[i].From != "delegate" || messages[i].DeliveredAt != nil {
			continue
		}
		messages[i].DeliveredAt = &deliveredAt
		changed = true
	}
	if !changed {
		return nil
	}
	return s.writeMessages(delegationID, messages)
}

// AnswerFor returns the answer paired with askID.
func (s *Store) AnswerFor(delegationID, askID string) (Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	messages, err := s.readMessages(delegationID)
	if err != nil {
		return Message{}, false, err
	}
	for _, msg := range messages {
		if msg.From == "orchestrator" && msg.Kind == "answer" && msg.ReplyTo == askID {
			return msg, true, nil
		}
	}
	return Message{}, false, nil
}

// UnansweredAsks returns delegate asks that have no paired answer.
func (s *Store) UnansweredAsks(delegationID string) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	messages, err := s.readMessages(delegationID)
	if err != nil {
		return nil, err
	}
	answered := make(map[string]struct{})
	for _, msg := range messages {
		if msg.From == "orchestrator" && msg.Kind == "answer" {
			answered[msg.ReplyTo] = struct{}{}
		}
	}
	unanswered := make([]Message, 0)
	for _, msg := range messages {
		if msg.From != "delegate" || msg.Kind != "ask" {
			continue
		}
		if _, ok := answered[msg.ID]; !ok {
			unanswered = append(unanswered, msg)
		}
	}
	return unanswered, nil
}

func validateMessage(msg Message) error {
	if len(msg.Text) == 0 {
		return errors.New("message text is empty")
	}
	if len(msg.Text) > maxMessageTextLen {
		return fmt.Errorf("message text exceeds %d bytes", maxMessageTextLen)
	}
	switch {
	case msg.From == "delegate" && (msg.Kind == "note" || msg.Kind == "ask"):
		if msg.ReplyTo != "" {
			return errors.New("delegate messages cannot reply to another message")
		}
	case msg.From == "orchestrator" && msg.Kind == "answer":
		if msg.ReplyTo == "" {
			return errors.New("answer reply_to is empty")
		}
	default:
		return fmt.Errorf("invalid message from/kind combination %q/%q", msg.From, msg.Kind)
	}
	return nil
}

func (s *Store) readMessages(delegationID string) ([]Message, error) {
	if !validUUID(delegationID) {
		return nil, ErrNotFound
	}
	file, err := os.Open(s.messagesPath(delegationID))
	if errors.Is(err, os.ErrNotExist) {
		return []Message{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read delegation messages %s: %w", delegationID, err)
	}
	defer file.Close()

	messages := make([]Message, 0)
	reader := bufio.NewReader(file)
	for lineNumber := 1; ; lineNumber++ {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var msg Message
			if err := json.Unmarshal(line, &msg); err != nil {
				return nil, fmt.Errorf("decode delegation messages %s line %d: %w", delegationID, lineNumber, err)
			}
			messages = append(messages, msg)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read delegation messages %s: %w", delegationID, readErr)
		}
	}
	return messages, nil
}

func (s *Store) writeMessages(delegationID string, messages []Message) error {
	dir := filepath.Join(s.dir, "messages")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create delegation message store %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".messages-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary delegation messages file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("set temporary delegation messages permissions: %w", err)
	}
	encoder := json.NewEncoder(tmp)
	for _, msg := range messages {
		if err := encoder.Encode(msg); err != nil {
			tmp.Close()
			return fmt.Errorf("encode delegation messages %s: %w", delegationID, err)
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary delegation messages: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary delegation messages: %w", err)
	}
	if err := os.Rename(tmpName, s.messagesPath(delegationID)); err != nil {
		return fmt.Errorf("replace delegation messages %s: %w", delegationID, err)
	}
	return nil
}

func (s *Store) messagesPath(delegationID string) string {
	return filepath.Join(s.dir, "messages", delegationID+".jsonl")
}
