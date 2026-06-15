package email

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestMockSender_CapturesMessages(t *testing.T) {
	m := NewMockSender()

	msg1 := Message{To: "a@example.com", Subject: "First", HTMLBody: "<p>1</p>", TextBody: "1"}
	msg2 := Message{To: "b@example.com", Subject: "Second", HTMLBody: "<p>2</p>", TextBody: "2"}

	if err := m.Send(context.Background(), msg1); err != nil {
		t.Fatalf("send msg1: %v", err)
	}
	if err := m.Send(context.Background(), msg2); err != nil {
		t.Fatalf("send msg2: %v", err)
	}

	sent := m.Sent()
	if len(sent) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(sent))
	}
	if sent[0].To != "a@example.com" {
		t.Fatalf("expected first message to a@example.com, got %q", sent[0].To)
	}
	if sent[1].To != "b@example.com" {
		t.Fatalf("expected second message to b@example.com, got %q", sent[1].To)
	}
}

func TestMockSender_ErrorInjection(t *testing.T) {
	testErr := errors.New("injected failure")
	m := NewMockSender()
	m.SetError(testErr, 2)

	msg := Message{To: "a@example.com", Subject: "Test", HTMLBody: "<p>1</p>", TextBody: "1"}

	// First two calls should fail.
	err := m.Send(context.Background(), msg)
	if !errors.Is(err, testErr) {
		t.Fatalf("expected injected error, got %v", err)
	}

	err = m.Send(context.Background(), msg)
	if !errors.Is(err, testErr) {
		t.Fatalf("expected injected error, got %v", err)
	}

	// Third call should succeed (injection exhausted).
	err = m.Send(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected third send error: %v", err)
	}

	sent := m.Sent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 captured message, got %d", len(sent))
	}
}

func TestMockSender_ConcurrentSends(t *testing.T) {
	m := NewMockSender()
	var wg sync.WaitGroup

	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg := Message{
				To:       "test@example.com",
				Subject:  "concurrent",
				HTMLBody: "<p>hello</p>",
				TextBody: "hello",
			}
			_ = m.Send(context.Background(), msg)
		}(i)
	}

	wg.Wait()

	sent := m.Sent()
	if len(sent) != 100 {
		t.Fatalf("expected 100 messages, got %d", len(sent))
	}
}

func TestMockSender_SentReturnsCopy(t *testing.T) {
	m := NewMockSender()
	msg := Message{To: "a@example.com", Subject: "Test", HTMLBody: "<p>1</p>", TextBody: "1"}
	if err := m.Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	sent := m.Sent()
	sent[0].To = "modified@example.com"

	// Original should be unchanged.
	original := m.Sent()
	if original[0].To != "a@example.com" {
		t.Fatalf("expected original to remain unchanged, got %q", original[0].To)
	}
}
