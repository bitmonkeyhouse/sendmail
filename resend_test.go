package email

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResendSender_ClassifiesErrors(t *testing.T) {
	tests := []struct {
		name          string
		statusCode    int
		body          string
		wantTransient bool
		wantPermanent bool
	}{
		{name: "200 OK", statusCode: 200, body: `{"id":"abc"}`},
		{name: "400 Bad Request", statusCode: 400, body: `{"message":"bad request"}`, wantPermanent: true},
		{name: "422 Unprocessable", statusCode: 422, body: `{"message":"validation error"}`, wantPermanent: true},
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

			sender := NewResendSenderWithClient("test-key", "default@example.com", server.Client())
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

func TestResendSender_NetworkError(t *testing.T) {
	// Use a closed server to simulate connection refused.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	sender := NewResendSenderWithClient("test-key", "default@example.com", server.Client())
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

func TestResendSender_DefaultFrom(t *testing.T) {
	var gotFrom string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			From string `json:"from"`
		}
		_ = json.Unmarshal(body, &req)
		gotFrom = req.From
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	}))
	defer server.Close()

	sender := NewResendSenderWithClient("test-key", "default@example.com", server.Client())
	sender.baseURL = server.URL

	msg := Message{
		To:       "to@example.com",
		Subject:  "Test",
		HTMLBody: "<p>Hello</p>",
		TextBody: "Hello",
	}

	err := sender.Send(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotFrom != "default@example.com" {
		t.Fatalf("expected from=default@example.com, got %q", gotFrom)
	}
}

func TestResendSender_ExplicitFrom(t *testing.T) {
	var gotFrom string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			From string `json:"from"`
		}
		_ = json.Unmarshal(body, &req)
		gotFrom = req.From
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	}))
	defer server.Close()

	sender := NewResendSenderWithClient("test-key", "default@example.com", server.Client())
	sender.baseURL = server.URL

	msg := Message{
		To:       "to@example.com",
		From:     "custom@example.com",
		Subject:  "Test",
		HTMLBody: "<p>Hi</p>",
		TextBody: "Hi",
	}

	err := sender.Send(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotFrom != "custom@example.com" {
		t.Fatalf("expected from=custom@example.com, got %q", gotFrom)
	}
}
