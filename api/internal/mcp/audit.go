package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// The MCP call audit (BE-10.1.5, ai-architecture.md 5.5).
//
// Every MCP tool call is recorded — server, tool, arguments, status, agent, job — because
// an MCP server is a channel through which model output reaches a system that can change
// state, and after an unexpected write the only useful question is which job made it. A
// call the allowlist refused is recorded too, as "denied": a refusal is a fact worth
// keeping, and a run of them is a misconfiguration or an attempt worth seeing.
//
// Arguments are stored with credential-shaped values redacted, on the same principle as
// the logger: a token that reaches a tool call must not become a token in an audit
// table somebody exports.

// CallStatus is the outcome of a recorded call.
type CallStatus string

const (
	CallOK     CallStatus = "ok"
	CallError  CallStatus = "error"
	CallDenied CallStatus = "denied"
)

// CallRecord is one call to audit.
type CallRecord struct {
	ServerID   *uuid.UUID
	ServerName string
	Tool       string

	Arguments map[string]any

	Status CallStatus
	Error  string

	Agent     string
	ProjectID *uuid.UUID
	JobID     *uuid.UUID
}

// RecordCall writes one audit row, redacting credential-shaped argument values.
func (s *Service) RecordCall(ctx context.Context, record CallRecord) error {
	encoded, err := json.Marshal(redactArguments(record.Arguments))
	if err != nil {
		encoded = []byte("{}")
	}

	if err := s.db.Queries().RecordMCPCall(ctx, dbgen.RecordMCPCallParams{
		ServerID:   record.ServerID,
		ServerName: record.ServerName,
		Tool:       record.Tool,
		Arguments:  encoded,
		Status:     string(record.Status),
		Error:      record.Error,
		Agent:      record.Agent,
		ProjectID:  record.ProjectID,
		JobID:      record.JobID,
	}); err != nil {
		return fmt.Errorf("record mcp call: %w", err)
	}
	return nil
}

// Call is one recorded call, for the audit screen.
type Call struct {
	ID         int64
	ServerID   *uuid.UUID
	ServerName string
	Tool       string
	Arguments  json.RawMessage
	Status     string
	Error      string
	Agent      string
	ProjectID  *uuid.UUID
	JobID      *uuid.UUID
	At         time.Time
}

// Calls lists recent MCP calls, optionally narrowed to a server or a project.
func (s *Service) Calls(
	ctx context.Context,
	serverID, projectID *uuid.UUID,
	limit int,
) ([]Call, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	rows, err := s.db.Queries().ListMCPCalls(ctx, dbgen.ListMCPCallsParams{
		ServerID:  serverID,
		ProjectID: projectID,
		PageSize:  int32(limit), //nolint:gosec // Bounded above.
	})
	if err != nil {
		return nil, fmt.Errorf("list mcp calls: %w", err)
	}

	calls := make([]Call, 0, len(rows))
	for _, row := range rows {
		calls = append(calls, Call{
			ID:         row.ID,
			ServerID:   row.ServerID,
			ServerName: row.ServerName,
			Tool:       row.Tool,
			Arguments:  row.Arguments,
			Status:     row.Status,
			Error:      row.Error,
			Agent:      row.Agent,
			ProjectID:  row.ProjectID,
			JobID:      row.JobID,
			At:         row.At,
		})
	}
	return calls, nil
}

// secretKeyPattern matches an argument name that carries a credential, so its value is
// redacted before the arguments reach the audit table.
var secretKeyPattern = regexp.MustCompile(`(?i)(token|secret|password|credential|api[_-]?key|authorization|bearer)`)

// redactArguments replaces credential-shaped values with a marker, recursively.
func redactArguments(arguments map[string]any) map[string]any {
	if arguments == nil {
		return map[string]any{}
	}

	redacted := make(map[string]any, len(arguments))
	for key, value := range arguments {
		if secretKeyPattern.MatchString(key) {
			redacted[key] = "[redacted]"
			continue
		}
		redacted[key] = redactValue(value)
	}
	return redacted
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return redactArguments(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = redactValue(item)
		}
		return out
	case string:
		// A value that itself looks like a bearer token is redacted even under an
		// innocuous key: "authorization: Bearer eyJ..." arriving as a plain string.
		if strings.HasPrefix(typed, "Bearer ") || strings.HasPrefix(typed, "eyJ") {
			return "[redacted]"
		}
		return typed
	default:
		return value
	}
}
