package sendmail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
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
// When credentials are configured, the server must advertise AUTH.
func NewSMTPSender(host string, port int, user, password, from string) *SMTPSender {
	return &SMTPSender{
		host:     host,
		port:     port,
		user:     user,
		password: password,
		from:     from,
	}
}

// Send rejects line breaks in headers and invalid mailbox addresses with ErrPermanent.
// Delivery is limited to 30 seconds, or the caller's earlier deadline.
func (s *SMTPSender) Send(ctx context.Context, msg Message) error {
	return s.send(ctx, msg, 30*time.Second, nil)
}

// A nil TLS config uses hostname verification and the system certificate roots.
func (s *SMTPSender) send(ctx context.Context, msg Message, timeout time.Duration, tlsConfig *tls.Config) (sendErr error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrTransient, err)
	}
	from := msg.From
	if from == "" {
		from = s.from
	}

	headers, from, to, err := prepareSMTPHeaders(from, msg)
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(s.host, fmt.Sprint(s.port))
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
	b.WriteString(headers)
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%s\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(msg.TextBody + "\r\n")
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	b.WriteString(msg.HTMLBody + "\r\n")
	fmt.Fprintf(&b, "--%s--\r\n", boundary)

	// Only translate failed operations: cancellation racing with a successful
	// QUIT must not turn a completed send into a retryable failure.
	defer func() {
		if sendErr == nil {
			return
		}
		if err := ctx.Err(); err != nil {
			sendErr = fmt.Errorf("%w: %w", ErrTransient, err)
			return
		}
		var netErr net.Error
		if errors.As(sendErr, &netErr) && netErr.Timeout() {
			// The socket deadline can fire before the context timer runs.
			sendErr = fmt.Errorf("%w: %w: %v", ErrTransient, context.DeadlineExceeded, sendErr)
			return
		}
		sendErr = classifySMTPError(sendErr)
	}()

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	// NewClient reads the greeting, so both cancellation and the deadline
	// must already apply to the raw connection (also used beneath TLS).
	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		return err
	}
	if err := client.Hello("localhost"); err != nil {
		return err
	}
	if ok, _ := client.Extension("STARTTLS"); ok {
		if tlsConfig == nil {
			tlsConfig = &tls.Config{ServerName: s.host}
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if auth != nil {
		if ok, _ := client.Extension("AUTH"); !ok {
			return errors.New("smtp: server doesn't support AUTH")
		}
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write([]byte(b.String())); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// prepareSMTPHeaders returns serialized headers and bare SMTP envelope addresses.
func prepareSMTPHeaders(from string, msg Message) (string, string, string, error) {
	fields := []struct {
		name  string
		value string
	}{
		{"From", from},
		{"To", msg.To},
		{"Subject", msg.Subject},
		{"Reply-To", msg.ReplyTo},
	}
	var headers strings.Builder
	var envelopeFrom, envelopeTo string
	for _, field := range fields {
		if field.name == "Reply-To" && field.value == "" {
			continue
		}
		if strings.ContainsAny(field.value, "\r\n") {
			return "", "", "", fmt.Errorf("%w: SMTP %s contains a line break", ErrPermanent, field.name)
		}
		value := field.value
		if field.name == "Subject" {
			value = mime.QEncoding.Encode("utf-8", value)
		} else {
			address, err := mail.ParseAddress(value)
			if err != nil {
				return "", "", "", fmt.Errorf("%w: invalid SMTP %s address: %v", ErrPermanent, field.name, err)
			}
			value = address.String()
			switch field.name {
			case "From":
				envelopeFrom = address.Address
			case "To":
				envelopeTo = address.Address
			}
		}
		fmt.Fprintf(&headers, "%s: %s\r\n", field.name, value)
	}
	return headers.String(), envelopeFrom, envelopeTo, nil
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
