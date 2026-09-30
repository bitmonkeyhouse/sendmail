package sendmail

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMailgunSender_ClassifiesErrors(t *testing.T) {
	tests := []struct {
		name          string
		statusCode    int
		body          string
		wantTransient bool
		wantPermanent bool
	}{
		{name: "200 OK", statusCode: 200, body: `{"id":"abc","message":"Queued. Thank you."}`},
		{name: "202 Accepted", statusCode: 202, body: `{"id":"abc","message":"Queued. Thank you."}`},
		{name: "400 Bad Request", statusCode: 400, body: `{"message":"bad request"}`, wantPermanent: true},
		{name: "401 Unauthorized", statusCode: 401, body: `{"message":"unauthorized"}`, wantPermanent: true},
		{name: "429 Rate Limited", statusCode: 429, body: `{"message":"rate limited"}`, wantTransient: true},
		{name: "500 Internal", statusCode: 500, body: `{"message":"internal error"}`, wantTransient: true},
		{name: "503 Service Unavailable", statusCode: 503, body: `{"message":"unavailable"}`, wantTransient: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			sender := NewMailgunSenderWithClient("test-key", "example.com", "default@example.com", server.Client())
			sender.baseURL = server.URL

			msg := Message{
				To:       "to@example.com",
				Subject:  "Test",
				HTMLBody: "<p>Hello</p>",
				TextBody: "Hello",
			}

			err := sender.Send(context.Background(), msg)

			if tt.wantTransient {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, ErrTransient) {
					t.Fatalf("expected ErrTransient, got: %v", err)
				}
				if errors.Is(err, ErrPermanent) {
					t.Fatal("should not be ErrPermanent")
				}
			} else if tt.wantPermanent {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, ErrPermanent) {
					t.Fatalf("expected ErrPermanent, got: %v", err)
				}
				if errors.Is(err, ErrTransient) {
					t.Fatal("should not be ErrTransient")
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got: %v", err)
				}
			}
		})
	}
}

func TestMailgunSender_ConfiguresEndpointRegion(t *testing.T) {
	tests := []struct {
		name   string
		sender *MailgunSender
		want   string
	}{
		{
			name:   "default constructor uses EU",
			sender: NewMailgunSender("test-key", "example.com", "default@example.com"),
			want:   mailgunAPIBaseEU,
		},
		{
			name:   "empty config uses EU",
			sender: NewMailgunSenderWithConfig("test-key", "example.com", "default@example.com", MailgunConfig{}),
			want:   mailgunAPIBaseEU,
		},
		{
			name: "explicit EU uses EU",
			sender: NewMailgunSenderWithConfig("test-key", "example.com", "default@example.com", MailgunConfig{
				Region: MailgunRegionEU,
			}),
			want: mailgunAPIBaseEU,
		},
		{
			name: "US config uses US",
			sender: NewMailgunSenderWithConfig("test-key", "example.com", "default@example.com", MailgunConfig{
				Region: MailgunRegionUS,
			}),
			want: mailgunAPIBaseUS,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.sender.baseURL != tt.want {
				t.Fatalf("expected baseURL=%q, got %q", tt.want, tt.sender.baseURL)
			}
		})
	}
}

func TestMailgunSender_NetworkError(t *testing.T) {
	// Use a closed server to simulate connection refused.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	sender := NewMailgunSenderWithClient("test-key", "example.com", "default@example.com", server.Client())
	sender.baseURL = server.URL

	msg := Message{
		To:       "to@example.com",
		Subject:  "Test",
		HTMLBody: "<p>Hello</p>",
		TextBody: "Hello",
	}

	err := sender.Send(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrTransient) {
		t.Fatalf("expected ErrTransient for network error, got: %v", err)
	}
}

func TestMailgunSender_SendsMultipartRequest(t *testing.T) {
	var gotPath string
	var gotUser string
	var gotPassword string
	gotFields := make(map[string]string)
	handlerErr := make(chan string, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUser, gotPassword, _ = r.BasicAuth()

		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			handlerErr <- "expected multipart/form-data content type, got " + r.Header.Get("Content-Type")
			w.WriteHeader(400)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			handlerErr <- "parse multipart form: " + err.Error()
			w.WriteHeader(400)
			return
		}
		for key, values := range r.MultipartForm.Value {
			if len(values) > 0 {
				gotFields[key] = values[0]
			}
		}
		handlerErr <- ""

		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"abc","message":"Queued. Thank you."}`))
	}))
	defer server.Close()

	sender := NewMailgunSenderWithClient("test-key", "example.com", "default@example.com", server.Client())
	sender.baseURL = server.URL

	msg := Message{
		To:       "to@example.com",
		Subject:  "Test Subject",
		HTMLBody: "<p>Hello</p>",
		TextBody: "Hello",
		ReplyTo:  "reply@example.com",
	}

	err := sender.Send(context.Background(), msg)
	if msg := <-handlerErr; msg != "" {
		t.Fatal(msg)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotPath != "/v3/example.com/messages" {
		t.Fatalf("expected path /v3/example.com/messages, got %q", gotPath)
	}
	if gotUser != "api" || gotPassword != "test-key" {
		t.Fatalf("expected basic auth api/test-key, got %q/%q", gotUser, gotPassword)
	}

	wantFields := map[string]string{
		"from":       "default@example.com",
		"to":         "to@example.com",
		"subject":    "Test Subject",
		"text":       "Hello",
		"html":       "<p>Hello</p>",
		"h:Reply-To": "reply@example.com",
	}
	for key, want := range wantFields {
		if gotFields[key] != want {
			t.Fatalf("expected %s=%q, got %q", key, want, gotFields[key])
		}
	}
}

func TestMailgunSender_ExplicitFrom(t *testing.T) {
	var gotFrom string
	handlerErr := make(chan string, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			handlerErr <- "parse multipart form: " + err.Error()
			w.WriteHeader(400)
			return
		}
		gotFrom = r.FormValue("from")
		handlerErr <- ""
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"abc","message":"Queued. Thank you."}`))
	}))
	defer server.Close()

	sender := NewMailgunSenderWithClient("test-key", "example.com", "default@example.com", server.Client())
	sender.baseURL = server.URL

	msg := Message{
		To:       "to@example.com",
		From:     "custom@example.com",
		Subject:  "Test",
		HTMLBody: "<p>Hi</p>",
		TextBody: "Hi",
	}

	err := sender.Send(context.Background(), msg)
	if msg := <-handlerErr; msg != "" {
		t.Fatal(msg)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotFrom != "custom@example.com" {
		t.Fatalf("expected from=custom@example.com, got %q", gotFrom)
	}
}
