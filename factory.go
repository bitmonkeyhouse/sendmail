package sendmail

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Provider identifies which email transport implementation to use.
type Provider string

const (
	// ProviderResend sends email via the Resend HTTP API.
	ProviderResend Provider = "resend"
	// ProviderMailgun sends email via the Mailgun HTTP API.
	ProviderMailgun Provider = "mailgun"
	// ProviderSMTP sends email via a standard SMTP server.
	ProviderSMTP Provider = "smtp"
)

// Config configures an email Sender.
type Config struct {
	// Provider chooses which Sender implementation to create.
	Provider Provider
	// DefaultFrom is required and is used when Message.From is empty.
	DefaultFrom string

	// ResendAPIKey is required when Provider is ProviderResend.
	ResendAPIKey string

	// MailgunAPIKey is required when Provider is ProviderMailgun.
	MailgunAPIKey string
	// MailgunDomain is required when Provider is ProviderMailgun.
	MailgunDomain string
	// MailgunRegion chooses the Mailgun endpoint. The zero value defaults to EU.
	MailgunRegion MailgunRegion

	// SMTPHost is required when Provider is ProviderSMTP.
	SMTPHost string
	// SMTPPort must be between 1 and 65535 when Provider is ProviderSMTP.
	SMTPPort int
	// SMTPMode selects SMTP security. The zero value requires STARTTLS.
	SMTPMode SMTPMode
	// SMTPUser is required except in credential-free dev-loopback mode.
	SMTPUser string
	// SMTPPassword is required except in credential-free dev-loopback mode.
	SMTPPassword string
}

// NewSender creates a Sender for config.Provider.
//
// Unlike the provider constructors, it validates config immediately: the
// provider must be resend, mailgun, or smtp; DefaultFrom must be nonblank; and
// provider-specific fields must be present, with Mailgun regions and SMTP modes
// checked. It returns an error describing the first missing or invalid field.
// Defaults remain the provider defaults (Mailgun EU, SMTP STARTTLS).
func NewSender(config Config) (Sender, error) {
	provider := normalizeProvider(config.Provider)
	if provider == "" {
		return nil, fmt.Errorf("email provider is required")
	}
	if provider != ProviderResend && provider != ProviderMailgun && provider != ProviderSMTP {
		return nil, fmt.Errorf("unsupported email provider %q", config.Provider)
	}
	if isBlank(config.DefaultFrom) {
		return nil, fmt.Errorf("email default from is required")
	}

	switch provider {
	case ProviderResend:
		if isBlank(config.ResendAPIKey) {
			return nil, fmt.Errorf("resend API key is required")
		}
		return NewResendSender(config.ResendAPIKey, config.DefaultFrom), nil

	case ProviderMailgun:
		region := normalizeMailgunRegion(config.MailgunRegion)
		if isBlank(config.MailgunAPIKey) {
			return nil, fmt.Errorf("mailgun API key is required")
		}
		if isBlank(config.MailgunDomain) {
			return nil, fmt.Errorf("mailgun domain is required")
		}
		if err := validateMailgunRegion(region); err != nil {
			return nil, err
		}
		return NewMailgunSenderWithConfig(config.MailgunAPIKey, config.MailgunDomain, config.DefaultFrom, MailgunConfig{Region: region}), nil

	case ProviderSMTP:
		mode := normalizeSMTPMode(config.SMTPMode)
		if err := validateSMTPConfig(config.SMTPHost, config.SMTPPort, config.SMTPUser, config.SMTPPassword, mode); err != nil {
			return nil, err
		}
		return NewSMTPSenderWithConfig(config.SMTPHost, config.SMTPPort, config.SMTPUser, config.SMTPPassword, config.DefaultFrom, SMTPConfig{Mode: mode}), nil
	}

	return nil, fmt.Errorf("unsupported email provider %q", config.Provider)
}

// NewSenderFromEnv creates a Sender from environment variables.
//
// EMAIL_PROVIDER must be one of resend, mailgun, or smtp. EMAIL_FROM is required
// and configures the default sender address. Provider-specific variables are RESEND_API_KEY;
// MAILGUN_API_KEY, MAILGUN_DOMAIN, and optional MAILGUN_REGION; or SMTP_HOST,
// SMTP_PORT, SMTP_USER, SMTP_PASSWORD, and optional SMTP_MODE (starttls by default).
// Only explicit dev-loopback mode may omit SMTP credentials. Values are validated
// by NewSender, so configuration errors are returned before any mail is sent.
func NewSenderFromEnv() (Sender, error) {
	config, err := configFromEnv()
	if err != nil {
		return nil, err
	}
	return NewSender(config)
}

func configFromEnv() (Config, error) {
	config := Config{
		Provider:    Provider(os.Getenv("EMAIL_PROVIDER")),
		DefaultFrom: os.Getenv("EMAIL_FROM"),
	}

	switch normalizeProvider(config.Provider) {
	case ProviderResend:
		config.ResendAPIKey = os.Getenv("RESEND_API_KEY")
	case ProviderMailgun:
		config.MailgunAPIKey = os.Getenv("MAILGUN_API_KEY")
		config.MailgunDomain = os.Getenv("MAILGUN_DOMAIN")
		config.MailgunRegion = MailgunRegion(os.Getenv("MAILGUN_REGION"))
	case ProviderSMTP:
		config.SMTPMode = SMTPMode(os.Getenv("SMTP_MODE"))
		config.SMTPHost = os.Getenv("SMTP_HOST")
		config.SMTPUser = os.Getenv("SMTP_USER")
		config.SMTPPassword = os.Getenv("SMTP_PASSWORD")
		port := strings.TrimSpace(os.Getenv("SMTP_PORT"))
		if port != "" {
			parsedPort, err := strconv.Atoi(port)
			if err != nil {
				return Config{}, fmt.Errorf("%w: SMTP_PORT must be an integer: %w", ErrPermanent, err)
			}
			config.SMTPPort = parsedPort
		}
	}

	return config, nil
}

func normalizeProvider(provider Provider) Provider {
	return Provider(strings.ToLower(strings.TrimSpace(string(provider))))
}

func normalizeMailgunRegion(region MailgunRegion) MailgunRegion {
	return MailgunRegion(strings.ToLower(strings.TrimSpace(string(region))))
}

func validateMailgunRegion(region MailgunRegion) error {
	switch region {
	case "", MailgunRegionEU, MailgunRegionUS:
		return nil
	default:
		return fmt.Errorf("unsupported mailgun region %q", region)
	}
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}
