package sendmail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
)

// ResendSender delivers email via the Resend HTTP API.
type ResendSender struct {
	apiKey      string
	defaultFrom string
	baseURL     string
	client      *http.Client
}

// NewResendSender creates a Sender that uses the Resend API.
// apiKey is the Resend API key; defaultFrom is used when msg.From is empty.
func NewResendSender(apiKey, defaultFrom string) *ResendSender {
	return &ResendSender{
		apiKey:      apiKey,
		defaultFrom: defaultFrom,
		baseURL:     "https://api.resend.com",
		client:      &http.Client{},
	}
}

// NewResendSenderWithClient is like NewResendSender but accepts a custom
// http.Client (useful for testing with a stubbed transport).
func NewResendSenderWithClient(apiKey, defaultFrom string, client *http.Client) *ResendSender {
	return &ResendSender{
		apiKey:      apiKey,
		defaultFrom: defaultFrom,
		baseURL:     "https://api.resend.com",
		client:      client,
	}
}

type resendRequest struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	Html    string `json:"html"`
	Text    string `json:"text"`
	ReplyTo string `json:"reply_to,omitempty"`
}

type resendErrorResp struct {
	Message string `json:"message"`
}

func (s *ResendSender) Send(ctx context.Context, msg Message) error {
	from := msg.From
	if from == "" {
		from = s.defaultFrom
	}

	payload := resendRequest{
		From:    from,
		To:      msg.To,
		Subject: msg.Subject,
		Html:    msg.HTMLBody,
		Text:    msg.TextBody,
		ReplyTo: msg.ReplyTo,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: marshalling request: %v", ErrPermanent, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/emails", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: creating request: %v", ErrPermanent, err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return fmt.Errorf("%w: %v", ErrTransient, err)
		}
		return fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := readProviderResponse(resp.Body)
	if err != nil {
		return fmt.Errorf("resend: %w", err)
	}

	if resp.StatusCode >= 500 {
		var errResp resendErrorResp
		_ = json.Unmarshal(respBody, &errResp)
		return fmt.Errorf("%w: resend API error (status %d): %s", ErrTransient, resp.StatusCode, limitProviderDetail(errResp.Message))
	}

	if resp.StatusCode == 429 {
		var errResp resendErrorResp
		_ = json.Unmarshal(respBody, &errResp)
		return fmt.Errorf("%w: resend rate limited (status 429): %s", ErrTransient, limitProviderDetail(errResp.Message))
	}

	if resp.StatusCode >= 400 {
		var errResp resendErrorResp
		_ = json.Unmarshal(respBody, &errResp)
		return fmt.Errorf("%w: resend API error (status %d): %s", ErrPermanent, resp.StatusCode, limitProviderDetail(errResp.Message))
	}

	return nil
}
