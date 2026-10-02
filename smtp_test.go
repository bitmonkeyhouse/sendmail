package sendmail

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http/httptest"
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
	ch := make(chan fakeSMTPMessage, 1)
	port, _ := startSMTPPeer(t, func(conn net.Conn) error {
		ch <- handleSMTPConn(conn)
		return nil
	})
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
	sender := NewSMTPSenderWithConfig("127.0.0.1", port, "", "", "noreply@example.com", SMTPConfig{Mode: SMTPModeDevLoopback})

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
	sender := NewSMTPSenderWithConfig("127.0.0.1", port, "", "", "noreply@example.com", SMTPConfig{Mode: SMTPModeDevLoopback})

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
				// Port zero prevents connections if header validation regresses;
				// the error below must identify the header, not SMTP configuration.
				sender := NewSMTPSenderWithConfig("127.0.0.1", 0, "", "", defaultFrom, SMTPConfig{Mode: SMTPModeDevLoopback})
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
				sender := NewSMTPSenderWithConfig("127.0.0.1", port, "", "", defaultFrom, SMTPConfig{Mode: SMTPModeDevLoopback})
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

// startSMTPPeer owns both sides of server cleanup, even if Send leaks a socket.
func startSMTPPeer(t *testing.T, serve func(net.Conn) error) (int, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return startSMTPPeerListener(t, ln, serve)
}

func startSMTPPeerListener(t *testing.T, ln net.Listener, serve func(net.Conn) error) (int, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		conn, err := ln.Accept()
		if err != nil {
			result <- err
			return
		}
		defer func() { _ = conn.Close() }()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			result <- err
			return
		}
		result <- serve(conn)
	}()
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			t.Error("SMTP peer did not stop")
		}
	})
	return ln.Addr().(*net.TCPAddr).Port, result
}

func TestSMTPSender_DevHelloFallback(t *testing.T) {
	for _, tc := range []struct {
		helo        bool
		credentials bool
	}{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("HELO=%t/credentials=%t", tc.helo, tc.credentials), func(t *testing.T) {
			port, peerResult := startSMTPPeer(t, func(conn net.Conn) error {
				peer := textproto.NewConn(conn)
				if err := peer.PrintfLine("220 ready"); err != nil {
					return err
				}
				type exchange struct{ command, reply string }
				commands := []exchange{{"EHLO ", "250 peer"}}
				if tc.helo {
					commands[0].reply = "500 EHLO unsupported"
					commands = append(commands, exchange{"HELO ", "250 peer"})
				}
				commands = append(commands,
					exchange{"MAIL FROM:", "250 OK"},
					exchange{"RCPT TO:", "250 OK"},
					exchange{"DATA", "354 send data"},
					exchange{"QUIT", "221 bye"},
				)
				for _, step := range commands {
					line, err := peer.ReadLine()
					if tc.credentials && step.command == "MAIL FROM:" {
						if err != io.EOF {
							return fmt.Errorf("expected closure without AUTH, got %q: %v", line, err)
						}
						return nil
					}
					if err != nil || !strings.HasPrefix(line, step.command) {
						return fmt.Errorf("expected %s, got %q: %v", step.command, line, err)
					}
					if err := peer.PrintfLine("%s", step.reply); err != nil {
						return err
					}
					if step.command == "DATA" {
						if _, err := peer.ReadDotBytes(); err != nil {
							return err
						}
						if err := peer.PrintfLine("250 accepted"); err != nil {
							return err
						}
					}
				}
				return nil
			})
			user, password := "", ""
			if tc.credentials {
				user, password = "user", "pass"
			}
			sender := NewSMTPSenderWithConfig("127.0.0.1", port, user, password, "from@example.com", SMTPConfig{Mode: SMTPModeDevLoopback})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := sender.Send(ctx, Message{To: "to@example.com"})
			if tc.credentials {
				if !errors.Is(err, ErrPermanent) || !strings.Contains(err.Error(), "required STARTTLS") {
					t.Fatalf("expected missing STARTTLS error, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-peerResult:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("peer did not finish")
			}
		})
	}
}

func TestSMTPSender_STARTTLSVerifiesCertificate(t *testing.T) {
	// Reuse the standard library's self-signed test certificate; production
	// verification must reject it rather than proceed to MAIL or AUTH.
	fixture := httptest.NewTLSServer(nil)
	defer fixture.Close()
	port, peerResult := startSMTPPeer(t, func(conn net.Conn) error {
		reader := bufio.NewReader(conn)
		if _, err := io.WriteString(conn, "220 ready\r\n"); err != nil {
			return err
		}
		if line, err := reader.ReadString('\n'); err != nil || !strings.HasPrefix(line, "EHLO ") {
			return fmt.Errorf("expected EHLO, got %q: %v", line, err)
		}
		if _, err := io.WriteString(conn, "250-peer\r\n250 STARTTLS\r\n"); err != nil {
			return err
		}
		if line, err := reader.ReadString('\n'); err != nil || line != "STARTTLS\r\n" {
			return fmt.Errorf("expected STARTTLS, got %q: %v", line, err)
		}
		if _, err := io.WriteString(conn, "220 start TLS\r\n"); err != nil {
			return err
		}
		if err := tls.Server(conn, fixture.TLS).Handshake(); err == nil {
			return errors.New("client accepted an untrusted certificate")
		}
		return nil
	})
	sender := NewSMTPSender("127.0.0.1", port, "user", "pass", "from@example.com")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := sender.Send(ctx, Message{To: "to@example.com"})
	if !errors.Is(err, ErrPermanent) || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected certificate verification failure, got %v", err)
	}
	select {
	case err := <-peerResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("TLS peer did not finish")
	}
}

