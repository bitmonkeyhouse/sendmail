package sendmail

import (
	"context"
	"errors"
)

// ErrTransient indicates a temporary failure that may succeed on retry
// (network errors, 5xx responses, rate limits).
var ErrTransient = errors.New("transient email error")

// ErrPermanent indicates a failure that will not succeed on retry
// (invalid address, suppressed recipient, malformed payload).
var ErrPermanent = errors.New("permanent email error")

// Message represents a fully-prepared email to be delivered.
type Message struct {
	To       string // required
	From     string // optional; sender substitutes default when empty
	Subject  string // required
	HTMLBody string // required
	TextBody string // required
	ReplyTo  string // optional
}

// Sender delivers a fully-prepared Message to an email provider.
//
// Implementations MUST respect context cancellation. If ctx is cancelled
// (deadline exceeded, caller shutdown, etc.), Send must return promptly with
// an error wrapping ctx.Err() or a classified ErrTransient/ErrPermanent.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}
