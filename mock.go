package sendmail

import (
	"context"
	"sync"
)

// MockSender is a test double for Sender that captures messages in memory.
// The zero value is ready to use and behaves like NewMockSender. All methods
// are safe for concurrent use. Send ignores the context; the order in which
// concurrent Sends are recorded is unspecified.
type MockSender struct {
	mu   sync.Mutex
	sent []Message

	// injectErr is returned for the next injectCount calls to Send, after which
	// it is cleared. Use SetError to configure it.
	injectErr   error
	injectCount int
}

// NewMockSender creates a MockSender ready for use in tests.
func NewMockSender() *MockSender {
	return &MockSender{}
}

// Send appends a copy of msg to the captured messages and returns nil, unless an
// error configured with SetError is still pending, in which case it returns
// that error instead. The context is ignored.
func (m *MockSender) Send(_ context.Context, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.injectErr != nil && m.injectCount > 0 {
		err := m.injectErr
		m.injectCount--
		if m.injectCount == 0 {
			m.injectErr = nil
		}
		return err
	}

	m.sent = append(m.sent, msg)
	return nil
}

// Sent returns a copy of the captured messages, so callers may retain or modify
// the slice without affecting the mock.
func (m *MockSender) Sent() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Message, len(m.sent))
	copy(out, m.sent)
	return out
}

// SetError configures the mock to return err from the next n calls to Send,
// after which it reverts to recording messages. A nil err or n less than 1
// disables the pending error.
func (m *MockSender) SetError(err error, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.injectErr = err
	m.injectCount = n
}
