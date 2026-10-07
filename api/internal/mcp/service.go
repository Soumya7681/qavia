package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Transport is how the platform reaches a server.
type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
)

// Scope decides which projects may use a server.
type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeProject Scope = "project"
)

// Health is a server's last connection result.
type Health string

const (
	HealthUnknown     Health = "unknown"
	HealthHealthy     Health = "healthy"
	HealthUnreachable Health = "unreachable"
)

// Server is one configured MCP server.
type Server struct {
	ID   uuid.UUID
	Name string

	Transport Transport
	Command   string
	Args      []string
	URL       string

	Scope   Scope
	ScopeID *uuid.UUID

	// EnabledTools is the allowlist. Deny-all: a tool not in this list is refused
	// before it ever reaches the server (BE-10.1.3).
	EnabledTools []string

	// DiscoveredTools is what the last connection test found, so the allowlist screen
	// can show what is available.
	DiscoveredTools []Tool

	// AutoWrite permits writes without a per-call confirmation. Off by default: an
	// agent must not silently file twenty tickets (BE-10.7).
	AutoWrite bool

	IsEnabled       bool
	HealthStatus    Health
	HealthDetail    string
	HealthCheckedAt *time.Time

	CreatedBy *uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time

	// HasCredentials is true when a token is stored. The token itself is never on this
	// struct: it is decrypted at the point of use, in Config, and nowhere else.
	HasCredentials bool
}

// Tool is one capability a server exposes.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	// Write marks a tool that changes state, so the write-safety rules can act on it
	// without re-deriving intent from the name (BE-10.7).
	Write bool `json:"write,omitempty"`
}

// ToolAllowed reports whether a tool may be called on this server. Deny-all: an empty
// allowlist allows nothing, which is the opposite of the schema doc's shorthand and the
// safe reading of it (BE-10.1.3).
func (s Server) ToolAllowed(tool string) bool {
	for _, allowed := range s.EnabledTools {
		if allowed == tool {
			return true
		}
	}
	return false
}

// Cipher is the slice of the settings cipher this package needs.
type Cipher interface {
	Encrypt(settingKey, plaintext string) (settings.Secret, error)
	Decrypt(settingKey string, secret settings.Secret) (string, error)
}

// Service owns the MCP registry.
type Service struct {
	db     *store.DB
	cipher Cipher
}

func NewService(db *store.DB, cipher Cipher) *Service {
	return &Service{db: db, cipher: cipher}
}

// credentialKey binds a server's credential to its ID, so a ciphertext copied to
// another row will not open — the same defence the settings cipher uses per key.
func credentialKey(id uuid.UUID) string { return "mcp.credentials." + id.String() }

// CreateInput is a new server.
type CreateInput struct {
	Name      string
	Transport Transport
	Command   string
	Args      []string
	URL       string

	Scope   Scope
	ScopeID *uuid.UUID

	Credential string
	AutoWrite  bool

	CreatedBy uuid.UUID
}

// Create adds a server. It starts deny-all: no tools are enabled until an admin tests
// the connection and opts into them (BE-10.1.3).
func (s *Service) Create(ctx context.Context, input CreateInput) (Server, error) {
	if err := validateShape(input.Transport, input.Command, input.URL); err != nil {
		return Server{}, err
	}
	if input.Scope == ScopeProject && input.ScopeID == nil {
		return Server{}, apierr.Validation("A project-scoped server needs a project.", map[string]any{"field": "scopeId"})
	}
	if input.Scope == ScopeGlobal {
		input.ScopeID = nil
	}

	row, err := s.db.Queries().CreateMCPServer(ctx, dbgen.CreateMCPServerParams{
		Name:      strings.TrimSpace(input.Name),
		Transport: dbgen.McpTransport(input.Transport),
		Command:   input.Command,
		Args:      nonNil(input.Args),
		Url:       input.URL,
		// Sealed after the insert, in storeCredential: a credential is bound to the
		// row's ID, which does not exist until the insert returns.
		Credentials:  json.RawMessage(`{}`),
		Scope:        dbgen.McpScope(input.Scope),
		ScopeID:      input.ScopeID,
		EnabledTools: []string{}, // deny-all until the admin opts in
		AutoWrite:    input.AutoWrite,
		IsEnabled:    true,
		CreatedBy:    &input.CreatedBy,
	})
	if err != nil {
		return Server{}, apierr.Internal(fmt.Errorf("create mcp server: %w", err))
	}
	// The credential is encrypted against the row's key, which needs the ID that only
	// exists after the insert. Store it now, then re-read so the returned server reflects
	// that a credential is set rather than the pre-update insert row.
	if input.Credential != "" {
		if err := s.storeCredential(ctx, row.ID, input.Credential); err != nil {
			return Server{}, err
		}
		return s.Get(ctx, row.ID)
	}
	return s.toServer(row)
}

