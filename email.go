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
//
// The senders pass these fields through without validating required content:
// Resend and Mailgun send whatever is provided and let the provider reject it,
// while SMTP additionally parses the To, From, and Reply-To addresses and rejects
// header line breaks.
type Message struct {
	To       string // recipient address
	From     string // sender address; production senders use their default when empty
	Subject  string // subject line
	HTMLBody string // HTML body, sent as the text/html part where supported
	TextBody string // plain-text body
	ReplyTo  string // optional reply-to address
}

// Sender delivers a fully-prepared Message.
type Sender interface {
	// Send delivers msg.
	//
	// Production implementations must respect context cancellation and deadlines.
	// SMTP cancellation errors wrap ctx.Err(); HTTP transport errors wrap
	// ErrTransient but may not wrap ctx.Err(). Production delivery failures wrap
	// ErrTransient if retryable or ErrPermanent otherwise; callers can test with
	// errors.Is. MockSender ignores ctx and returns injected errors unchanged.
	Send(ctx context.Context, msg Message) error
}
