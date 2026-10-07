// Package jira mirrors defects into Jira Cloud (BE-10.2, F-13.1).
//
// It registers behind the DefectTracker interface, alongside the built-in tracker which
// registers first and is the fallback. The rule that shapes everything here: **the
// internal defect is the source of truth, and Jira is a mirror.** A Jira outage must not
// lose a defect, so the internal record is written first, unconditionally, and this
// adapter's failure degrades to an admin alert rather than a lost bug. Removing the
// configuration reverts to the built-in with no orphaned records — the external_ref
// column simply stops being filled (BE-10.2.4).
//
// The credential is a Jira API token, read live from settings at the point of use so a
// rotation takes effect on the next call, and it never reaches a log or a ticket body.
package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability/defecttracker"
	"github.com/hyscaler/qavia/api/internal/settings"
)

// ID is this adapter's ID, stored on a mirrored defect's external_ref so a ticket found
// in Jira traces back here.
const ID = "jira"

// Settings is the configuration this adapter reads live.
type Settings interface {
	String(ctx context.Context, key string, target settings.Target) (string, error)
	Secret(ctx context.Context, key string, target settings.Target) (string, bool, error)
}

// Summariser writes a one-line ticket summary on the cheap tier. Optional: without it
// the defect's own title is used, which is a fine summary and costs nothing (BE-10.2.2).
type Summariser interface {
	SummariseDefect(ctx context.Context, projectID uuid.UUID, title, description string) (string, error)
}

// Tracker files defects in Jira.
type Tracker struct {
	settings   Settings
	summariser Summariser
	client     *http.Client
}

func NewTracker(settingsService Settings, summariser Summariser) *Tracker {
	return &Tracker{
		settings:   settingsService,
		summariser: summariser,
		client:     &http.Client{Timeout: 20 * time.Second},
	}
}

func (t *Tracker) ID() string { return ID }

// Available reports whether Jira is configured for a project. It reads global settings
// here; the per-project override is applied at the point of a call, which is where the
// project is known. Global configuration is enough to make the adapter active.
func (t *Tracker) Available(ctx context.Context) bool {
	config, err := t.read(ctx, settings.Target{})
	return err == nil && config.complete()
}

// Create files a Jira issue and returns where it landed.
func (t *Tracker) Create(ctx context.Context, ticket defecttracker.Ticket) (defecttracker.Reference, error) {
	config, err := t.read(ctx, settings.Target{ProjectID: &ticket.ProjectID})
	if err != nil {
		return defecttracker.Reference{}, err
	}
	if !config.complete() {
		return defecttracker.Reference{}, fmt.Errorf("jira: not configured for this project")
	}

	summary := ticket.Title
	if t.summariser != nil {
		if generated, err := t.summariser.SummariseDefect(ctx, ticket.ProjectID, ticket.Title, ticket.Description); err == nil && generated != "" {
			summary = generated
		}
	}
	summary = clamp(summary, 250)

	payload := map[string]any{
		"fields": map[string]any{
			"project":   map[string]any{"key": config.projectKey},
			"issuetype": map[string]any{"name": config.issueType},
			"summary":   summary,
			// The description carries the internal defect id and the links back, so a
			// person in Jira can reach the failure, the analysis, and the run here.
			"description": adf(ticket),
		},
	}

	var created struct {
		Key string `json:"key"`
	}
	if err := t.do(ctx, config, http.MethodPost, "/rest/api/3/issue", payload, &created); err != nil {
		return defecttracker.Reference{}, err
	}

	return defecttracker.Reference{
		Provider: ID,
		Key:      created.Key,
		URL:      strings.TrimRight(config.baseURL, "/") + "/browse/" + created.Key,
	}, nil
}

// UpdateStatus transitions a Jira issue to match the platform's status.
//
// Jira transitions are workflow-specific and named per project, so the adapter looks up
// the available transitions and matches by a best-effort name map. A status with no
// matching transition is left alone and reported, rather than forced: the internal
// defect is the truth, and a mirror that cannot follow is a mirror that says so.
func (t *Tracker) UpdateStatus(ctx context.Context, ref defecttracker.Reference, status defecttracker.Status) error {
	config, err := t.read(ctx, settings.Target{})
	if err != nil {
		return err
	}
	if !config.complete() {
		return fmt.Errorf("jira: not configured")
	}

	var transitions struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	if err := t.do(ctx, config, http.MethodGet, "/rest/api/3/issue/"+ref.Key+"/transitions", nil, &transitions); err != nil {
		return err
	}

	wanted := statusNames(status)
	for _, transition := range transitions.Transitions {
		if wanted[strings.ToLower(transition.To.Name)] || wanted[strings.ToLower(transition.Name)] {
			return t.do(ctx, config, http.MethodPost, "/rest/api/3/issue/"+ref.Key+"/transitions",
				map[string]any{"transition": map[string]any{"id": transition.ID}}, nil)
		}
	}
	return fmt.Errorf("jira: no transition on %s matches status %q", ref.Key, status)
}

