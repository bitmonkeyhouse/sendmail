package email

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
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
