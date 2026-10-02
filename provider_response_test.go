package sendmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type providerTestTransport func(*http.Request) (*http.Response, error)

func (f providerTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func providerTestSender(provider string, client *http.Client, baseURL string) Sender {
	if provider == "resend" {
		s := NewResendSenderWithClient("test-key", "from@example.com", client)
		s.baseURL = baseURL
		return s
	}
	s := NewMailgunSenderWithClient("test-key", "example.com", "from@example.com", client)
	s.baseURL = baseURL
	return s
}

type providerTestBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *providerTestBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func (b *providerTestBody) Close() error {
	b.closed = true
	return nil
}

// Emits data forever without allocating a giant response or returning EOF.
type providerEndlessReader struct{}

func (providerEndlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

type providerFailingReader struct {
	remaining int
	err       error
}

func (r *providerFailingReader) Read(p []byte) (int, error) {
	n := min(len(p), r.remaining)
	for i := range p[:n] {
		p[i] = 'x'
	}
	r.remaining -= n
	if r.remaining == 0 {
		return n, r.err
	}
	return n, nil
}

func TestReadProviderResponse(t *testing.T) {
	sentinel := errors.New("read failed")
	for _, tt := range []struct {
		name string
		size int
		err  error
	}{
		{"below", providerResponseLimit - 1, nil},
		{"exact", providerResponseLimit, nil},
		{"above", providerResponseLimit + 1, nil},
		{"read failure", 10, sentinel},
		{"exact with failure", providerResponseLimit, sentinel},
		{"overflow with simultaneous failure", providerResponseLimit + 1, sentinel},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var reader io.Reader = strings.NewReader(strings.Repeat("x", tt.size))
			if tt.err != nil {
				reader = &providerFailingReader{remaining: tt.size, err: tt.err}
			}
			body, err := readProviderResponse(reader)
			if tt.size > providerResponseLimit || tt.err != nil {
				if body != nil || !errors.Is(err, ErrTransient) || errors.Is(err, ErrPermanent) {
					t.Fatalf("expected nil body and transient error, got %d bytes, %v", len(body), err)
				}
				if tt.size > providerResponseLimit {
					if errors.Is(err, sentinel) || err.Error() != "transient email error: response exceeds 64 KiB limit" {
						t.Fatalf("overflow must take precedence with fixed error: %v", err)
					}
				} else if !errors.Is(err, sentinel) {
					t.Fatalf("lost underlying error: %v", err)
				}
			} else if err != nil || string(body) != strings.Repeat("x", tt.size) {
				t.Fatalf("response not preserved: length %d, error %v", len(body), err)
			}
		})
	}
}

func TestHTTPProviders_ResponseLimit(t *testing.T) {
	for _, provider := range []string{"resend", "mailgun"} {
		for _, status := range []int{200, 400, 429, 500} {
			for _, size := range []int{providerResponseLimit - 1, providerResponseLimit, providerResponseLimit + 1} {
				t.Run(fmt.Sprintf("%s/%d/%d", provider, status, size), func(t *testing.T) {
					// Valid JSON padded to the exact requested length.
					payload := `{"message":"short"}`
					body := &providerTestBody{Reader: strings.NewReader(payload + strings.Repeat(" ", size-len(payload)))}
					client := &http.Client{Transport: providerTestTransport(func(*http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: status, Body: body, ContentLength: -1}, nil
					})}
					err := providerTestSender(provider, client, "http://provider.test").Send(context.Background(), Message{})
					wantTransient := size > providerResponseLimit || status == 429 || status >= 500
					wantPermanent := size <= providerResponseLimit && status == 400
					if errors.Is(err, ErrTransient) != wantTransient || errors.Is(err, ErrPermanent) != wantPermanent || (err == nil) != (!wantTransient && !wantPermanent) {
						t.Fatalf("unexpected classification: %v", err)
					}
					if !body.closed || body.read != size {
						t.Fatalf("read %d of %d; closed=%v", body.read, size, body.closed)
					}
					if size > providerResponseLimit {
						want := provider + ": transient email error: response exceeds 64 KiB limit"
						if err.Error() != want {
							t.Fatalf("unexpected overflow error: %v", err)
						}
					} else if err != nil && !strings.HasSuffix(err.Error(), ": short") {
						t.Fatalf("short message changed: %v", err)
					}
				})
			}
		}
	}
}