// Get returns one server.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Server, error) {
	row, err := s.db.Queries().GetMCPServer(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Server{}, apierr.MCPServerNotFound()
		}
		return Server{}, apierr.Internal(fmt.Errorf("read mcp server: %w", err))
	}
	return s.toServer(row)
}

// List returns every configured server, for the settings screen.
func (s *Service) List(ctx context.Context) ([]Server, error) {
	rows, err := s.db.Queries().ListAllMCPServers(ctx)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("list mcp servers: %w", err))
	}
	return s.toServers(rows)
}

// ForProject returns the enabled servers a project may use: the global ones plus its
// own. This is the per-project boundary — another project's servers never appear
// (ai-architecture.md 5.4).
func (s *Service) ForProject(ctx context.Context, projectID uuid.UUID) ([]Server, error) {
	rows, err := s.db.Queries().ListMCPServersForScope(ctx, &projectID)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("list mcp servers for project: %w", err))
	}
	return s.toServers(rows)
}

// Config is a server ready to connect: its transport details and its decrypted
// credential. Built at the point of use and never stored; the credential lives on it
// only for the length of one connection.
type Config struct {
	ID        uuid.UUID
	Name      string
	Transport Transport
	Command   string
	Args      []string
	URL       string

	Credential   string
	EnabledTools []string
	AutoWrite    bool
}

// ConfigFor decrypts a server's credential for a connection. The one place the token
// leaves the database in the clear, and it goes straight to the caller that connects.
func (s *Service) ConfigFor(ctx context.Context, id uuid.UUID) (Config, error) {
	row, err := s.db.Queries().GetMCPServer(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Config{}, apierr.MCPServerNotFound()
		}
		return Config{}, apierr.Internal(fmt.Errorf("read mcp server: %w", err))
	}

	credential, err := s.openCredential(id, row.Credentials)
	if err != nil {
		return Config{}, err
	}

	return Config{
		ID:           row.ID,
		Name:         row.Name,
		Transport:    Transport(row.Transport),
		Command:      row.Command,
		Args:         row.Args,
		URL:          row.Url,
		Credential:   credential,
		EnabledTools: row.EnabledTools,
		AutoWrite:    row.AutoWrite,
	}, nil
}

// UpdateInput changes a server's configuration and its allowlist.
type UpdateInput struct {
	Name      string
	Transport Transport
	Command   string
	Args      []string
	URL       string

	Scope   Scope
	ScopeID *uuid.UUID

	// EnabledTools is the new allowlist. This is where an admin opts into specific
	// tools after a connection test.
	EnabledTools []string
	AutoWrite    bool
	IsEnabled    bool

	// Credential, when non-nil, replaces the stored one. Nil leaves it; an empty string
	// clears it.
	Credential *string
}

// Update changes a server.
func (s *Service) Update(ctx context.Context, id uuid.UUID, input UpdateInput) (Server, error) {
	if err := validateShape(input.Transport, input.Command, input.URL); err != nil {
		return Server{}, err
	}
	if input.Scope == ScopeProject && input.ScopeID == nil {
		return Server{}, apierr.Validation("A project-scoped server needs a project.", map[string]any{"field": "scopeId"})
	}
	if input.Scope == ScopeGlobal {
		input.ScopeID = nil
	}

	row, err := s.db.Queries().UpdateMCPServer(ctx, dbgen.UpdateMCPServerParams{
		ID:           id,
		Name:         strings.TrimSpace(input.Name),
		Transport:    dbgen.McpTransport(input.Transport),
		Command:      input.Command,
		Args:         nonNil(input.Args),
		Url:          input.URL,
		Scope:        dbgen.McpScope(input.Scope),
		ScopeID:      input.ScopeID,
		EnabledTools: nonNil(input.EnabledTools),
		AutoWrite:    input.AutoWrite,
		IsEnabled:    input.IsEnabled,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Server{}, apierr.MCPServerNotFound()
		}
		return Server{}, apierr.Internal(fmt.Errorf("update mcp server: %w", err))
	}

	if input.Credential != nil {
		if err := s.storeCredential(ctx, id, *input.Credential); err != nil {
			return Server{}, err
		}
	}
	return s.toServer(row)
}

