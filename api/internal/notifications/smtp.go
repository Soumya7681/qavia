package notifications

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/hyscaler/qavia/api/internal/capability/notifier"
	"github.com/hyscaler/qavia/api/internal/settings"
)

// The SMTP notifier (BE-10.4, F-13.5).
//
// It registers behind the Notifier interface alongside the in-app channel, which always
// runs. That is the whole point of the capability registry here: this adapter is
// allowed to fail. When it does, the message is already in the notification centre, the
// failure is recorded, and an admin is alerted — so breaking the SMTP credentials
// mid-flight loses no notification, it only stops the emails until someone fixes them
// (BE-10.4.3).
//
// Unconfigured is not an error. An installation with no SMTP host is a normal
// installation using the in-app channel, and Available reports false so the service
// never tries to send through it (BE-10.4.2).

// SMTPID is this adapter's ID.
const SMTPID = "smtp"

// SMTPSettings is the configuration this adapter reads, live, at send time. Read each
// time rather than cached, so a credential change takes effect on the next send rather
// than the next restart.
type SMTPSettings interface {
	String(ctx context.Context, key string, target settings.Target) (string, error)
	Int(ctx context.Context, key string, target settings.Target) (int, error)
	Secret(ctx context.Context, key string, target settings.Target) (string, bool, error)
}

// SMTP delivers email.
type SMTP struct {
	settings SMTPSettings

	// baseURL turns a message's path into an absolute link. Emails are read outside
	// the app, so a relative link is no link (FR-8.5).
	baseURL string
}

func NewSMTP(settingsService SMTPSettings, baseURL string) *SMTP {
	return &SMTP{settings: settingsService, baseURL: strings.TrimRight(baseURL, "/")}
}

func (s *SMTP) ID() string { return SMTPID }

func (s *SMTP) Channel() notifier.Channel { return notifier.ChannelEmail }

// Available reports whether a host is configured. An unconfigured SMTP is the normal
// state of a fresh install, not a fault (BE-10.4.2).
func (s *SMTP) Available(ctx context.Context) bool {
	host, err := s.settings.String(ctx, "notifications.smtp_host", settings.Target{})
	return err == nil && strings.TrimSpace(host) != ""
}

// Send delivers one email.
func (s *SMTP) Send(ctx context.Context, to notifier.Recipient, msg notifier.Message) error {
	if to.Email == "" {
		return fmt.Errorf("smtp: recipient %s has no email address", to.UserID)
	}

	config, err := s.read(ctx)
	if err != nil {
		return err
	}

	body := s.render(to, msg, config.from)
	address := net.JoinHostPort(config.host, fmt.Sprintf("%d", config.port))

	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("smtp: dial %s: %w", address, err)
	}

	client, err := smtp.NewClient(conn, config.host)
	if err != nil {
		_ = conn.Close() //nolint:errcheck // best effort on an already-failing path
		return fmt.Errorf("smtp: greet %s: %w", config.host, err)
	}
	defer func() { _ = client.Close() }() //nolint:errcheck // best effort

	// STARTTLS when the server offers it, which every modern relay on 587 does. A
	// plaintext fallback is deliberately not offered: sending a credential in the clear
	// is worse than not sending the email.
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: config.host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("smtp: start TLS: %w", err)
		}
	}

	if config.username != "" {
		auth := smtp.PlainAuth("", config.username, config.password, config.host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp: authenticate: %w", err)
		}
	}

	if err := client.Mail(config.from); err != nil {
		return fmt.Errorf("smtp: set sender: %w", err)
	}
	if err := client.Rcpt(to.Email); err != nil {
		return fmt.Errorf("smtp: set recipient: %w", err)
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp: open data: %w", err)
	}
	if _, err := writer.Write([]byte(body)); err != nil {
		return fmt.Errorf("smtp: write body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("smtp: close data: %w", err)
	}

	return client.Quit()
}

type smtpConfig struct {
	host     string
	port     int
	username string
	password string
	from     string
}

func (s *SMTP) read(ctx context.Context) (smtpConfig, error) {
	global := settings.Target{}

	host, err := s.settings.String(ctx, "notifications.smtp_host", global)
	if err != nil {
		return smtpConfig{}, fmt.Errorf("smtp: read host: %w", err)
	}
	port, err := s.settings.Int(ctx, "notifications.smtp_port", global)
	if err != nil {
		return smtpConfig{}, fmt.Errorf("smtp: read port: %w", err)
	}
	username, err := s.settings.String(ctx, "notifications.smtp_username", global)
	if err != nil {
		return smtpConfig{}, fmt.Errorf("smtp: read username: %w", err)
	}
	password, _, err := s.settings.Secret(ctx, "notifications.smtp_password", global)
	if err != nil {
		return smtpConfig{}, fmt.Errorf("smtp: read password: %w", err)
	}
	from, err := s.settings.String(ctx, "notifications.from_address", global)
	if err != nil {
		return smtpConfig{}, fmt.Errorf("smtp: read from address: %w", err)
	}
	if strings.TrimSpace(from) == "" {
		from = username
	}

	return smtpConfig{host: host, port: port, username: username, password: password, from: from}, nil
}

// render builds a minimal, well-formed email. Plain text, because a notification is a
// title, a sentence, and a link, and an HTML email framework would be weight for
// nothing.
func (s *SMTP) render(to notifier.Recipient, msg notifier.Message, from string) string {
	var builder strings.Builder

	builder.WriteString("From: " + from + "\r\n")
	builder.WriteString("To: " + to.Email + "\r\n")
	builder.WriteString("Subject: " + sanitiseHeader(msg.Title) + "\r\n")
	builder.WriteString("MIME-Version: 1.0\r\n")
	builder.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	builder.WriteString("\r\n")

	if msg.Body != "" {
		builder.WriteString(msg.Body + "\r\n\r\n")
	}
	if msg.Link != "" && s.baseURL != "" {
		builder.WriteString(s.baseURL + msg.Link + "\r\n")
	}
	return builder.String()
}

// sanitiseHeader strips the CR and LF that would let a title inject extra headers. A
// notification title is model- or user-influenced text, and a subject line is a header.
func sanitiseHeader(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
}
