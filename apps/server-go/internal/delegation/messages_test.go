package delegation

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMessageStoreRoundTripAndDelivery(t *testing.T) {
	createdAt := time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC)
	store := newTestStore(t, createdAt)
	record := createTestRecord(t, store, CreateParams{Agent: "codex"})

	note, err := store.AppendMessage(record.DelegationID, Message{From: "delegate", Kind: "note", Text: "review complete"})
	if err != nil {
		t.Fatalf("AppendMessage(note) error = %v", err)
	}
	if !validUUID(note.ID) || !note.TS.Equal(createdAt) || note.DeliveredAt != nil {
		t.Fatalf("note identity = %#v", note)
	}
	store.now = func() time.Time { return createdAt.Add(time.Minute) }
	ask, err := store.AppendMessage(record.DelegationID, Message{From: "delegate", Kind: "ask", Text: "which option?"})
	if err != nil {
		t.Fatalf("AppendMessage(ask) error = %v", err)
	}

	messages, err := store.Messages(record.DelegationID)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(messages) != 2 || messages[0].ID != note.ID || messages[1].ID != ask.ID {
		t.Fatalf("Messages() = %#v, want note then ask", messages)
	}
	undelivered, err := store.UndeliveredFromDelegate(record.DelegationID)
	if err != nil || len(undelivered) != 2 {
		t.Fatalf("UndeliveredFromDelegate() = (%#v, %v), want two messages", undelivered, err)
	}

	deliveredAt := createdAt.Add(2 * time.Minute)
	store.now = func() time.Time { return deliveredAt }
	if err := store.MarkDelivered(record.DelegationID, []string{note.ID}); err != nil {
		t.Fatalf("MarkDelivered() error = %v", err)
	}
	undelivered, err = store.UndeliveredFromDelegate(record.DelegationID)
	if err != nil || len(undelivered) != 1 || undelivered[0].ID != ask.ID {
		t.Fatalf("UndeliveredFromDelegate() = (%#v, %v), want ask", undelivered, err)
	}
	messages, err = store.Messages(record.DelegationID)
	if err != nil {
		t.Fatal(err)
	}
	if messages[0].DeliveredAt == nil || !messages[0].DeliveredAt.Equal(deliveredAt) {
		t.Fatalf("note DeliveredAt = %v, want %v", messages[0].DeliveredAt, deliveredAt)
	}
	if messages[1].DeliveredAt != nil {
		t.Fatalf("ask DeliveredAt = %v, want nil", messages[1].DeliveredAt)
	}

	raw, err := os.ReadFile(store.messagesPath(record.DelegationID))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(raw), "\n"); lines != 2 {
		t.Fatalf("message log newline count = %d, want 2", lines)
	}
}

func TestMessageStoreAnswerPairing(t *testing.T) {
	store := newTestStore(t, time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC))
	record := createTestRecord(t, store, CreateParams{Agent: "codex"})
	ask, err := store.AppendMessage(record.DelegationID, Message{From: "delegate", Kind: "ask", Text: "continue?"})
	if err != nil {
		t.Fatal(err)
	}
	if answer, ok, err := store.AnswerFor(record.DelegationID, ask.ID); err != nil || ok {
		t.Fatalf("AnswerFor() before answer = (%#v, %v, %v), want no answer", answer, ok, err)
	}
	answer, err := store.AppendMessage(record.DelegationID, Message{
		From:    "orchestrator",
		Kind:    "answer",
		Text:    "continue",
		ReplyTo: ask.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.AnswerFor(record.DelegationID, ask.ID)
	if err != nil || !ok || got.ID != answer.ID {
		t.Fatalf("AnswerFor() = (%#v, %v, %v), want %#v", got, ok, err, answer)
	}
	if err := store.MarkDelivered(record.DelegationID, []string{answer.ID}); err != nil {
		t.Fatal(err)
	}
	got, ok, err = store.AnswerFor(record.DelegationID, ask.ID)
	if err != nil || !ok || got.DeliveredAt != nil {
		t.Fatalf("answer delivery = (%#v, %v, %v), want nil DeliveredAt", got, ok, err)
	}
}

func TestMessageStoreUnansweredAsks(t *testing.T) {
	store := newTestStore(t, time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC))
	record := createTestRecord(t, store, CreateParams{Agent: "codex"})
	answeredAsk, err := store.AppendMessage(record.DelegationID, Message{From: "delegate", Kind: "ask", Text: "first?"})
	if err != nil {
		t.Fatal(err)
	}
	unansweredAsk, err := store.AppendMessage(record.DelegationID, Message{From: "delegate", Kind: "ask", Text: "second?"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(record.DelegationID, Message{From: "delegate", Kind: "note", Text: "context"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendMessage(record.DelegationID, Message{From: "orchestrator", Kind: "answer", Text: "yes", ReplyTo: answeredAsk.ID}); err != nil {
		t.Fatal(err)
	}

	asks, err := store.UnansweredAsks(record.DelegationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(asks) != 1 || asks[0].ID != unansweredAsk.ID {
		t.Fatalf("UnansweredAsks() = %#v, want %#v", asks, unansweredAsk)
	}
}

func TestAppendMessageValidation(t *testing.T) {
	store := newTestStore(t, time.Now().UTC())
	record := createTestRecord(t, store, CreateParams{Agent: "codex"})

	tests := []struct {
		name string
		msg  Message
	}{
		{name: "empty text", msg: Message{From: "delegate", Kind: "note"}},
		{name: "oversized text", msg: Message{From: "delegate", Kind: "note", Text: strings.Repeat("x", maxMessageTextLen+1)}},
		{name: "delegate answer", msg: Message{From: "delegate", Kind: "answer", Text: "yes", ReplyTo: "ask"}},
		{name: "orchestrator note", msg: Message{From: "orchestrator", Kind: "note", Text: "hello"}},
		{name: "answer without reply", msg: Message{From: "orchestrator", Kind: "answer", Text: "yes"}},
		{name: "delegate reply", msg: Message{From: "delegate", Kind: "note", Text: "hello", ReplyTo: "ask"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.AppendMessage(record.DelegationID, test.msg); err == nil {
				t.Fatal("AppendMessage() error = nil, want validation error")
			}
		})
	}

	missing := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := store.AppendMessage(missing, Message{From: "delegate", Kind: "note", Text: "hello"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AppendMessage(unknown) error = %v, want ErrNotFound", err)
	}
	if messages, err := store.Messages(record.DelegationID); err != nil || len(messages) != 0 {
		t.Fatalf("Messages() after rejected appends = (%#v, %v), want empty", messages, err)
	}
}

func TestMessagesMissingFileIsEmpty(t *testing.T) {
	store := newTestStore(t, time.Now().UTC())
	record := createTestRecord(t, store, CreateParams{Agent: "codex"})
	messages, err := store.Messages(record.DelegationID)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if messages == nil || len(messages) != 0 {
		t.Fatalf("Messages() = %#v, want non-nil empty slice", messages)
	}
}