// Comment appends to a Jira issue's thread.
func (t *Tracker) Comment(ctx context.Context, ref defecttracker.Reference, body string) error {
	config, err := t.read(ctx, settings.Target{})
	if err != nil {
		return err
	}
	if !config.complete() {
		return fmt.Errorf("jira: not configured")
	}

	return t.do(ctx, config, http.MethodPost, "/rest/api/3/issue/"+ref.Key+"/comment",
		map[string]any{"body": adfText(body)}, nil)
}

// jiraConfig is the resolved configuration for one call.
type jiraConfig struct {
	baseURL    string
	email      string
	token      string
	projectKey string
	issueType  string
}

func (c jiraConfig) complete() bool {
	return c.baseURL != "" && c.email != "" && c.token != "" && c.projectKey != ""
}

func (t *Tracker) read(ctx context.Context, target settings.Target) (jiraConfig, error) {
	baseURL, err := t.settings.String(ctx, "jira.base_url", target)
	if err != nil {
		return jiraConfig{}, fmt.Errorf("jira: read base url: %w", err)
	}
	email, err := t.settings.String(ctx, "jira.email", target)
	if err != nil {
		return jiraConfig{}, fmt.Errorf("jira: read email: %w", err)
	}
	token, _, err := t.settings.Secret(ctx, "jira.api_token", target)
	if err != nil {
		return jiraConfig{}, fmt.Errorf("jira: read token: %w", err)
	}
	projectKey, err := t.settings.String(ctx, "jira.project_key", target)
	if err != nil {
		return jiraConfig{}, fmt.Errorf("jira: read project key: %w", err)
	}
	issueType, err := t.settings.String(ctx, "jira.issue_type", target)
	if err != nil {
		return jiraConfig{}, fmt.Errorf("jira: read issue type: %w", err)
	}
	if strings.TrimSpace(issueType) == "" {
		issueType = "Bug"
	}

	return jiraConfig{
		baseURL:    strings.TrimSpace(baseURL),
		email:      strings.TrimSpace(email),
		token:      token,
		projectKey: strings.TrimSpace(projectKey),
		issueType:  strings.TrimSpace(issueType),
	}, nil
}

// do makes one Jira REST call, decoding into out when non-nil.
func (t *Tracker) do(ctx context.Context, config jiraConfig, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("jira: encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(config.baseURL, "/")+path, reader)
	if err != nil {
		return fmt.Errorf("jira: build request: %w", err)
	}

	// Basic auth with email:token, base64 in the header. The token is never on a URL or
	// in a log line.
	credential := base64.StdEncoding.EncodeToString([]byte(config.email + ":" + config.token))
	request.Header.Set("Authorization", "Basic "+credential)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := t.client.Do(request)
	if err != nil {
		return fmt.Errorf("jira: %s %s: %w", method, path, err)
	}
	defer func() { _ = response.Body.Close() }() //nolint:errcheck // best effort

	if response.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 2<<10)) //nolint:errcheck // diagnostic only
		return fmt.Errorf("jira: %s %s returned %d: %s", method, path, response.StatusCode, strings.TrimSpace(string(snippet)))
	}

	if out != nil {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			return fmt.Errorf("jira: decode response: %w", err)
		}
	}
	return nil
}

// statusNames maps the platform's status onto the Jira status names it might transition
// to. Best-effort, because Jira workflows are named per project.
func statusNames(status defecttracker.Status) map[string]bool {
	switch status {
	case defecttracker.StatusInProgress:
		return set("in progress", "in progress", "start progress")
	case defecttracker.StatusFixed:
		return set("done", "fixed", "resolved", "resolve issue", "close")
	case defecttracker.StatusWontFix:
		return set("won't do", "wont fix", "won't fix", "close")
	case defecttracker.StatusAcknowledged:
		return set("acknowledged", "to do", "open")
	default:
		return set("to do", "open", "reopen")
	}
}

func set(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

// adf builds an Atlassian Document Format body for a new issue: the description, the
// internal id, and the links.
func adf(ticket defecttracker.Ticket) map[string]any {
	content := []any{paragraph(ticket.Description)}
	content = append(content, paragraph("Qavia defect: "+ticket.ID.String()))

	for label, url := range ticket.Links {
		content = append(content, paragraph(label+": "+url))
	}

	return map[string]any{"type": "doc", "version": 1, "content": content}
}

func adfText(text string) map[string]any {
	return map[string]any{"type": "doc", "version": 1, "content": []any{paragraph(text)}}
}

func paragraph(text string) map[string]any {
	if text == "" {
		text = " "
	}
	return map[string]any{
		"type":    "paragraph",
		"content": []any{map[string]any{"type": "text", "text": text}},
	}
}

func clamp(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
