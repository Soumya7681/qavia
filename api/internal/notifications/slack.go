package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hyscaler/qavia/api/internal/capability/notifier"
	"github.com/hyscaler/qavia/api/internal/settings"
)

// The Slack notifier (BE-10.4, F-13.3).
//
// Like SMTP, it registers behind the Notifier interface beside the always-on in-app
// channel, so it is allowed to fail: the message is already in the notification centre,
// and a Slack outage degrades to an admin alert rather than a lost run summary
// (BE-10.4.3).
//
// It posts to one workspace channel rather than per user, because a run summary is a
// team fact and Slack is where teams read them. A per-user DM would need every person's
// Slack ID mapped, which is configuration nobody keeps current; a channel is one
// setting and it is where the conversation already happens.

// SlackID is this adapter's ID.
const SlackID = "slack"

// SlackSettings is the configuration this adapter reads live at send time.
type SlackSettings interface {
	String(ctx context.Context, key string, target settings.Target) (string, error)
	Secret(ctx context.Context, key string, target settings.Target) (string, bool, error)
}

// Slack posts messages to a channel.
type Slack struct {
	settings SlackSettings
	baseURL  string
	client   *http.Client
}

func NewSlack(settingsService SlackSettings, baseURL string) *Slack {
	return &Slack{
		settings: settingsService,
		baseURL:  strings.TrimRight(baseURL, "/"),
		client:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *Slack) ID() string { return SlackID }

func (s *Slack) Channel() notifier.Channel { return notifier.ChannelSlack }

// Available reports whether a token and a channel are configured.
func (s *Slack) Available(ctx context.Context) bool {
	token, _, err := s.settings.Secret(ctx, "notifications.slack_token", settings.Target{})
	if err != nil || strings.TrimSpace(token) == "" {
		return false
	}
	channel, err := s.settings.String(ctx, "notifications.slack_channel", settings.Target{})
	return err == nil && strings.TrimSpace(channel) != ""
}

// Send posts one message.
//
// Deduplicated across recipients is not this adapter's job: the service fans out per
// recipient, and a channel post per team member would spam the channel. In practice a
// caller notifying a whole team through Slack wants one post, and the platform's Slack
// use is run summaries, which the run notifier sends once. Sending per recipient here is
// acceptable because the volume is low; if it grows, the fix is a channel-scoped path in
// the service, not a change here.
func (s *Slack) Send(ctx context.Context, _ notifier.Recipient, msg notifier.Message) error {
	global := settings.Target{}

	token, _, err := s.settings.Secret(ctx, "notifications.slack_token", global)
	if err != nil {
		return fmt.Errorf("slack: read token: %w", err)
	}
	channel, err := s.settings.String(ctx, "notifications.slack_channel", global)
	if err != nil {
		return fmt.Errorf("slack: read channel: %w", err)
	}

	payload, err := json.Marshal(map[string]any{
		"channel": channel,
		"text":    s.render(msg),
	})
	if err != nil {
		return fmt.Errorf("slack: encode message: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://slack.com/api/chat.postMessage", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("slack: build request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")

	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("slack: post message: %w", err)
	}
	defer func() { _ = response.Body.Close() }() //nolint:errcheck // best effort

	// Slack answers 200 with {"ok": false, "error": "..."} for a rejected message, so
	// the status code alone is not enough: a bad token is a 200. The body decides.
	var reply struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		return fmt.Errorf("slack: read reply: %w", err)
	}
	if !reply.OK {
		return fmt.Errorf("slack: message rejected: %s", reply.Error)
	}
	return nil
}

// render builds the message text with an absolute link Slack can make clickable.
func (s *Slack) render(msg notifier.Message) string {
	var builder strings.Builder

	builder.WriteString("*" + slackEscape(msg.Title) + "*")
	if msg.Body != "" {
		builder.WriteString("\n" + slackEscape(msg.Body))
	}
	if msg.Link != "" && s.baseURL != "" {
		builder.WriteString("\n" + s.baseURL + msg.Link)
	}
	return builder.String()
}

// slackEscape neutralises the three characters Slack treats as markup control, so a
// title containing them reads as text rather than breaking the formatting.
func slackEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}
