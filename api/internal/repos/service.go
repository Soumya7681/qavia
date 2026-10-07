// Package repos owns a project's repository connection and what can be learned from
// the checkout deterministically (F-3.4, F-3.5, F-6.7).
//
// The phase it belongs to is the one where this platform starts reading a client's
// source code, so two rules are set here rather than later:
//
//   - **A repository URL is a URL a user supplied.** It is checked against the same
//     allowlist and the same private-address rules as a test target, because a worker
//     cloning it sits inside the network and "clone this" is as good an SSRF primitive
//     as "test this" (BE-4.7).
//   - **Detection is deterministic.** Which test framework a repository uses is
//     written in its own files, and a model asked to guess would be right most of the
//     time, which is the worst possible accuracy for something every later stage
//     depends on (BE-6.3).
package repos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Provider is where a repository lives.
type Provider string

const (
	// ProviderGit is clone-by-URL: the only connected path in this phase, and the one
	// that needs no OAuth app, no installation, and no vendor.
	ProviderGit Provider = "git"

	ProviderGitHub    Provider = "github"
	ProviderGitLab    Provider = "gitlab"
	ProviderBitbucket Provider = "bitbucket"

	// ProviderArchive is the built-in: somebody uploaded a zip.
	ProviderArchive Provider = "archive"
)

func (p Provider) Valid() bool {
	switch p {
	case ProviderGit, ProviderGitHub, ProviderGitLab, ProviderBitbucket, ProviderArchive:
		return true
	default:
		return false
	}
}

// Implemented reports whether this phase can actually fetch from the provider.
//
// The others are declared so the API and the UI agree about what exists, and asking
// for one returns a reason rather than nothing (F-3.12).
func (p Provider) Implemented() bool {
	return p == ProviderGit || p == ProviderArchive
}

