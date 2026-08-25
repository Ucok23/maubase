// Package email sends transactional email — currently just password
// reset links (internal/auth's CreateResetToken/ResetPassword, spec/
// password-reset.md). Sender is deliberately a one-method interface so
// the rest of the codebase never depends on Resend specifically; a
// self-hosted deployment that wants a different provider only has to
// implement Send.
package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Sender interface {
	// Send delivers one email. html is the full body — callers build
	// their own markup; this package doesn't template anything.
	Send(ctx context.Context, to, subject, html string) error
}

// ResendSender sends mail through Resend's HTTP API
// (https://resend.com/docs/api-reference/emails/send-email) — no SDK,
// just a JSON POST, matching this project's general preference for a
// direct dependency over a wrapping client library where the API is
// this small.
type ResendSender struct {
	apiKey     string
	from       string
	httpClient *http.Client
}

// NewResendSender builds a sender that authenticates as apiKey and sends
// every message from address from (Resend requires a verified sending
// domain for this in production; a *.resend.dev address works
// unverified, for testing).
func NewResendSender(apiKey, from string) *ResendSender {
	return &ResendSender{apiKey: apiKey, from: from, httpClient: &http.Client{Timeout: 10 * time.Second}}
}

func (s *ResendSender) Send(ctx context.Context, to, subject, html string) error {
	body, err := json.Marshal(map[string]any{
		"from":    s.from,
		"to":      []string{to},
		"subject": subject,
		"html":    html,
	})
	if err != nil {
		return fmt.Errorf("marshal resend request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build resend request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send via resend: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("resend returned %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// NoopSender is what a deployment gets when no email provider is
// configured (MAUBASE_RESEND_API_KEY/MAUBASE_EMAIL_FROM unset) — Send
// always fails with a clear, actionable error, so misconfiguration
// surfaces the first time anyone requests a password reset rather than
// silently swallowing it (or, worse, silently "succeeding" with no email
// ever sent).
type NoopSender struct{}

func (NoopSender) Send(context.Context, string, string, string) error {
	return fmt.Errorf("no email sender configured — set MAUBASE_RESEND_API_KEY and MAUBASE_EMAIL_FROM")
}
