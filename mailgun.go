package sendmail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

const (
	mailgunAPIBaseEU = "https://api.eu.mailgun.net"
	mailgunAPIBaseUS = "https://api.mailgun.net"
)

// MailgunRegion identifies which Mailgun API region to use.
type MailgunRegion string

const (
	// MailgunRegionEU sends via Mailgun's EU endpoint.
	MailgunRegionEU MailgunRegion = "eu"
	// MailgunRegionUS sends via Mailgun's US endpoint.
	MailgunRegionUS MailgunRegion = "us"
)

// MailgunConfig configures a MailgunSender.
type MailgunConfig struct {
	// Region chooses the Mailgun API endpoint. The zero value defaults to EU.
	Region MailgunRegion
}

// MailgunSender delivers email via the Mailgun HTTP API.
type MailgunSender struct {
	apiKey      string
	domain      string
	defaultFrom string
	baseURL     string
	client      *http.Client
}

// NewMailgunSender creates a Sender that uses the Mailgun API.
// apiKey is the Mailgun API key, domain is the sending domain, and
// defaultFrom is used when msg.From is empty. It defaults to Mailgun's EU endpoint.
func NewMailgunSender(apiKey, domain, defaultFrom string) *MailgunSender {
	return newMailgunSender(apiKey, domain, defaultFrom, &http.Client{}, MailgunConfig{})
}

// NewMailgunSenderWithConfig is like NewMailgunSender but accepts Mailgun-specific
// configuration, such as choosing the US endpoint instead of the default EU endpoint.
func NewMailgunSenderWithConfig(apiKey, domain, defaultFrom string, config MailgunConfig) *MailgunSender {
	return newMailgunSender(apiKey, domain, defaultFrom, &http.Client{}, config)
}

// NewMailgunSenderWithClient is like NewMailgunSender but accepts a custom
// http.Client (useful for testing with a stubbed transport).
func NewMailgunSenderWithClient(apiKey, domain, defaultFrom string, client *http.Client) *MailgunSender {
	return newMailgunSender(apiKey, domain, defaultFrom, client, MailgunConfig{})
}

func newMailgunSender(apiKey, domain, defaultFrom string, client *http.Client, config MailgunConfig) *MailgunSender {
	return &MailgunSender{
		apiKey:      apiKey,
		domain:      domain,
		defaultFrom: defaultFrom,
		baseURL:     mailgunAPIBaseURL(config.Region),
		client:      client,
	}
}

func mailgunAPIBaseURL(region MailgunRegion) string {
	switch region {
	case MailgunRegionUS:
		return mailgunAPIBaseUS
	case MailgunRegionEU, "":
		return mailgunAPIBaseEU
	default:
		return mailgunAPIBaseEU
	}
}

type mailgunErrorResp struct {
	Message string `json:"message"`
}

func (s *MailgunSender) Send(ctx context.Context, msg Message) error {
	from := msg.From
	if from == "" {
		from = s.defaultFrom
	}

	body, contentType, err := newMailgunRequestBody(from, msg)
	if err != nil {
		return fmt.Errorf("%w: encoding request: %v", ErrPermanent, err)
	}

	endpoint := strings.TrimRight(s.baseURL, "/") + "/v3/" + url.PathEscape(s.domain) + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return fmt.Errorf("%w: creating request: %v", ErrPermanent, err)
	}
	req.SetBasicAuth("api", s.apiKey)
	req.Header.Set("Content-Type", contentType)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%w: reading response: %v", ErrTransient, err)
	}

	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: mailgun API error (status %d): %s", ErrTransient, resp.StatusCode, mailgunErrorMessage(respBody))
	}

	if resp.StatusCode == 429 {
		return fmt.Errorf("%w: mailgun rate limited (status 429): %s", ErrTransient, mailgunErrorMessage(respBody))
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("%w: mailgun API error (status %d): %s", ErrPermanent, resp.StatusCode, mailgunErrorMessage(respBody))
	}

	return nil
}

func newMailgunRequestBody(from string, msg Message) (*bytes.Buffer, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	fields := []struct {
		name  string
		value string
	}{
		{name: "from", value: from},
		{name: "to", value: msg.To},
		{name: "subject", value: msg.Subject},
		{name: "text", value: msg.TextBody},
		{name: "html", value: msg.HTMLBody},
	}
	if msg.ReplyTo != "" {
		fields = append(fields, struct {
			name  string
			value string
		}{name: "h:Reply-To", value: msg.ReplyTo})
	}

	for _, field := range fields {
		if err := writer.WriteField(field.name, field.value); err != nil {
			return nil, "", err
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", err
	}

	return &body, writer.FormDataContentType(), nil
}

func mailgunErrorMessage(body []byte) string {
	var errResp mailgunErrorResp
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Message != "" {
		return errResp.Message
	}
	return strings.TrimSpace(string(body))
}