// Connection is a project's repository.
type Connection struct {
	ID        uuid.UUID
	ProjectID uuid.UUID

	Provider      Provider
	URL           string
	DefaultBranch string

	// CredentialRef is the settings key holding the access token, or empty for a
	// public repository. A name, never the secret.
	CredentialRef string

	LastCommit    string
	LastFetchedAt *time.Time
	LastError     string

	// Detected is what the last sync read out of the repository's own manifests, and
	// DetectedAt says when. Nil means nobody has looked yet, which is a different
	// state from "looked and found nothing" (BE-6.3.3).
	Detected   Stack
	DetectedAt *time.Time

	CreatedBy *uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// HasCredential reports whether a token is configured, without reading it.
func (c Connection) HasCredential() bool { return c.CredentialRef != "" }

// Targets is the allowlist and SSRF check, applied to a repository URL for the same
// reason it is applied to a test target.
type Targets interface {
	CheckURL(ctx context.Context, projectID uuid.UUID, raw string) (checked Target, err error)
}

// Target is what the check returns, narrowed to what this package uses.
type Target interface {
	// Hostname is the approved host, for the audit trail.
	Hostname() string
}

// Settings is the slice of the settings service this package needs.
type Settings interface {
	Int(ctx context.Context, key string, target settings.Target) (int, error)
	String(ctx context.Context, key string, target settings.Target) (string, error)
	Duration(ctx context.Context, key string, target settings.Target) (time.Duration, error)
	Secret(ctx context.Context, key string, target settings.Target) (string, bool, error)
}

// Cloner is what a process needs in order to materialise source.
//
// Optional, and the API process deliberately does not have it: cloning needs git, a
// disk, and the token, and none of those belong in the process serving requests
// (BE-6.1.3).
type Cloner struct {
	// Fetchers are keyed by provider, so adding GitHub in phase 10 is a registration
	// rather than a change to the clone path.
	Fetchers map[Provider]Fetcher

	// Target checks a repository URL against the project's allowlist, the same way a
	// run's target is checked.
	Target TargetCheck

	// Root is where per-job checkouts are created. Empty uses the system temporary
	// directory.
	Root string
}

// Service owns repository connections.
type Service struct {
	db       *store.DB
	settings Settings

	cloner Cloner
}

// Option configures the service.
type Option func(*Service)

// WithCloner gives the service what it needs to materialise source. Only the worker
// passes it.
func WithCloner(cloner Cloner) Option {
	return func(s *Service) { s.cloner = cloner }
}

func NewService(db *store.DB, config Settings, options ...Option) *Service {
	service := &Service{db: db, settings: config}
	for _, option := range options {
		option(service)
	}
	return service
}

// CredentialKey is the settings key a project's repository token lives under.
//
// One key, project-scoped, rather than a column: the settings store already owns
// encryption at rest, rotation, and the rule that a secret is never returned on read
// (F-1.8). A second home for secrets would need all three again.
//
//nolint:gosec // G101: a settings key, not a credential
const CredentialKey = "repo.access_token"

// ConnectInput is a repository being attached to a project.
type ConnectInput struct {
	ProjectID uuid.UUID
	Provider  Provider
	URL       string
	Branch    string

	// HasToken records that a token was stored under CredentialKey by the caller.
	// The token itself never passes through this package.
	HasToken bool

	CreatedBy *uuid.UUID
}

// Connect attaches or replaces a project's repository.
func (s *Service) Connect(ctx context.Context, input ConnectInput) (Connection, error) {
	if input.Provider == "" {
		input.Provider = ProviderGit
	}
	if !input.Provider.Valid() {
		return Connection{}, apierr.Validation(
			fmt.Sprintf("Provider %q is not one this platform knows.", input.Provider),
			map[string]any{"field": "provider"})
	}
	if !input.Provider.Implemented() {
		// Declared but not delivered. Named rather than silently accepted, so a UI can
		// disable it with a reason instead of hiding it (F-3.12).
		return Connection{}, apierr.NotImplemented(
			fmt.Sprintf("%s as a repository provider", input.Provider))
	}

	if input.Provider != ProviderArchive {
		if err := validateCloneURL(input.URL); err != nil {
			return Connection{}, err
		}
	}

	credentialRef := ""
	if input.HasToken {
		credentialRef = CredentialKey
	}

	row, err := s.db.Queries().UpsertRepoConnection(ctx, dbgen.UpsertRepoConnectionParams{
		ProjectID:     input.ProjectID,
		Provider:      dbgen.RepoProvider(input.Provider),
		RepoUrl:       strings.TrimSpace(input.URL),
		DefaultBranch: strings.TrimSpace(input.Branch),
		CredentialRef: credentialRef,
		CreatedBy:     input.CreatedBy,
	})
	if err != nil {
		return Connection{}, apierr.Internal(fmt.Errorf("store the repository connection: %w", err))
	}
	return toConnection(row), nil
}

// Get reads a project's connection.
func (s *Service) Get(ctx context.Context, projectID uuid.UUID) (Connection, bool, error) {
	row, err := s.db.Queries().GetRepoConnection(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Not an error: a project with no repository is a project that has not
			// connected one, and every consumer of this has to work without it.
			return Connection{}, false, nil
		}
		return Connection{}, false, apierr.Internal(
			fmt.Errorf("read the repository connection: %w", err))
	}
	return toConnection(row), true, nil
}

// Disconnect removes a project's connection.
func (s *Service) Disconnect(ctx context.Context, projectID uuid.UUID) error {
	if _, err := s.db.Queries().DeleteRepoConnection(ctx, projectID); err != nil {
		return apierr.Internal(fmt.Errorf("remove the repository connection: %w", err))
	}
	return nil
}

// Token reads the project's access token, or empty when there is none.
//
// Read at the moment of a clone and held no longer. The settings service owns the
// decryption; this is the only place in the phase that asks for the plaintext.
func (s *Service) Token(ctx context.Context, projectID uuid.UUID) (string, error) {
	token, found, err := s.settings.Secret(ctx, CredentialKey,
		settings.Target{ProjectID: &projectID})
	if err != nil {
		return "", apierr.Internal(fmt.Errorf("read the repository token: %w", err))
	}
	if !found {
		return "", nil
	}
	return token, nil
}

// RecordFetch stores what a clone got.
func (s *Service) RecordFetch(ctx context.Context, projectID uuid.UUID, commit string) error {
	if err := s.db.Queries().RecordRepoFetch(ctx, dbgen.RecordRepoFetchParams{
		ProjectID: projectID, LastCommit: commit,
	}); err != nil {
		return apierr.Internal(fmt.Errorf("record the fetch: %w", err))
	}
	return nil
}

// RecordFetchError stores why a clone failed, so a project page can say so rather
// than showing a repository that looks connected and never works.
func (s *Service) RecordFetchError(ctx context.Context, projectID uuid.UUID, reason string) error {
	if err := s.db.Queries().RecordRepoFetchError(ctx, dbgen.RecordRepoFetchErrorParams{
		ProjectID: projectID, LastError: reason,
	}); err != nil {
		return apierr.Internal(fmt.Errorf("record the fetch error: %w", err))
	}
	return nil
}