func TestHTTPProviders_EndlessAndReadFailure(t *testing.T) {
	sentinel := errors.New("response read sentinel")
	for _, provider := range []string{"resend", "mailgun"} {
		for _, tt := range []struct {
			name   string
			reader func() io.Reader
			read   int
			cause  error
		}{
			{"endless", func() io.Reader { return providerEndlessReader{} }, providerResponseLimit + 1, nil},
			{"failure", func() io.Reader { return &providerFailingReader{remaining: 10, err: sentinel} }, 10, sentinel},
			{"overflow and failure", func() io.Reader { return &providerFailingReader{remaining: providerResponseLimit + 1, err: sentinel} }, providerResponseLimit + 1, nil},
		} {
			t.Run(provider+"/"+tt.name, func(t *testing.T) {
				body := &providerTestBody{Reader: tt.reader()}
				client := &http.Client{Transport: providerTestTransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 400, Body: body, ContentLength: -1}, nil
				})}
				err := providerTestSender(provider, client, "http://provider.test").Send(context.Background(), Message{})
				if !errors.Is(err, ErrTransient) || errors.Is(err, ErrPermanent) || errors.Is(err, sentinel) != (tt.cause != nil) {
					t.Fatalf("unexpected classification/cause: %v", err)
				}
				if !body.closed || body.read != tt.read {
					t.Fatalf("read=%d want=%d closed=%v", body.read, tt.read, body.closed)
				}
				if tt.cause == nil && err.Error() != provider+": transient email error: response exceeds 64 KiB limit" {
					t.Fatalf("overflow includes unexpected details: %v", err)
				}
			})
		}
	}
}

func TestHTTPProviders_DetailLimit(t *testing.T) {
	for _, provider := range []string{"resend", "mailgun"} {
		for _, raw := range []bool{false, true} {
			if raw && provider != "mailgun" {
				continue
			}
			for _, tt := range []struct {
				name, message, want string
			}{
				{"short", "normal message", "normal message"},
				{"empty", "", ""},
				{"exact", strings.Repeat("x", providerDetailLimit), strings.Repeat("x", providerDetailLimit)},
				{"large", strings.Repeat("x", 8<<10), strings.Repeat("x", providerDetailLimit) + providerDetailTruncation},
				{"multibyte", strings.Repeat("x", providerDetailLimit-1) + strings.Repeat("界", 100), strings.Repeat("x", providerDetailLimit-1) + providerDetailTruncation},
			} {
				for _, status := range []int{400, 429, 500} {
					t.Run(fmt.Sprintf("%s/raw=%v/%s/%d", provider, raw, tt.name, status), func(t *testing.T) {
						payload := tt.message
						want := tt.want
						if !raw {
							encoded, err := json.Marshal(map[string]string{"message": tt.message})
							if err != nil {
								t.Fatal(err)
							}
							payload = string(encoded)
							// Preserve Mailgun's existing fallback for empty JSON messages.
							if provider == "mailgun" && tt.message == "" {
								want = payload
							}
						}
						client := &http.Client{Transport: providerTestTransport(func(*http.Request) (*http.Response, error) {
							return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(payload))}, nil
						})}
						err := providerTestSender(provider, client, "http://provider.test").Send(context.Background(), Message{})
						if err == nil || errors.Is(err, ErrPermanent) != (status == 400) || errors.Is(err, ErrTransient) != (status != 400) {
							t.Fatalf("unexpected classification: %v", err)
						}
						_, detail, found := strings.Cut(err.Error(), fmt.Sprintf("(status %d): ", status))
						if !found || detail != want || !utf8.ValidString(detail) || len(detail) > providerDetailLimit+len(providerDetailTruncation) {
							t.Fatalf("unexpected detail: length=%d validUTF8=%v", len(detail), utf8.ValidString(detail))
						}
					})
				}
			}
		}
	}
}

func TestLimitProviderDetail_InvalidUTF8(t *testing.T) {
	if got := limitProviderDetail("bad\xfftext"); got != "bad\uFFFDtext" || !utf8.ValidString(got) {
		t.Fatalf("invalid raw text not normalized: %q", got)
	}
}

type providerObservedBody struct {
	io.ReadCloser
	read   chan struct{}
	closed chan struct{}
}

func (b *providerObservedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		select {
		case b.read <- struct{}{}:
		default:
		}
	}
	return n, err
}

func (b *providerObservedBody) Close() error {
	err := b.ReadCloser.Close()
	close(b.closed)
	return err
}

func providerWait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestHTTPProviders_ContextCancelsResponseRead(t *testing.T) {
	for _, provider := range []string{"resend", "mailgun"} {
		t.Run(provider, func(t *testing.T) {
			release := make(chan struct{})
			handlerDone := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "partial response")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release) // Unblock server cleanup even if a test assertion fails.
			read := make(chan struct{}, 1)
			closed := make(chan struct{})
			transport := server.Client().Transport
			client := &http.Client{Transport: providerTestTransport(func(req *http.Request) (*http.Response, error) {
				resp, err := transport.RoundTrip(req)
				if err == nil {
					resp.Body = &providerObservedBody{ReadCloser: resp.Body, read: read, closed: closed}
				}
				return resp, err
			})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- providerTestSender(provider, client, server.URL).Send(ctx, Message{})
			}()
			providerWait(t, read, "partial response read")
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, ErrTransient) || !errors.Is(err, context.Canceled) || errors.Is(err, ErrPermanent) {
					t.Fatalf("cancellation lost classification/cause: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Send did not abort the stalled response read")
			}
			providerWait(t, closed, "response body close")
			providerWait(t, handlerDone, "server cancellation")
		})
	}
}
