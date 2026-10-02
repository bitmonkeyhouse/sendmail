package sendmail

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

type fakeSMTPMessage struct {
	from string
	to   []string
	body string
}

// startFakeSMTP starts a minimal SMTP server on a random port.
// It returns the port and a channel that will receive the first message processed.
func startFakeSMTP(t *testing.T) (int, <-chan fakeSMTPMessage) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ch := make(chan fakeSMTPMessage, 1)

	go func() {
		defer func() { _ = ln.Close() }()
		conn, err := ln.Accept()
		if err != nil {
			close(ch)
			return
		}
		defer func() { _ = conn.Close() }()
		msg := handleSMTPConn(conn)
		ch <- msg
	}()

	return port, ch
}

func handleSMTPConn(conn net.Conn) fakeSMTPMessage {
	_, _ = fmt.Fprintf(conn, "220 fake SMTP ready\r\n")
	scanner := bufio.NewScanner(conn)
	var dataLines []string
	inData := false
	var fromAddr string
	var toAddrs []string
	var result fakeSMTPMessage

	for scanner.Scan() {
		line := scanner.Text()
		upper := strings.ToUpper(line)

		if inData {
			if line == "." {
				inData = false
				result = fakeSMTPMessage{from: fromAddr, to: toAddrs, body: strings.Join(dataLines, "\n")}
				_, _ = fmt.Fprintf(conn, "250 OK\r\n")
				continue
			}
			dataLines = append(dataLines, line)
			continue
		}

		switch {
		case strings.HasPrefix(upper, "EHLO") || strings.HasPrefix(upper, "HELO"):
			_, _ = fmt.Fprintf(conn, "250-fake\r\n250 AUTH PLAIN\r\n")
		case strings.HasPrefix(upper, "AUTH"):
			_, _ = fmt.Fprintf(conn, "235 Authenticated\r\n")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			fromAddr = extractAngleAddr(line[10:])
			_, _ = fmt.Fprintf(conn, "250 OK\r\n")
		case strings.HasPrefix(upper, "RCPT TO:"):
			toAddrs = append(toAddrs, extractAngleAddr(line[8:]))
			_, _ = fmt.Fprintf(conn, "250 OK\r\n")
		case upper == "DATA":
			_, _ = fmt.Fprintf(conn, "354 Go ahead\r\n")
			inData = true
		case upper == "QUIT":
			_, _ = fmt.Fprintf(conn, "221 Bye\r\n")
			return result
		default:
			_, _ = fmt.Fprintf(conn, "500 Unknown\r\n")
		}
	}
	return result
}

func extractAngleAddr(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<") && strings.Contains(s, ">") {
		return s[1:strings.Index(s, ">")]
	}
	return s
}