func TestSMTPSender_TLSDelivery(t *testing.T) {
	for _, mode := range []SMTPMode{SMTPModeSTARTTLS, SMTPModeTLS} {
		t.Run(string(mode), func(t *testing.T) {
			fixture := httptest.NewTLSServer(nil)
			defer fixture.Close()
			// Trust only this fixture, with normal certificate and IP verification.
			roots := x509.NewCertPool()
			roots.AddCert(fixture.Certificate())
			const host = "127.0.0.1"
			tlsConfig := &tls.Config{ServerName: host, RootCAs: roots}
			msg := Message{
				To:       "Recipient <to@example.com>",
				Subject:  "TLS delivery",
				TextBody: "Delivered over verified TLS",
				HTMLBody: "<p>Delivered over verified TLS</p>",
			}
			port, peerResult := startSMTPPeer(t, func(conn net.Conn) error {
				peer := textproto.NewConn(conn)
				exchange := func(command, reply string) error {
					line, err := peer.ReadLine()
					if err != nil || line != command {
						return fmt.Errorf("expected %s, got %q: %v", command, line, err)
					}
					return peer.PrintfLine("%s", reply)
				}
				if mode == SMTPModeSTARTTLS {
					if err := peer.PrintfLine("220 ready"); err != nil {
						return err
					}
					if err := exchange("EHLO localhost", "250-peer\r\n250 STARTTLS"); err != nil {
						return err
					}
					if err := exchange("STARTTLS", "220 start TLS"); err != nil {
						return err
					}
				}
				secure := tls.Server(conn, fixture.TLS)
				if err := secure.Handshake(); err != nil {
					return err
				}
				// Every subsequent command is read from the encrypted connection.
				peer = textproto.NewConn(secure)
				if mode == SMTPModeTLS {
					if err := peer.PrintfLine("220 ready"); err != nil {
						return err
					}
				}
				if err := exchange("EHLO localhost", "250-peer\r\n250 AUTH PLAIN"); err != nil {
					return err
				}
				credentials := base64.StdEncoding.EncodeToString([]byte("\x00user\x00pass"))
				for _, step := range []struct{ command, reply string }{
					{"AUTH PLAIN " + credentials, "235 authenticated"},
					{"MAIL FROM:<from@example.com>", "250 OK"},
					{"RCPT TO:<to@example.com>", "250 OK"},
					{"DATA", "354 send data"},
				} {
					if err := exchange(step.command, step.reply); err != nil {
						return err
					}
				}
				body, err := peer.ReadDotBytes()
				if err != nil {
					return err
				}
				for _, want := range []string{"Subject: " + msg.Subject, msg.TextBody, msg.HTMLBody} {
					if !strings.Contains(string(body), want) {
						return fmt.Errorf("delivered message missing %q", want)
					}
				}
				if err := peer.PrintfLine("250 accepted"); err != nil {
					return err
				}
				return exchange("QUIT", "221 bye")
			})
			sender := NewSMTPSenderWithConfig(host, port, "user", "pass", "Sender <from@example.com>", SMTPConfig{Mode: mode})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := sender.send(ctx, msg, 30*time.Second, tlsConfig); err != nil {
				t.Fatalf("Send with verified %s: %v", mode, err)
			}
			select {
			case err := <-peerResult:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("TLS peer did not finish")
			}
		})
	}
}

