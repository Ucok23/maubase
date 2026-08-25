package email

import (
	"context"
	"sync"
)

// FakeSender records every email it's asked to send instead of actually
// sending anything, for tests that need to verify what maubase tried to
// send (a password-reset link, say — see test/password_reset_test.go)
// without depending on network access or a real provider.
type FakeSender struct {
	mu   sync.Mutex
	sent []SentEmail
}

type SentEmail struct {
	To      string
	Subject string
	HTML    string
}

func NewFakeSender() *FakeSender {
	return &FakeSender{}
}

func (f *FakeSender) Send(_ context.Context, to, subject, html string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, SentEmail{To: to, Subject: subject, HTML: html})
	return nil
}

// Sent returns every email recorded so far, oldest first.
func (f *FakeSender) Sent() []SentEmail {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]SentEmail, len(f.sent))
	copy(out, f.sent)
	return out
}
