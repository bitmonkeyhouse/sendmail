package sendmail

import (
	"context"
	"os"
	"testing"
)

func TestResendSender_Integration(t *testing.T) {
	apiKey := os.Getenv("RESEND_API_KEY")
	if apiKey == "" {
		t.Skip("RESEND_API_KEY not set, skipping integration test")
	}

	defaultFrom := os.Getenv("EMAIL_FROM")
	if defaultFrom == "" {
		t.Skip("EMAIL_FROM not set, skipping integration test")
	}

	to := os.Getenv("RESEND_TO_ADDRESS")
	if to == "" {
		t.Skip("RESEND_TO_ADDRESS not set, skipping integration test")
	}

	sender := NewResendSender(apiKey, defaultFrom)

	msg := Message{
		To:       to,
		Subject:  "Integration Test from github.com/bitmonkeyhouse/sendmail",
		HTMLBody: "<p>This is an integration test from github.com/bitmonkeyhouse/sendmail.</p>",
		TextBody: "This is an integration test from github.com/bitmonkeyhouse/sendmail.",
	}

	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatalf("ResendSender.Send failed: %v", err)
	}
}
