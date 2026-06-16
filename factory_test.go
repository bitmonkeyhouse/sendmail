package email

import (
	"strings"
	"testing"
)

func TestNewSender_ConfiguresResend(t *testing.T) {
	sender, err := NewSender(Config{
		Provider:     " ReSeNd ",
		DefaultFrom:  "default@example.com",
		ResendAPIKey: "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resendSender, ok := sender.(*ResendSender)
	if !ok {
		t.Fatalf("expected *ResendSender, got %T", sender)
	}
	if resendSender.apiKey != "test-key" {
		t.Fatalf("expected apiKey=test-key, got %q", resendSender.apiKey)
	}
	if resendSender.defaultFrom != "default@example.com" {
		t.Fatalf("expected defaultFrom=default@example.com, got %q", resendSender.defaultFrom)
	}
}

func TestNewSender_ConfiguresMailgun(t *testing.T) {
	sender, err := NewSender(Config{
		Provider:      "MAILGUN",
		DefaultFrom:   "default@example.com",
		MailgunAPIKey: "test-key",
		MailgunDomain: "example.com",
		MailgunRegion: "US",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mailgunSender, ok := sender.(*MailgunSender)
	if !ok {
		t.Fatalf("expected *MailgunSender, got %T", sender)
	}
	if mailgunSender.apiKey != "test-key" {
		t.Fatalf("expected apiKey=test-key, got %q", mailgunSender.apiKey)
	}
	if mailgunSender.domain != "example.com" {
		t.Fatalf("expected domain=example.com, got %q", mailgunSender.domain)
	}
	if mailgunSender.defaultFrom != "default@example.com" {
		t.Fatalf("expected defaultFrom=default@example.com, got %q", mailgunSender.defaultFrom)
	}
	if mailgunSender.baseURL != mailgunAPIBaseUS {
		t.Fatalf("expected baseURL=%q, got %q", mailgunAPIBaseUS, mailgunSender.baseURL)
	}
}

func TestNewSender_ConfiguresSMTP(t *testing.T) {
	sender, err := NewSender(Config{
		Provider:     ProviderSMTP,
		DefaultFrom:  "default@example.com",
		SMTPHost:     "smtp.example.com",
		SMTPPort:     587,
		SMTPUser:     "user",
		SMTPPassword: "password",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	smtpSender, ok := sender.(*SMTPSender)
	if !ok {
		t.Fatalf("expected *SMTPSender, got %T", sender)
	}
	if smtpSender.host != "smtp.example.com" {
		t.Fatalf("expected host=smtp.example.com, got %q", smtpSender.host)
	}
	if smtpSender.port != 587 {
		t.Fatalf("expected port=587, got %d", smtpSender.port)
	}
	if smtpSender.user != "user" {
		t.Fatalf("expected user=user, got %q", smtpSender.user)
	}
	if smtpSender.password != "password" {
		t.Fatalf("expected password=password, got %q", smtpSender.password)
	}
	if smtpSender.from != "default@example.com" {
		t.Fatalf("expected from=default@example.com, got %q", smtpSender.from)
	}
}

func TestNewSender_Validation(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr string
	}{
		{
			name:    "missing provider",
			config:  Config{},
			wantErr: "email provider is required",
		},
		{
			name: "unsupported provider",
			config: Config{
				Provider:    "ses",
				DefaultFrom: "default@example.com",
			},
			wantErr: "unsupported email provider",
		},
		{
			name: "missing default from",
			config: Config{
				Provider:     ProviderResend,
				ResendAPIKey: "test-key",
			},
			wantErr: "email default from is required",
		},
		{
			name: "missing resend API key",
			config: Config{
				Provider:    ProviderResend,
				DefaultFrom: "default@example.com",
			},
			wantErr: "resend API key is required",
		},
		{
			name: "missing mailgun API key",
			config: Config{
				Provider:      ProviderMailgun,
				DefaultFrom:   "default@example.com",
				MailgunDomain: "example.com",
			},
			wantErr: "mailgun API key is required",
		},
		{
			name: "missing mailgun domain",
			config: Config{
				Provider:      ProviderMailgun,
				DefaultFrom:   "default@example.com",
				MailgunAPIKey: "test-key",
			},
			wantErr: "mailgun domain is required",
		},
		{
			name: "invalid mailgun region",
			config: Config{
				Provider:      ProviderMailgun,
				DefaultFrom:   "default@example.com",
				MailgunAPIKey: "test-key",
				MailgunDomain: "example.com",
				MailgunRegion: "ap",
			},
			wantErr: "unsupported mailgun region",
		},
		{
			name: "missing smtp host",
			config: Config{
				Provider:     ProviderSMTP,
				DefaultFrom:  "default@example.com",
				SMTPPort:     587,
				SMTPUser:     "user",
				SMTPPassword: "password",
			},
			wantErr: "smtp host is required",
		},
		{
			name: "invalid smtp port",
			config: Config{
				Provider:     ProviderSMTP,
				DefaultFrom:  "default@example.com",
				SMTPHost:     "smtp.example.com",
				SMTPUser:     "user",
				SMTPPassword: "password",
			},
			wantErr: "smtp port must be between 1 and 65535",
		},
		// SMTP credentials are now optional (nil auth when absent, e.g. Mailpit),
		// so the former "missing smtp user/password" error cases are removed.
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender, err := NewSender(tt.config)
			if err == nil {
				t.Fatalf("expected error, got sender %T", sender)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestNewSenderFromEnv_ConfiguresResend(t *testing.T) {
	t.Setenv("EMAIL_PROVIDER", "resend")
	t.Setenv("EMAIL_FROM", "default@example.com")
	t.Setenv("RESEND_API_KEY", "test-key")

	sender, err := NewSenderFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resendSender, ok := sender.(*ResendSender)
	if !ok {
		t.Fatalf("expected *ResendSender, got %T", sender)
	}
	if resendSender.apiKey != "test-key" {
		t.Fatalf("expected apiKey=test-key, got %q", resendSender.apiKey)
	}
	if resendSender.defaultFrom != "default@example.com" {
		t.Fatalf("expected defaultFrom=default@example.com, got %q", resendSender.defaultFrom)
	}
}

func TestNewSenderFromEnv_ConfiguresMailgun(t *testing.T) {
	t.Setenv("EMAIL_PROVIDER", "mailgun")
	t.Setenv("EMAIL_FROM", "default@example.com")
	t.Setenv("MAILGUN_API_KEY", "test-key")
	t.Setenv("MAILGUN_DOMAIN", "example.com")
	t.Setenv("MAILGUN_REGION", "us")

	sender, err := NewSenderFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mailgunSender, ok := sender.(*MailgunSender)
	if !ok {
		t.Fatalf("expected *MailgunSender, got %T", sender)
	}
	if mailgunSender.baseURL != mailgunAPIBaseUS {
		t.Fatalf("expected baseURL=%q, got %q", mailgunAPIBaseUS, mailgunSender.baseURL)
	}
}

func TestNewSenderFromEnv_ConfiguresSMTP(t *testing.T) {
	t.Setenv("EMAIL_PROVIDER", "smtp")
	t.Setenv("EMAIL_FROM", "default@example.com")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_PORT", "587")
	t.Setenv("SMTP_USER", "user")
	t.Setenv("SMTP_PASSWORD", "password")

	sender, err := NewSenderFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	smtpSender, ok := sender.(*SMTPSender)
	if !ok {
		t.Fatalf("expected *SMTPSender, got %T", sender)
	}
	if smtpSender.port != 587 {
		t.Fatalf("expected port=587, got %d", smtpSender.port)
	}
}

func TestNewSenderFromEnv_InvalidSMTPPort(t *testing.T) {
	t.Setenv("EMAIL_PROVIDER", "smtp")
	t.Setenv("EMAIL_FROM", "default@example.com")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_PORT", "not-a-number")
	t.Setenv("SMTP_USER", "user")
	t.Setenv("SMTP_PASSWORD", "password")

	sender, err := NewSenderFromEnv()
	if err == nil {
		t.Fatalf("expected error, got sender %T", sender)
	}
	if !strings.Contains(err.Error(), "SMTP_PORT must be an integer") {
		t.Fatalf("expected SMTP_PORT error, got %q", err.Error())
	}
}
