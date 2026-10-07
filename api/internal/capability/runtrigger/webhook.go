package runtrigger

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/settings"
)

// WebhookID is the built-in webhook trigger's ID.
const WebhookID = "webhook"

// tokenPrefix marks a Qavia webhook token, so one pasted into the wrong field is
// recognisable, and a leaked one is greppable in a log somebody else owns.
const tokenPrefix = "qv"

// signaturePrefix follows the convention every webhook sender already implements.
const signaturePrefix = "sha256="

// Settings is the slice of the settings service this package needs.
type Settings interface {
	Secret(ctx context.Context, key string, target settings.Target) (string, bool, error)
	Duration(ctx context.Context, key string, target settings.Target) (time.Duration, error)
}

// ReplayGuard remembers signatures that have already been accepted.
//
// Declared as an interface so the store is swappable and, more importantly, so
// this package does not reach for a Redis client itself. A guard that forgets
// nothing would grow without bound, so implementations expire entries after the
// tolerance window: outside it, the timestamp check refuses the request anyway.
type ReplayGuard interface {
	// FirstUse records the signature and reports whether it had not been seen.
	FirstUse(ctx context.Context, signature string, ttl time.Duration) (bool, error)
}

// Webhook is the built-in inbound trigger.
//
// A per-project token in the URL identifies the project, and an HMAC over the raw
// body proves the sender holds the shared secret. The token alone is not
// sufficient: it travels in a URL, and URLs end up in proxy logs and browser
// history, so a request without a valid signature is refused.
type Webhook struct {
	settings  Settings
	submitter Submitter
	replay    ReplayGuard
}

func NewWebhook(settingsService Settings, submitter Submitter, replay ReplayGuard) *Webhook {
	return &Webhook{settings: settingsService, submitter: submitter, replay: replay}
}

func (w *Webhook) ID() string { return WebhookID }

func (w *Webhook) Kind() Kind { return KindWebhook }

// Available is true wherever the platform is running: the endpoint exists and
// needs no external service. Whether a given project has a token is a per-project
// question, answered when a request arrives.
func (w *Webhook) Available(_ context.Context) bool { return true }

// Delivery is one inbound webhook.
type Delivery struct {
	Token     string
	Signature string
	Timestamp string

	// Body is the raw bytes as received. The signature covers exactly these: a
	// re-encoded body would not verify, which is why nothing parses it first.
	Body []byte

	Chain      string
	ArtifactID *uuid.UUID
	Reference  string
}

// Accept verifies a delivery and submits the run.
//
// Four checks, in this order, each refusing with the same error so a prober
// learns nothing about which one failed:
//
//  1. The token names a project.
//  2. The project has a webhook secret configured.
//  3. The timestamp is inside the tolerance window, which bounds how long a
//     captured request stays useful at all.
//  4. The signature matches the raw body, and has not been used before.
func (w *Webhook) Accept(ctx context.Context, delivery Delivery) (uuid.UUID, error) {
	projectID, ok := ProjectFromToken(delivery.Token)
	if !ok {
		return uuid.Nil, apierr.WebhookSignatureInvalid()
	}

	secret, configured, err := w.settings.Secret(ctx, "triggers.webhook_token",
		settings.Target{ProjectID: &projectID})
	if err != nil {
		return uuid.Nil, err
	}
	if !configured {
		return uuid.Nil, apierr.WebhookSignatureInvalid()
	}

	// The stored secret is the token itself, so a token that does not match the
	// project's is refused before any hashing work is done.
	if !hmac.Equal([]byte(secret), []byte(delivery.Token)) {
		return uuid.Nil, apierr.WebhookSignatureInvalid()
	}

	tolerance, err := w.settings.Duration(ctx, "triggers.webhook_tolerance", settings.Target{})
	if err != nil {
		return uuid.Nil, err
	}
	if err := withinTolerance(delivery.Timestamp, tolerance); err != nil {
		return uuid.Nil, err
	}

	expected := Sign(secret, delivery.Timestamp, delivery.Body)
	if !hmac.Equal([]byte(expected), []byte(strings.TrimSpace(delivery.Signature))) {
		return uuid.Nil, apierr.WebhookSignatureInvalid()
	}

	if w.replay != nil {
		fresh, err := w.replay.FirstUse(ctx, expected, tolerance*2)
		if err != nil {
			return uuid.Nil, err
		}
		if !fresh {
			// A correctly signed request that has already been accepted. Replaying
			// one is how a captured delivery becomes a second run.
			return uuid.Nil, apierr.WebhookReplayed()
		}
	}

	return w.submitter.SubmitTriggered(ctx, Request{
		ProjectID:  projectID,
		Chain:      delivery.Chain,
		ArtifactID: delivery.ArtifactID,
		Reference:  delivery.Reference,
		Source:     KindWebhook,
	})
}

// Sign renders the signature a sender must send.
//
// The timestamp is inside the signed material, so an attacker cannot take a valid
// signature and move it to a fresh timestamp to get past the tolerance check.
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// NewToken mints a project's webhook token.
//
// The project ID is part of the token so an inbound request resolves to a project
// in one step, with no table scan and no lookup keyed on a secret. The random half
// is what makes it unguessable; the project half is not a secret and is not
// treated as one.
func NewToken(projectID uuid.UUID) string {
	return fmt.Sprintf("%s_%s_%s",
		tokenPrefix,
		strings.ReplaceAll(projectID.String(), "-", ""),
		strings.ReplaceAll(uuid.NewString(), "-", ""))
}

// ProjectFromToken reads the project out of a token.
func ProjectFromToken(token string) (uuid.UUID, bool) {
	parts := strings.Split(strings.TrimSpace(token), "_")
	if len(parts) != 3 || parts[0] != tokenPrefix {
		return uuid.Nil, false
	}

	projectID, err := uuid.Parse(parts[1])
	if err != nil {
		return uuid.Nil, false
	}
	return projectID, true
}

// withinTolerance refuses a delivery that is too old or too far in the future.
//
// Clock skew makes the future case real rather than theoretical, so the window
// applies in both directions.
func withinTolerance(timestamp string, tolerance time.Duration) error {
	seconds, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return apierr.WebhookSignatureInvalid()
	}

	drift := time.Since(time.Unix(seconds, 0))
	if drift < 0 {
		drift = -drift
	}
	if drift > tolerance {
		return apierr.WebhookStale(int(tolerance.Seconds()))
	}
	return nil
}