func TestSMTPSender_DefaultFrom(t *testing.T) {
	port, msgCh := startFakeSMTP(t)
	sender := NewSMTPSender("127.0.0.1", port, "user", "pass", "noreply@example.com")

	msg := Message{
		To:       "user@example.com",
		Subject:  "Test Subject",
		HTMLBody: "<p>Hello</p>",
		TextBody: "Hello",
		ReplyTo:  "reply@example.com",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sender.Send(ctx, msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case res := <-msgCh:
		if res.from != "noreply@example.com" {
			t.Errorf("expected from=noreply@example.com, got %q", res.from)
		}
		if len(res.to) != 1 || res.to[0] != "user@example.com" {
			t.Errorf("expected to=[user@example.com], got %v", res.to)
		}
		if !strings.Contains(res.body, "Test Subject") {
			t.Error("body should contain subject")
		}
		if !strings.Contains(res.body, "<p>Hello</p>") {
			t.Error("body should contain HTML body")
		}
		if !strings.Contains(res.body, "reply@example.com") {
			t.Error("body should contain Reply-To header")
		}
		if !strings.Contains(res.body, "multipart/alternative") {
			t.Error("body should be multipart/alternative")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fake SMTP server")
	}
}

func TestSMTPSender_ExplicitFrom(t *testing.T) {
	port, msgCh := startFakeSMTP(t)
	sender := NewSMTPSender("127.0.0.1", port, "user", "pass", "noreply@example.com")

	msg := Message{
		To:       "user@example.com",
		From:     "custom@example.com",
		Subject:  "Test",
		HTMLBody: "<p>Hi</p>",
		TextBody: "Hi",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sender.Send(ctx, msg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case res := <-msgCh:
		if res.from != "custom@example.com" {
			t.Errorf("expected from=custom@example.com, got %q", res.from)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}

func TestSMTPSender_InvalidHeaders(t *testing.T) {
	for _, field := range []string{"From", "To", "Subject", "Reply-To", "DefaultFrom"} {
		cases := []struct {
			name  string
			value string
		}{
			{"CR", "valid@example.com\rBcc: injected@example.com"},
			{"LF", "valid@example.com\nBcc: injected@example.com"},
			{"CRLF", "valid@example.com\r\nBcc: injected@example.com"},
			{"header_termination", "valid@example.com\r\n\r\ninjected body"},
		}
		if field != "Subject" {
			cases = append(cases,
				struct{ name, value string }{"malformed", "not-an-address"},
				struct{ name, value string }{"multiple", "a@example.com, b@example.com"},
			)
			if field != "Reply-To" {
				cases = append(cases, struct{ name, value string }{"empty", ""})
			}
		}
		for _, tc := range cases {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				defaultFrom := "default@example.com"
				msg := Message{To: "to@example.com", Subject: "Test", ReplyTo: "reply@example.com"}
				switch field {
				case "From":
					msg.From = tc.value
					// An empty explicit From falls back to the default.
					defaultFrom = tc.value
				case "To":
					msg.To = tc.value
				case "Subject":
					msg.Subject = tc.value
				case "Reply-To":
					msg.ReplyTo = tc.value
				case "DefaultFrom":
					defaultFrom = tc.value
				}
				// Port zero refuses connections, so reaching the network would
				// yield ErrTransient instead of the required validation error.
				sender := NewSMTPSender("127.0.0.1", 0, "", "", defaultFrom)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				err := sender.Send(ctx, msg)
				if !errors.Is(err, ErrPermanent) || errors.Is(err, ErrTransient) {
					t.Fatalf("expected ErrPermanent before dialing, got %v", err)
				}
				wantField := field
				if field == "DefaultFrom" {
					wantField = "From"
				}
				if !strings.Contains(err.Error(), wantField) {
					t.Errorf("error should identify %s, got %v", wantField, err)
				}
			})
		}
	}
}

func TestSMTPSender_EncodedHeaders(t *testing.T) {
	for _, explicitFrom := range []bool{false, true} {
		for _, replyTo := range []string{"", "Réponse <reply@example.com>"} {
			t.Run(fmt.Sprintf("explicitFrom=%t/replyTo=%t", explicitFrom, replyTo != ""), func(t *testing.T) {
				port, msgCh := startFakeSMTP(t)
				from := "Équipe <sender@example.com>"
				defaultFrom := from
				msg := Message{
					To:       "Zoë <to@example.com>",
					Subject:  strings.Repeat("Bienvenue, 世界! ", 10) + "Fin",
					ReplyTo:  replyTo,
					TextBody: "First line\nBcc: body text\nLast line",
					HTMLBody: "<p>First line</p>\n<p>Last line</p>",
				}
				if explicitFrom {
					msg.From = from
					defaultFrom = "invalid\r\ndefault"
				}
				sender := NewSMTPSender("127.0.0.1", port, "", "", defaultFrom)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := sender.Send(ctx, msg); err != nil {
					t.Fatalf("Send: %v", err)
				}
				var res fakeSMTPMessage
				select {
				case res = <-msgCh:
				case <-ctx.Done():
					t.Fatal("timed out waiting for fake SMTP server")
				}
				if res.from != "sender@example.com" || len(res.to) != 1 || res.to[0] != "to@example.com" {
					t.Errorf("unexpected envelope: from=%q to=%v", res.from, res.to)
				}
				parsed, err := mail.ReadMessage(strings.NewReader(res.body))
				if err != nil {
					t.Fatalf("ReadMessage: %v", err)
				}
				for header, value := range map[string]string{"From": from, "To": msg.To, "Reply-To": replyTo} {
					if value == "" {
						if _, present := parsed.Header[header]; present {
							t.Errorf("%s should be omitted", header)
						}
						continue
					}
					addresses, err := parsed.Header.AddressList(header)
					want, wantErr := mail.ParseAddress(value)
					if err != nil || wantErr != nil || len(addresses) != 1 || *addresses[0] != *want {
						t.Errorf("%s did not round-trip: %v (error %v)", header, addresses, err)
					}
					if wantErr == nil && parsed.Header.Get(header) != want.String() {
						t.Errorf("%s should use canonical address encoding, got %q", header, parsed.Header.Get(header))
					}
				}
				if !strings.HasPrefix(parsed.Header.Get("Subject"), "=?utf-8?q?") {
					t.Errorf("Subject should be RFC 2047 encoded, got %q", parsed.Header.Get("Subject"))
				}
				decoded, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
				if err != nil || decoded != msg.Subject {
					t.Errorf("Subject did not round-trip: %q (error %v)", decoded, err)
				}
				if parsed.Header.Get("Bcc") != "" {
					t.Error("body text must not become a Bcc header")
				}
				_, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
				if err != nil {
					t.Fatalf("ParseMediaType: %v", err)
				}
				reader := multipart.NewReader(parsed.Body, params["boundary"])
				for _, want := range []string{msg.TextBody, msg.HTMLBody} {
					part, err := reader.NextPart()
					if err != nil {
						t.Fatalf("NextPart: %v", err)
					}
					body, err := io.ReadAll(part)
					if err != nil || string(body) != want {
						t.Errorf("body changed: %q (error %v)", body, err)
					}
				}
			})
		}
	}
}

func TestSMTPSender_ConnectionRefused(t *testing.T) {
	sender := NewSMTPSender("127.0.0.1", 19999, "user", "pass", "noreply@example.com")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msg := Message{To: "x@example.com", Subject: "Test", HTMLBody: "x", TextBody: "x"}
	err := sender.Send(ctx, msg)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrTransient) {
		t.Fatalf("expected ErrTransient, got: %v", err)
	}
}

func TestSMTPSender_ClassifyPermanentError(t *testing.T) {
	err := classifySMTPError(&textproto.Error{Code: 550, Msg: "mailbox unavailable"})
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("expected ErrPermanent for 550, got: %v", err)
	}
}

func TestSMTPSender_ClassifyTransientError(t *testing.T) {
	err := classifySMTPError(&textproto.Error{Code: 451, Msg: "try again later"})
	if !errors.Is(err, ErrTransient) {
		t.Fatalf("expected ErrTransient for 451, got: %v", err)
	}
	if errors.Is(err, ErrPermanent) {
		t.Fatal("451 should not be ErrPermanent")
	}
}

func TestSMTPSender_ClassifyNetworkError(t *testing.T) {
	err := classifySMTPError(fmt.Errorf("connection refused"))
	if !errors.Is(err, ErrTransient) {
		t.Fatalf("expected ErrTransient for network error, got: %v", err)
	}
}

func TestSMTPSender_ClassifyNoError(t *testing.T) {
	if err := classifySMTPError(nil); err != nil {
		t.Fatalf("expected nil, got: %v", err)
	}
}