// Archive names an uploaded source archive.
type Archive struct {
	ID         uuid.UUID
	Filename   string
	StorageKey string
}

// LatestArchive is the newest source upload for a project, for an archive-backed
// sync.
func (s *Service) LatestArchive(ctx context.Context, projectID uuid.UUID) (Archive, bool, error) {
	row, err := s.db.Queries().LatestSourceArchive(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Archive{}, false, nil
		}
		return Archive{}, false, apierr.Internal(fmt.Errorf("read the source archive: %w", err))
	}
	return Archive{ID: row.ID, Filename: row.Filename, StorageKey: row.StorageKey}, true, nil
}

// stackOverride reads the project's forced framework, or empty.
func (s *Service) stackOverride(ctx context.Context, projectID uuid.UUID) (string, error) {
	value, err := s.settings.String(ctx, "repo.stack_override",
		settings.Target{ProjectID: &projectID})
	if err != nil {
		return "", apierr.Internal(fmt.Errorf("read the stack override: %w", err))
	}
	return strings.TrimSpace(value), nil
}

// CloneOptions are the settings a clone uses.
type CloneOptions struct {
	Depth   int
	Timeout time.Duration
	Quota   int64
}

// CloneOptionsFor reads them for a project.
func (s *Service) CloneOptionsFor(ctx context.Context, projectID uuid.UUID) (CloneOptions, error) {
	scope := settings.Target{ProjectID: &projectID}

	depth, err := s.settings.Int(ctx, "repo.clone_depth", scope)
	if err != nil {
		return CloneOptions{}, apierr.Internal(fmt.Errorf("read the clone depth: %w", err))
	}
	timeout, err := s.settings.Duration(ctx, "repo.clone_timeout", scope)
	if err != nil {
		return CloneOptions{}, apierr.Internal(fmt.Errorf("read the clone timeout: %w", err))
	}
	quota, err := s.settings.Int(ctx, "repo.max_size_mib", scope)
	if err != nil {
		return CloneOptions{}, apierr.Internal(fmt.Errorf("read the repository size limit: %w", err))
	}

	return CloneOptions{
		Depth:   depth,
		Timeout: timeout,
		Quota:   int64(quota) << 20,
	}, nil
}

// validateCloneURL refuses what cannot be cloned safely.
//
// The scheme check is the load-bearing part. `file://` would clone a path on the
// worker, `ssh://` would use a key this platform does not manage, and an `ext::`
// transport runs an arbitrary command — git's own remote helpers are a code-execution
// surface, and the only two worth accepting here are HTTP and HTTPS.
func validateCloneURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return apierr.Validation("A repository connection needs a URL.",
			map[string]any{"field": "url"})
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return apierr.Validation(
			"That is not a URL this platform can clone. Expected something like https://github.com/acme/service.git.",
			map[string]any{"field": "url"})
	}

	switch parsed.Scheme {
	case "http", "https":
	default:
		return apierr.Validation(
			fmt.Sprintf("Repository scheme %q is not supported. Use https, or upload an archive.",
				parsed.Scheme),
			map[string]any{"field": "url", "scheme": parsed.Scheme})
	}

	if parsed.User != nil {
		// A URL carrying its own credentials would put a secret in a database column,
		// in every log line that mentions the repository, and in `ps` output during the
		// clone. The token belongs in the settings store.
		return apierr.Validation(
			"Remove the credentials from the URL and store the token in the project's settings instead.",
			map[string]any{"field": "url"})
	}

	return nil
}

func toConnection(row dbgen.RepoConnection) Connection {
	stack := Stack{}
	if len(row.Detected) > 0 {
		if err := json.Unmarshal(row.Detected, &stack); err != nil {
			// A detection blob that no longer parses is treated as "not looked yet"
			// rather than failing a read: the next sync overwrites it.
			stack = Stack{}
		}
	}

	return Connection{
		Detected:      stack,
		DetectedAt:    row.DetectedAt,
		ID:            row.ID,
		ProjectID:     row.ProjectID,
		Provider:      Provider(row.Provider),
		URL:           row.RepoUrl,
		DefaultBranch: row.DefaultBranch,
		CredentialRef: row.CredentialRef,
		LastCommit:    row.LastCommit,
		LastFetchedAt: row.LastFetchedAt,
		LastError:     row.LastError,
		CreatedBy:     row.CreatedBy,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}