// Delete removes a server. Its call history is kept — the rows lose their server link
// but not the record that the calls happened, which is what an audit needs.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	affected, err := s.db.Queries().DeleteMCPServer(ctx, id)
	if err != nil {
		return apierr.Internal(fmt.Errorf("delete mcp server: %w", err))
	}
	if affected == 0 {
		return apierr.MCPServerNotFound()
	}
	return nil
}

// RecordHealth stores the outcome of a connection test and the tools it discovered.
func (s *Service) RecordHealth(ctx context.Context, id uuid.UUID, status Health, detail string, tools []Tool) error {
	encoded, err := json.Marshal(tools)
	if err != nil {
		return apierr.Internal(fmt.Errorf("encode discovered tools: %w", err))
	}
	if err := s.db.Queries().RecordMCPHealth(ctx, dbgen.RecordMCPHealthParams{
		ID:              id,
		HealthStatus:    dbgen.McpHealth(status),
		HealthDetail:    detail,
		DiscoveredTools: encoded,
	}); err != nil {
		return apierr.Internal(fmt.Errorf("record mcp health: %w", err))
	}
	return nil
}

// validateShape checks the transport has the field it needs.
func validateShape(transport Transport, command, url string) error {
	switch transport {
	case TransportStdio:
		if strings.TrimSpace(command) == "" {
			return apierr.Validation("A stdio server needs a command.", map[string]any{"field": "command"})
		}
	case TransportHTTP:
		if strings.TrimSpace(url) == "" {
			return apierr.Validation("An http server needs a URL.", map[string]any{"field": "url"})
		}
	default:
		return apierr.Validation(fmt.Sprintf("%q is not an MCP transport.", transport), map[string]any{"field": "transport"})
	}
	return nil
}

func (s *Service) storeCredential(ctx context.Context, id uuid.UUID, plaintext string) error {
	if strings.TrimSpace(plaintext) == "" {
		if err := s.db.Queries().SetMCPCredentials(ctx, dbgen.SetMCPCredentialsParams{
			ID: id, Credentials: json.RawMessage(`{}`),
		}); err != nil {
			return apierr.Internal(fmt.Errorf("clear mcp credential: %w", err))
		}
		return nil
	}

	secret, err := s.cipher.Encrypt(credentialKey(id), plaintext)
	if err != nil {
		return apierr.Internal(fmt.Errorf("encrypt mcp credential: %w", err))
	}
	encoded, err := json.Marshal(secret)
	if err != nil {
		return apierr.Internal(fmt.Errorf("encode mcp credential: %w", err))
	}
	if err := s.db.Queries().SetMCPCredentials(ctx, dbgen.SetMCPCredentialsParams{
		ID: id, Credentials: encoded,
	}); err != nil {
		return apierr.Internal(fmt.Errorf("store mcp credential: %w", err))
	}
	return nil
}

func (s *Service) openCredential(id uuid.UUID, raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "{}" {
		return "", nil
	}
	var secret settings.Secret
	if err := json.Unmarshal(raw, &secret); err != nil {
		return "", apierr.Internal(fmt.Errorf("read mcp credential: %w", err))
	}
	if secret.Version == 0 {
		return "", nil
	}
	plaintext, err := s.cipher.Decrypt(credentialKey(id), secret)
	if err != nil {
		return "", apierr.Internal(fmt.Errorf("decrypt mcp credential: %w", err))
	}
	return plaintext, nil
}

func (s *Service) toServers(rows []dbgen.McpServer) ([]Server, error) {
	servers := make([]Server, 0, len(rows))
	for _, row := range rows {
		server, err := s.toServer(row)
		if err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}
	return servers, nil
}

func (s *Service) toServer(row dbgen.McpServer) (Server, error) {
	var tools []Tool
	if len(row.DiscoveredTools) > 0 {
		if err := json.Unmarshal(row.DiscoveredTools, &tools); err != nil {
			tools = nil
		}
	}

	hasCredentials := len(row.Credentials) > 0 && string(row.Credentials) != "{}"

	return Server{
		ID:              row.ID,
		Name:            row.Name,
		Transport:       Transport(row.Transport),
		Command:         row.Command,
		Args:            row.Args,
		URL:             row.Url,
		Scope:           Scope(row.Scope),
		ScopeID:         row.ScopeID,
		EnabledTools:    row.EnabledTools,
		DiscoveredTools: tools,
		AutoWrite:       row.AutoWrite,
		IsEnabled:       row.IsEnabled,
		HealthStatus:    Health(row.HealthStatus),
		HealthDetail:    row.HealthDetail,
		HealthCheckedAt: row.HealthCheckedAt,
		CreatedBy:       row.CreatedBy,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
		HasCredentials:  hasCredentials,
	}, nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
