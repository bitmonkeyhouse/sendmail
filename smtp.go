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

// SMTPMode selects the SMTP transport security policy.
type SMTPMode string

const (
	// SMTPModeSTARTTLS requires verified STARTTLS before authentication or delivery.
	SMTPModeSTARTTLS SMTPMode = "starttls"
	// SMTPModeTLS establishes verified implicit TLS before the SMTP greeting.
	SMTPModeTLS SMTPMode = "tls"
	// SMTPModeDevLoopback permits unauthenticated plaintext only to a loopback TCP peer.
	SMTPModeDevLoopback SMTPMode = "dev-loopback"
)

// SMTPConfig configures an SMTPSender.
type SMTPConfig struct {
	// Mode selects the security policy. The zero value requires STARTTLS.
	Mode SMTPMode
}

// SMTPSender delivers email via a standard SMTP server.
type SMTPSender struct {
	host     string
	port     int
	user     string
	password string
	from     string
	mode     SMTPMode
}

// NewSMTPSender creates an SMTP sender requiring verified STARTTLS and PLAIN auth.
// host is the certificate hostname, port is the port number, user/password are
// required credentials, and from is the default sender address.
// Configuration is validated by Send before dialing.
func NewSMTPSender(host string, port int, user, password, from string) *SMTPSender {
	return NewSMTPSenderWithConfig(host, port, user, password, from, SMTPConfig{})
}

// NewSMTPSenderWithConfig is like NewSMTPSender but selects an explicit security
// mode. Only dev-loopback may omit credentials; configured credentials require
// TLS and advertised AUTH in every mode. Send validates configuration before dialing.
func NewSMTPSenderWithConfig(host string, port int, user, password, from string, config SMTPConfig) *SMTPSender {
	return &SMTPSender{
		host:     host,
		port:     port,
		user:     user,
		password: password,
		from:     from,
		mode:     normalizeSMTPMode(config.Mode),
	}
}

// Send validates SMTP configuration and message headers before dialing.
// Invalid configuration, headers, certificates, and security-policy violations
// wrap ErrPermanent. Delivery is limited to 30 seconds, or the caller's earlier deadline.
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

	mode := normalizeSMTPMode(s.mode)
	if err := validateSMTPConfig(s.host, s.port, s.user, s.password, mode); err != nil {
		return err
	}
	addr := net.JoinHostPort(s.host, fmt.Sprint(s.port))
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
		if errors.Is(sendErr, ErrPermanent) || errors.Is(sendErr, ErrTransient) {
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
	if mode == SMTPModeDevLoopback {
		if err := validateSMTPLoopbackPeer(conn.RemoteAddr()); err != nil {
			return err
		}
	}
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		// The private test config can trust a fixture, but hostname verification
		// always uses the configured SMTP host.
		tlsConfig = tlsConfig.Clone()
	}
	tlsConfig.ServerName = s.host
	// Attach cancellation and deadline to the raw socket before either the TLS
	// handshake or NewClient's greeting read. Keep ownership of that raw socket.
	smtpConn := conn
	if mode == SMTPModeTLS {
		secure := tls.Client(conn, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return err
		}
		smtpConn = secure
	}
	client, err := smtp.NewClient(smtpConn, s.host)
	if err != nil {
		return err
	}
	if err := client.Hello("localhost"); err != nil {
		return err
	}
	if mode != SMTPModeTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsConfig); err != nil {
				return err
			}
		} else if mode == SMTPModeSTARTTLS || auth != nil {
			return fmt.Errorf("%w: smtp: server doesn't support required STARTTLS", ErrPermanent)
		}
	}
	if auth != nil {
		if ok, _ := client.Extension("AUTH"); !ok {
			return fmt.Errorf("%w: smtp: server doesn't support AUTH after TLS", ErrPermanent)
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
	if errors.Is(err, ErrPermanent) || errors.Is(err, ErrTransient) {
		return err
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return fmt.Errorf("%w: SMTP certificate verification: %w", ErrPermanent, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrTransient, err)
	}
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) {
		if tpErr.Code >= 500 {
			return fmt.Errorf("%w: %v", ErrPermanent, err)
		}
	}
	return fmt.Errorf("%w: %v", ErrTransient, err)
}

func normalizeSMTPMode(mode SMTPMode) SMTPMode {
	mode = SMTPMode(strings.ToLower(strings.TrimSpace(string(mode))))
	if mode == "" {
		return SMTPModeSTARTTLS
	}
	return mode
}

func validateSMTPConfig(host string, port int, user, password string, mode SMTPMode) error {
	switch mode {
	case SMTPModeSTARTTLS, SMTPModeTLS, SMTPModeDevLoopback:
	default:
		return fmt.Errorf("%w: unsupported smtp mode %q", ErrPermanent, mode)
	}
	if isBlank(host) {
		return fmt.Errorf("%w: smtp host is required", ErrPermanent)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("%w: smtp port must be between 1 and 65535", ErrPermanent)
	}
	if mode != SMTPModeDevLoopback || user != "" || password != "" {
		if isBlank(user) || isBlank(password) {
			return fmt.Errorf("%w: smtp user and password are required", ErrPermanent)
		}
	}
	return nil
}

func validateSMTPLoopbackPeer(addr net.Addr) error {
	peer, ok := addr.(*net.TCPAddr)
	if !ok || !peer.IP.IsLoopback() {
		return fmt.Errorf("%w: smtp dev-loopback requires a loopback TCP peer", ErrPermanent)
	}
	return nil
}

func randomBoundary() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", buf[:]), nil
}