func TestSMTPSender_AlreadyCanceled(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sender := NewSMTPSender("127.0.0.1", ln.Addr().(*net.TCPAddr).Port, "", "", "from@example.com")
	err = sender.Send(ctx, Message{To: "to@example.com"})
	if !errors.Is(err, ErrTransient) || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected transient cancellation, got %v", err)
	}
	if err := ln.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	conn, err := ln.Accept()
	if err == nil {
		_ = conn.Close()
		t.Fatal("already-canceled send opened a connection")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("Accept: %v", err)
	}
}

func TestSMTPSender_StallsCloseConnection(t *testing.T) {
	for _, stage := range []string{"greeting", "mail", "data", "tls", "implicit_tls"} {
		for _, mode := range []string{"cancel", "caller_deadline", "fallback_deadline"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				ready := make(chan struct{})
				port, peerResult := startSMTPPeer(t, func(conn net.Conn) error {
					reader := bufio.NewReader(conn)
					step := func(reply, command string) error {
						if _, err := io.WriteString(conn, reply); err != nil {
							return err
						}
						line, err := reader.ReadString('\n')
						if err != nil {
							return err
						}
						if !strings.HasPrefix(line, command) {
							return fmt.Errorf("expected %s, got %q", command, line)
						}
						return nil
					}
					if stage != "greeting" && stage != "implicit_tls" {
						if err := step("220 ready\r\n", "EHLO "); err != nil {
							return err
						}
						if stage == "tls" {
							if err := step("250-peer\r\n250 STARTTLS\r\n", "STARTTLS\r\n"); err != nil {
								return err
							}
							if _, err := io.WriteString(conn, "220 start TLS\r\n"); err != nil {
								return err
							}
						} else {
							if err := step("250 peer\r\n", "MAIL FROM:"); err != nil {
								return err
							}
							if stage == "data" {
								if err := step("250 OK\r\n", "RCPT TO:"); err != nil {
									return err
								}
								if err := step("250 OK\r\n", "DATA\r\n"); err != nil {
									return err
								}
								if _, err := io.WriteString(conn, "354 send data\r\n"); err != nil {
									return err
								}
								for {
									line, err := reader.ReadString('\n')
									if err != nil {
										return err
									}
									if line == ".\r\n" {
										break
									}
								}
							}
						}
					}
					close(ready)
					// EOF proves the client closed the socket; a timeout is failure.
					_, err := io.Copy(io.Discard, reader)
					return err
				})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "caller_deadline" {
					var deadlineCancel context.CancelFunc
					ctx, deadlineCancel = context.WithTimeout(ctx, time.Second)
					defer deadlineCancel()
				}
				sender := NewSMTPSenderWithConfig("127.0.0.1", port, "", "", "from@example.com", SMTPConfig{Mode: SMTPModeDevLoopback})
				if stage == "implicit_tls" {
					sender = NewSMTPSenderWithConfig("127.0.0.1", port, "user", "pass", "from@example.com", SMTPConfig{Mode: SMTPModeTLS})
				}
				result := make(chan error, 1)
				go func() {
					msg := Message{To: "to@example.com", TextBody: "hello"}
					if mode == "fallback_deadline" {
						result <- sender.send(ctx, msg, time.Second, nil)
					} else {
						result <- sender.Send(ctx, msg)
					}
				}()
				select {
				case <-ready:
				case err := <-peerResult:
					t.Fatalf("peer failed before stall: %v", err)
				case <-time.After(3 * time.Second):
					t.Fatal("peer did not reach stall")
				}
				want := context.DeadlineExceeded
				if mode == "cancel" {
					want = context.Canceled
					cancel()
				}
				select {
				case err := <-result:
					if !errors.Is(err, ErrTransient) || !errors.Is(err, want) {
						t.Fatalf("expected transient %v, got %v", want, err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("Send did not return promptly")
				}
				select {
				case err := <-peerResult:
					if err != nil {
						t.Fatalf("peer did not observe clean socket closure: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("Send returned without closing its socket")
				}
			})
		}
	}
}
