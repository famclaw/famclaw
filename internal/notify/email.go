package notify

import (
	"context"
	"fmt"
	"mime"
	"net/smtp"
	"strings"
	"html/template"

	"github.com/famclaw/famclaw/internal/config"
	"github.com/famclaw/famclaw/internal/store"
)

// sanitizeHeader strips CR/LF to prevent email header injection.
func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return s
}

// sanitizeInput strips CR/LF from user-controlled values to prevent
// injection without collapsing the HTML template's legitimate newlines.
func sanitizeInput(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// EmailNotifier sends notifications via SMTP email.
type EmailNotifier struct {
	cfg config.EmailConfig
}

// NewEmailNotifier creates a new EmailNotifier.
func NewEmailNotifier(cfg config.EmailConfig) *EmailNotifier {
	return &EmailNotifier{cfg: cfg}
}

func (e *EmailNotifier) Notify(ctx context.Context, a *store.Approval, approveURL, denyURL string) error {
	subject := fmt.Sprintf("FamClaw: %s needs approval for %q", a.UserDisplay, a.Category)
	body := formatApprovalHTML(a, approveURL, denyURL)
	return e.send(subject, body)
}

func (e *EmailNotifier) NotifyDecision(ctx context.Context, a *store.Approval) error {
	subject := fmt.Sprintf("FamClaw: Request from %s %s", a.UserDisplay, a.Status)
	body := formatDecisionHTML(a)
	return e.send(subject, body)
}

func (e *EmailNotifier) send(subject, body string) error {
	addr := fmt.Sprintf("%s:%d", e.cfg.SMTPHost, e.cfg.SMTPPort)
	auth := smtp.PlainAuth("", e.cfg.From, e.cfg.Password, e.cfg.SMTPHost)

	for _, to := range e.cfg.To {
		safeSubject := mime.QEncoding.Encode("utf-8", sanitizeHeader(subject))
		safeFrom := sanitizeHeader(e.cfg.From)
		safeTo := sanitizeHeader(to)
		msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s",
			safeFrom, safeTo, safeSubject, body)
		if err := smtp.SendMail(addr, auth, e.cfg.From, []string{to}, []byte(msg)); err != nil {
			return fmt.Errorf("sending email to %s: %w", to, err)
		}
	}
	return nil
}

func formatApprovalHTML(a *store.Approval, approveURL, denyURL string) string {
	esc := func(s string) string { return template.HTMLEscapeString(sanitizeInput(s)) }
	return fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="utf-8"></head>
<body style="font-family:system-ui;max-width:480px;margin:0 auto;padding:20px">
<h2>FamClaw Approval Request</h2>
<p><strong>%s</strong> (%s) wants to ask about a topic that needs your approval.</p>
<table style="border-collapse:collapse;width:100%%">
<tr><td style="padding:8px;border:1px solid #ddd"><strong>Category</strong></td><td style="padding:8px;border:1px solid #ddd">%s</td></tr>
<tr><td style="padding:8px;border:1px solid #ddd"><strong>Question</strong></td><td style="padding:8px;border:1px solid #ddd">%s</td></tr>
<tr><td style="padding:8px;border:1px solid #ddd"><strong>Age Group</strong></td><td style="padding:8px;border:1px solid #ddd">%s</td></tr>
</table>
<div style="margin:24px 0">
<a href="%s" style="background:#22c55e;color:#fff;padding:12px 24px;border-radius:8px;text-decoration:none;margin-right:12px">Approve</a>
<a href="%s" style="background:#ef4444;color:#fff;padding:12px 24px;border-radius:8px;text-decoration:none">Deny</a>
</div>
<p style="color:#6b7280;font-size:13px">This link expires in 24 hours.</p>
</body></html>`, esc(a.UserDisplay), esc(a.AgeGroup), esc(a.Category), esc(a.QueryText), esc(a.AgeGroup), esc(approveURL), esc(denyURL))
}

func formatDecisionHTML(a *store.Approval) string {
	esc := func(s string) string { return template.HTMLEscapeString(sanitizeInput(s)) }
	return fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="utf-8"></head>
<body style="font-family:system-ui;max-width:480px;margin:0 auto;padding:20px">
<h2>FamClaw Decision</h2>
<p>FamClaw: %s's request about %q has been %s by %s.</p>
</body></html>`,
		esc(a.UserDisplay), esc(a.Category), esc(a.Status), esc(a.DecidedBy),
	)
}
