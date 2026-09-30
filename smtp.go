package sendmail

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/smtp"
	"net/textproto"
	"strings"
)

// SMTPSender delivers email via a standard SMTP server.
type SMTPSender struct {
	host     string
	port     int
	user     string
	password string
	from     string
}

// NewSMTPSender creates a Sender that uses SMTP with PLAIN auth.
// host is the SMTP server hostname, port is the port number,
// user/password are the SMTP credentials, and from is the default sender address.
func NewSMTPSender(host string, port int, user, password, from string) *SMTPSender {
	return &SMTPSender{
		host:     host,
		port:     port,
		user:     user,
		password: password,
		from:     from,
	}
}

func (s *SMTPSender) Send(ctx context.Context, msg Message) error {
	from := msg.From
	if from == "" {
		from = s.from
	}

	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	// Auth only when credentials are configured. Local dev servers (Mailpit)
	// run AUTH-disabled and reject a PLAIN auth attempt with "server doesn't
	// support AUTH"; nil auth skips the AUTH step entirely.
	var auth smtp.Auth
	if s.user != "" || s.password != "" {
		auth = smtp.PlainAuth("", s.user, s.password, s.host)
	}

	boundary, err := randomBoundary()
	if err != nil {
		return fmt.Errorf("%w: generating boundary: %v", ErrPermanent, err)
	}

	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + msg.To + "\r\n")
	b.WriteString("Subject: " + msg.Subject + "\r\n")
	if msg.ReplyTo != "" {
		b.WriteString("Reply-To: " + msg.ReplyTo + "\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%s\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(msg.TextBody + "\r\n")
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	b.WriteString(msg.HTMLBody + "\r\n")
	fmt.Fprintf(&b, "--%s--\r\n", boundary)

	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(addr, auth, from, []string{msg.To}, []byte(b.String()))
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrTransient, ctx.Err())
	case err := <-done:
		return classifySMTPError(err)
	}
}

// classifySMTPError maps net/smtp errors to ErrTransient or ErrPermanent.
func classifySMTPError(err error) error {
	if err == nil {
		return nil
	}
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) {
		if tpErr.Code >= 500 {
			return fmt.Errorf("%w: %v", ErrPermanent, err)
		}
	}
	return fmt.Errorf("%w: %v", ErrTransient, err)
}

func randomBoundary() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", buf[:]), nil
}
