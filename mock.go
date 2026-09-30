package sendmail

import (
	"context"
	"sync"
)

// MockSender is a test double for Sender that captures messages in memory.
type MockSender struct {
	mu   sync.Mutex
	sent []Message

	// InjectErr is the error to return for the next N calls to Send.
	// Set this before calling Send to simulate failures.
	injectErr   error
	injectCount int
}

// NewMockSender creates a MockSender ready for use in tests.
func NewMockSender() *MockSender {
	return &MockSender{}
}

// Send records the message and returns nil, unless an error has been injected.
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

// Sent returns a copy of all messages captured so far.
func (m *MockSender) Sent() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Message, len(m.sent))
	copy(out, m.sent)
	return out
}

// SetError configures the mock to return err for the next n calls to Send.
func (m *MockSender) SetError(err error, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.injectErr = err
	m.injectCount = n
}
