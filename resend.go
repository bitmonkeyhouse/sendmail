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

// NewResendSender creates a ResendSender that uses the Resend API.
// apiKey is the Resend API key and defaultFrom is used when msg.From is empty.
// It does not validate apiKey and uses an http.Client with no timeout, so callers
// should pass a context with a deadline to Send.
func NewResendSender(apiKey, defaultFrom string) *ResendSender {
	return &ResendSender{
		apiKey:      apiKey,
		defaultFrom: defaultFrom,
		baseURL:     "https://api.resend.com",
		client:      &http.Client{},
	}
}

// NewResendSenderWithClient is like NewResendSender but uses client for HTTP
// requests, which is useful for testing with a stubbed transport. client must be
// non-nil and is used as-is; a client without its own Timeout relies on the
// context passed to Send for deadlines.
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

// Send delivers msg through the Resend API. If msg.From is empty, the sender's
// defaultFrom is used.
//
// It returns an error wrapping ErrTransient for transport failures, 5xx
// responses, rate limiting (HTTP 429), and response-read failures (including
// oversized bodies), and one wrapping ErrPermanent for other 4xx responses and
// request-construction failures.
// Cancelling ctx aborts the request; the resulting transport error is wrapped
// as ErrTransient but does not necessarily wrap ctx.Err().
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
