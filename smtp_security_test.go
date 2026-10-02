package sendmail

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func TestSMTPSender_RequiresSTARTTLS(t *testing.T) {
	for _, helo := range []bool{false, true} {
		t.Run(fmt.Sprintf("HELO=%t", helo), func(t *testing.T) {
			port, result := startSMTPPeer(t, func(conn net.Conn) error {
				peer := textproto.NewConn(conn)
				if err := peer.PrintfLine("220 ready"); err != nil {
					return err
				}
				line, err := peer.ReadLine()
				if err != nil || !strings.HasPrefix(line, "EHLO ") {
					return fmt.Errorf("expected EHLO: %q: %v", line, err)
				}
				if helo {
					if err := peer.PrintfLine("500 EHLO unsupported"); err != nil {
						return err
					}
					line, err = peer.ReadLine()
					if err != nil || !strings.HasPrefix(line, "HELO ") {
						return fmt.Errorf("expected HELO: %q: %v", line, err)
					}
				}
				// Advertising AUTH without STARTTLS must not expose credentials.
				if err := peer.PrintfLine("250-peer\r\n250 AUTH PLAIN"); err != nil {
					return err
				}
				line, err = peer.ReadLine()
				if err != io.EOF {
					return fmt.Errorf("expected closure before AUTH/MAIL/DATA: %q: %v", line, err)
				}
				return nil
			})
			sender := NewSMTPSender("127.0.0.1", port, "user", "pass", "from@example.com")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := sender.Send(ctx, Message{To: "to@example.com"}); !errors.Is(err, ErrPermanent) || errors.Is(err, ErrTransient) {
				t.Fatalf("expected permanent STARTTLS policy failure: %v", err)
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSMTPSender_TLSFailuresStopDelivery(t *testing.T) {
	fixture := httptest.NewTLSServer(nil)
	defer fixture.Close()
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	for _, mode := range []SMTPMode{SMTPModeSTARTTLS, SMTPModeTLS, SMTPModeDevLoopback} {
		for _, failure := range []string{"untrusted", "hostname", "missing_auth", "failed_auth"} {
			t.Run(string(mode)+"/"+failure, func(t *testing.T) {
				port, result := startSMTPPeer(t, func(conn net.Conn) error {
					peer := textproto.NewConn(conn)
					exchange := func(command, reply string) error {
						line, err := peer.ReadLine()
						if err != nil || !strings.HasPrefix(line, command) {
							return fmt.Errorf("expected %s: %q: %v", command, line, err)
						}
						return peer.PrintfLine("%s", reply)
					}
					if mode != SMTPModeTLS {
						if err := peer.PrintfLine("220 ready"); err != nil {
							return err
						}
						// Pre-TLS AUTH must not be reused after TLS.
						if err := exchange("EHLO ", "250-peer\r\n250-AUTH PLAIN\r\n250 STARTTLS"); err != nil {
							return err
						}
						if err := exchange("STARTTLS", "220 start TLS"); err != nil {
							return err
						}
					}
					secure := tls.Server(conn, fixture.TLS)
					err := secure.Handshake()
					if failure == "untrusted" || failure == "hostname" {
						if err == nil {
							return errors.New("client accepted invalid certificate")
						}
						return nil
					}
					if err != nil {
						return err
					}
					peer = textproto.NewConn(secure)
					if mode == SMTPModeTLS {
						if err := peer.PrintfLine("220 ready"); err != nil {
							return err
						}
					}
					reply := "250 peer"
					if failure == "failed_auth" {
						reply = "250-peer\r\n250 AUTH PLAIN"
					}
					if err := exchange("EHLO ", reply); err != nil {
						return err
					}
					if failure == "failed_auth" {
						if err := exchange("AUTH PLAIN ", "535 authentication failed"); err != nil {
							return err
						}
						// net/smtp aborts AUTH and sends QUIT on failure, never MAIL.
						if err := exchange("*", "501 aborted"); err != nil {
							return err
						}
						if err := exchange("QUIT", "221 bye"); err != nil {
							return err
						}
					}
					line, err := peer.ReadLine()
					if err != io.EOF {
						return fmt.Errorf("expected closure before MAIL/DATA: %q: %v", line, err)
					}
					return nil
				})
				config := &tls.Config{ServerName: "127.0.0.1", RootCAs: roots}
				if failure == "untrusted" {
					config = nil
				}
				host := "127.0.0.1"
				if failure == "hostname" {
					host = "localhost" // resolves to loopback, but is absent from the fixture's SANs
				}
				sender := NewSMTPSenderWithConfig(host, port, "user", "pass", "from@example.com", SMTPConfig{Mode: mode})
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				err := sender.send(ctx, Message{To: "to@example.com"}, 30*time.Second, config)
				if !errors.Is(err, ErrPermanent) || errors.Is(err, ErrTransient) {
					t.Fatalf("expected permanent TLS/AUTH failure: %v", err)
				}
				if err := <-result; err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestSMTPSender_ConfigValidationBeforeDial(t *testing.T) {
	for _, tc := range []struct {
		mode           SMTPMode
		user, password string
	}{
		{"typo", "user", "pass"}, {"dev_loopback", "", ""},
		{"", "", ""}, {SMTPModeSTARTTLS, "", "pass"}, {SMTPModeTLS, "user", ""},
		{SMTPModeTLS, " ", "pass"}, {SMTPModeDevLoopback, "user", ""},
	} {
		t.Run(fmt.Sprintf("%s/user=%q/password=%q", tc.mode, tc.user, tc.password), func(t *testing.T) {
			// A dial to this syntactically invalid address would be transient.
			sender := NewSMTPSenderWithConfig("invalid host", 587, tc.user, tc.password, "from@example.com", SMTPConfig{Mode: tc.mode})
			err := sender.Send(context.Background(), Message{To: "to@example.com"})
			if !errors.Is(err, ErrPermanent) || errors.Is(err, ErrTransient) {
				t.Fatalf("expected configuration failure before dial: %v", err)
			}
		})
	}
}

func TestSMTPLoopbackPeer(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "127.0.0.2", "::1", "::ffff:127.0.0.1", "10.0.0.1", "172.18.0.2", "192.168.1.1", "2001:db8::1", "203.0.113.1"} {
		t.Run(ip, func(t *testing.T) {
			addr := &net.TCPAddr{IP: net.ParseIP(ip), Port: 25}
			err := validateSMTPLoopbackPeer(addr)
			if addr.IP.IsLoopback() {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrPermanent) {
				t.Fatalf("expected permanent rejection: %v", err)
			}
		})
	}
	if err := validateSMTPLoopbackPeer(&net.UnixAddr{Name: "localhost"}); !errors.Is(err, ErrPermanent) {
		t.Fatalf("hostname is not a loopback TCP peer: %v", err)
	}
}

func TestSMTPSender_DevLoopbackIPv6(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	port, result := startSMTPPeerListener(t, ln, func(conn net.Conn) error {
		msg := handleSMTPConn(conn)
		if msg.from != "from@example.com" || len(msg.to) != 1 || msg.to[0] != "to@example.com" {
			return fmt.Errorf("unexpected IPv6 envelope: %+v", msg)
		}
		return nil
	})
	sender := NewSMTPSenderWithConfig("::1", port, "", "", "from@example.com", SMTPConfig{Mode: SMTPModeDevLoopback})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sender.Send(ctx, Message{To: "to@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestClassifySMTPError_PreservesClassification(t *testing.T) {
	for _, sentinel := range []error{ErrPermanent, ErrTransient, context.Canceled, context.DeadlineExceeded} {
		err := classifySMTPError(fmt.Errorf("wrapped: %w", sentinel))
		if !errors.Is(err, sentinel) {
			t.Fatalf("lost sentinel %v: %v", sentinel, err)
		}
		if sentinel == ErrPermanent && errors.Is(err, ErrTransient) {
			t.Fatalf("policy error became transient: %v", err)
		}
	}
}
