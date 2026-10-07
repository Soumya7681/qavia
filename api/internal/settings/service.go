package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/role"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Target identifies whose value is being read.
//
// Both fields are optional. A job worker resolving a project setting has no user; a
// global read has neither.
type Target struct {
	UserID    *uuid.UUID
	ProjectID *uuid.UUID
}

// Value is a resolved setting: the value plus where it came from.
//
// Source matters to the UI, which shows whether a value is inherited and offers
// "reset to inherited" (F-1.6).
type Value struct {
	Key    string
	Raw    json.RawMessage
	Source Scope

	// FromDefault means no row existed at any scope and the registry default was
	// used.
	FromDefault bool

	UpdatedAt *time.Time
}

// Service resolves, validates, and writes settings.
type Service struct {
	db       *store.DB
	registry *Registry
	cipher   *Cipher
	cache    *cache
	recorder *audit.Recorder
}

func NewService(
	db *store.DB,
	registry *Registry,
	cipher *Cipher,
	recorder *audit.Recorder,
	cacheTTL time.Duration,
) *Service {
	return &Service{
		db:       db,
		registry: registry,
		cipher:   cipher,
		cache:    newCache(cacheTTL),
		recorder: recorder,
	}
}

// Registry exposes the declarations, for the API that renders the settings screen.
func (s *Service) Registry() *Registry { return s.registry }

// Cache exposes the cache so the invalidation listener can clear it.
func (s *Service) Cache() *cache { return s.cache }

// Resolve returns one value, following user, then project, then global, then the
// registry default.
//
// The order is applied in exactly one place. Every caller uses it, and there is no
// hand-rolled fallback anywhere else (backend-standards.md 6).
func (s *Service) Resolve(ctx context.Context, key string, target Target) (Value, error) {
	entry, declared := s.registry.Lookup(key)
	if !declared {
		// An undeclared key is an error, never a zero value.
		return Value{}, apierr.SettingUnknownKey(key)
	}

	if cached, found := s.cache.get(key, target); found {
		return cached, nil
	}

	rows, err := s.db.Queries().ResolveSetting(ctx, dbgen.ResolveSettingParams{
		Key:       key,
		UserID:    target.UserID,
		ProjectID: target.ProjectID,
	})
	if err != nil {
		return Value{}, fmt.Errorf("resolve setting %q: %w", key, err)
	}

	value, err := s.pick(entry, rows)
	if err != nil {
		return Value{}, err
	}

	s.cache.put(key, target, value)
	return value, nil
}

// pick takes the strongest row the query returned, or falls back to the default.
//
// The query already orders user, project, global, so this does not sort. A row at a
// scope the entry no longer allows is skipped rather than honoured: narrowing a
// setting's scopes in a later release must not leave an orphaned override in force.
func (s *Service) pick(entry Entry, rows []dbgen.Setting) (Value, error) {
	for _, row := range rows {
		scope := Scope(row.Scope)
		if !entry.AllowsScope(scope) {
			continue
		}
		return Value{
			Key:       entry.Key,
			Raw:       row.Value,
			Source:    scope,
			UpdatedAt: &row.UpdatedAt,
		}, nil
	}

	if entry.IsSecret() {
		// An unset secret is not an error: an unconfigured optional integration is
		// normal (requirements.md 5.4). The caller checks IsSet.
		return Value{Key: entry.Key, Source: ScopeGlobal, FromDefault: true}, nil
	}

	raw, err := entry.DefaultJSON()
	if err != nil {
		return Value{}, err
	}
	return Value{Key: entry.Key, Raw: raw, Source: ScopeGlobal, FromDefault: true}, nil
}

// ---------------------------------------------------------------- accessors

// String resolves a string setting.
func (s *Service) String(ctx context.Context, key string, target Target) (string, error) {
	value, err := s.Resolve(ctx, key, target)
	if err != nil {
		return "", err
	}
	return decodeAs[string](key, value.Raw)
}

// Int resolves an integer setting.
func (s *Service) Int(ctx context.Context, key string, target Target) (int, error) {
	value, err := s.Resolve(ctx, key, target)
	if err != nil {
		return 0, err
	}
	return decodeAs[int](key, value.Raw)
}

// Int64 resolves an integer setting that may exceed an int on 32-bit builds, such as
// a byte size.
func (s *Service) Int64(ctx context.Context, key string, target Target) (int64, error) {
	value, err := s.Resolve(ctx, key, target)
	if err != nil {
		return 0, err
	}
	return decodeAs[int64](key, value.Raw)
}

// Float resolves a number setting, such as a fractional CPU limit.
//
// Declared separately from Int because KindNumber is genuinely fractional: "one and
// a half cores" is a value an operator wants, and rounding it silently is worse than
// not supporting it.
func (s *Service) Float(ctx context.Context, key string, target Target) (float64, error) {
	value, err := s.Resolve(ctx, key, target)
	if err != nil {
		return 0, err
	}
	return decodeAs[float64](key, value.Raw)
}

// Bool resolves a boolean setting.
func (s *Service) Bool(ctx context.Context, key string, target Target) (bool, error) {
	value, err := s.Resolve(ctx, key, target)
	if err != nil {
		return false, err
	}
	return decodeAs[bool](key, value.Raw)
}

// StringList resolves a list setting.
func (s *Service) StringList(ctx context.Context, key string, target Target) ([]string, error) {
	value, err := s.Resolve(ctx, key, target)
	if err != nil {
		return nil, err
	}
	return decodeAs[[]string](key, value.Raw)
}

// Duration resolves a duration setting.
func (s *Service) Duration(ctx context.Context, key string, target Target) (time.Duration, error) {
	text, err := s.String(ctx, key, target)
	if err != nil {
		return 0, err
	}
	return ParseDuration(text)
}

// Secret resolves and decrypts a secret setting.
//
// It returns found=false when nothing is stored, because an unconfigured optional
// integration is not an error. The plaintext is returned for immediate use and must
// not be cached or held by the caller.
func (s *Service) Secret(ctx context.Context, key string, target Target) (plaintext string, found bool, err error) {
	entry, declared := s.registry.Lookup(key)
	if !declared {
		return "", false, apierr.SettingUnknownKey(key)
	}
	if !entry.IsSecret() {
		return "", false, fmt.Errorf("settings: %q is not a secret", key)
	}

	value, err := s.Resolve(ctx, key, target)
	if err != nil {
		return "", false, err
	}
	if value.FromDefault || len(value.Raw) == 0 {
		return "", false, nil
	}

	secret, err := DecodeSecret(value.Raw)
	if err != nil {
		return "", false, fmt.Errorf("settings: stored secret %q is unreadable: %w", key, err)
	}

	plaintext, err = s.cipher.Decrypt(key, secret)
	if err != nil {
		return "", false, err
	}
	return plaintext, true, nil
}

// JSON resolves an object setting into a named type.
//
// Generic so the caller names the type: no domain type holds a map[string]any
// (backend-standards.md 9).
func JSON[T any](ctx context.Context, s *Service, key string, target Target) (T, error) {
	var zero T

	value, err := s.Resolve(ctx, key, target)
	if err != nil {
		return zero, err
	}
	return decodeAs[T](key, value.Raw)
}

func decodeAs[T any](key string, raw json.RawMessage) (T, error) {
	var out T
	if len(raw) == 0 {
		return out, fmt.Errorf("settings: %q has no value", key)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("settings: %q is not the expected type: %w", key, err)
	}
	return out, nil
}

// ------------------------------------------------------------------- writes

// WriteRequest is one setting change.
type WriteRequest struct {
	Key   string
	Scope Scope

	// ScopeID is the project or user the value belongs to. Nil for global.
	ScopeID *uuid.UUID

	// Raw is the new value as JSON. For a secret it is a JSON string holding the
	// plaintext, which is encrypted before it is stored.
	Raw json.RawMessage
}

// Actor is who is making the change, for the permission check and the audit row.
type Actor struct {
	UserID uuid.UUID
	Email  string
	Role   role.Role
}

// Write stores one setting. It is WriteBatch with a single change.
func (s *Service) Write(ctx context.Context, actor Actor, request WriteRequest) (Value, error) {
	values, err := s.WriteBatch(ctx, actor, []WriteRequest{request})
	if err != nil {
		return Value{}, err
	}
	return values[0], nil
}

// WriteBatch applies several changes atomically.
//
// All of it or none of it, in one transaction. A settings form is submitted as a
// batch, and applying half of it leaves an admin guessing which half took effect,
// with no way to tell from the screen. That includes the audit rows: a rolled-back
// write must leave nothing claiming it happened.
//
// Validation and authorisation run over every change before anything is written, so
// the rejection is genuine rather than a matter of which change happened to be
// first.
func (s *Service) WriteBatch(ctx context.Context, actor Actor, requests []WriteRequest) ([]Value, error) {
	if len(requests) == 0 {
		return nil, apierr.Validation("No changes were supplied.", nil)
	}

	type prepared struct {
		entry     Entry
		request   WriteRequest
		stored    json.RawMessage
		auditable json.RawMessage
		previous  json.RawMessage
	}

	batch := make([]prepared, 0, len(requests))
	for _, request := range requests {
		entry, declared := s.registry.Lookup(request.Key)
		if !declared {
			return nil, apierr.SettingUnknownKey(request.Key)
		}
		if err := s.authorize(entry, actor, request); err != nil {
			return nil, err
		}

		stored, auditable, err := s.prepare(entry, request.Raw)
		if err != nil {
			return nil, err
		}

		batch = append(batch, prepared{
			entry:     entry,
			request:   request,
			stored:    stored,
			auditable: auditable,
			previous:  s.previousForAudit(ctx, entry, request),
		})
	}

	values := make([]Value, len(batch))
	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		for i, item := range batch {
			var (
				row   dbgen.Setting
				txErr error
			)
			if item.request.Scope == ScopeGlobal {
				// Global rows have a NULL scope_id, and two NULLs are distinct to a
				// plain unique index, so they need their own ON CONFLICT target.
				row, txErr = q.UpsertGlobalSetting(ctx, dbgen.UpsertGlobalSettingParams{
					Key: item.entry.Key, Value: item.stored,
					IsSecret: item.entry.IsSecret(), UpdatedBy: &actor.UserID,
				})
			} else {
				row, txErr = q.UpsertSetting(ctx, dbgen.UpsertSettingParams{
					Scope: dbgen.SettingsScope(item.request.Scope), ScopeID: item.request.ScopeID,
					Key: item.entry.Key, Value: item.stored,
					IsSecret: item.entry.IsSecret(), UpdatedBy: &actor.UserID,
				})
			}
			if txErr != nil {
				return fmt.Errorf("write setting %q: %w", item.entry.Key, txErr)
			}

			// The audit row shares the transaction, so a rollback takes it with it.
			if txErr = q.RecordSettingChange(ctx, dbgen.RecordSettingChangeParams{
				Scope:    dbgen.SettingsScope(item.request.Scope),
				ScopeID:  item.request.ScopeID,
				Key:      item.entry.Key,
				OldValue: item.previous,
				NewValue: item.auditable,
				ActorID:  &actor.UserID,
			}); txErr != nil {
				return fmt.Errorf("audit setting %q: %w", item.entry.Key, txErr)
			}

			values[i] = Value{
				Key:       item.entry.Key,
				Raw:       row.Value,
				Source:    item.request.Scope,
				UpdatedAt: &row.UpdatedAt,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Invalidation happens only after the transaction commits. Publishing earlier
	// would tell other processes to reload a value that might yet be rolled back.
	for _, item := range batch {
		s.recorder.Record(ctx, audit.Entry{
			Action:     auditAction(item.entry),
			ActorID:    &actor.UserID,
			ActorEmail: actor.Email,
			Subject:    item.entry.Key,
			ProjectID:  projectIDFor(item.request),
			Detail:     map[string]any{"scope": string(item.request.Scope)},
		})

		if err := s.Invalidate(ctx, item.entry.Key); err != nil {
			return nil, err
		}
	}

	return values, nil
}

// Clear removes an override so the value falls through to the next scope.
func (s *Service) Clear(ctx context.Context, actor Actor, key string, scope Scope, scopeID *uuid.UUID) error {
	entry, declared := s.registry.Lookup(key)
	if !declared {
		return apierr.SettingUnknownKey(key)
	}
	if err := s.authorize(entry, actor, WriteRequest{Key: key, Scope: scope, ScopeID: scopeID}); err != nil {
		return err
	}

	previous := s.previousForAudit(ctx, entry, WriteRequest{Key: key, Scope: scope, ScopeID: scopeID})

	err := s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, txErr := q.DeleteSetting(ctx, dbgen.DeleteSettingParams{
			Key: key, Scope: dbgen.SettingsScope(scope), ScopeID: scopeID,
		}); txErr != nil {
			return fmt.Errorf("clear setting %q: %w", key, txErr)
		}
		return q.RecordSettingChange(ctx, dbgen.RecordSettingChangeParams{
			Scope: dbgen.SettingsScope(scope), ScopeID: scopeID, Key: key,
			OldValue: previous, NewValue: nil, ActorID: &actor.UserID,
		})
	})
	if err != nil {
		return err
	}

	return s.Invalidate(ctx, key)
}

// prepare validates the incoming value and returns what to store and what to audit.
//
// For a secret those differ: the stored form is the sealed envelope, and the audited
// form is [redacted]. Only the fact of the change is kept (requirements.md 5.3).
func (s *Service) prepare(entry Entry, raw json.RawMessage) (stored, auditable json.RawMessage, err error) {
	if err := entry.validateValue(raw); err != nil {
		return nil, nil, apierr.SettingInvalid(entry.Key, capitalise(err.Error()))
	}

	if !entry.IsSecret() {
		return raw, raw, nil
	}

	var plaintext string
	if err := json.Unmarshal(raw, &plaintext); err != nil {
		return nil, nil, apierr.SettingInvalid(entry.Key, "Must be text.")
	}

	secret, err := s.cipher.Encrypt(entry.Key, plaintext)
	if err != nil {
		return nil, nil, err
	}
	encoded, err := EncodeSecret(secret)
	if err != nil {
		return nil, nil, err
	}
	return encoded, json.RawMessage(`"[redacted]"`), nil
}

// previousForAudit reads the current stored value for the audit row.
//
// A read failure is not worth failing the write over: the audit row records the
// change either way, and an absent old value is better than a refused settings
// update.
func (s *Service) previousForAudit(ctx context.Context, entry Entry, request WriteRequest) json.RawMessage {
	row, err := s.db.Queries().GetSetting(ctx, dbgen.GetSettingParams{
		Key: entry.Key, Scope: dbgen.SettingsScope(request.Scope), ScopeID: request.ScopeID,
	})
	if err != nil {
		return nil
	}
	if entry.IsSecret() {
		return json.RawMessage(`"[redacted]"`)
	}
	return row.Value
}

// authorize checks the scope and the role.
func (s *Service) authorize(entry Entry, actor Actor, request WriteRequest) error {
	if !entry.AllowsScope(request.Scope) {
		return apierr.SettingWrongScope(entry.Key, scopeList(entry.Scopes))
	}

	switch request.Scope {
	case ScopeGlobal:
		if request.ScopeID != nil {
			return apierr.Validation("A global setting has no owner.",
				map[string]any{"key": entry.Key})
		}
	case ScopeProject, ScopeUser:
		if request.ScopeID == nil {
			return apierr.Validation(
				fmt.Sprintf("A %s setting needs the %s it belongs to.", request.Scope, request.Scope),
				map[string]any{"key": entry.Key})
		}
	}

	// A user editing their own preferences is always allowed, whatever their role.
	// That is why preferences declare Viewer as their minimum: the scope, not the
	// role, is what stops one user editing another's.
	if request.Scope == ScopeUser && request.ScopeID != nil && *request.ScopeID == actor.UserID {
		return nil
	}

	if !actor.Role.AtLeast(entry.MinRole) {
		return apierr.SettingRoleTooLow(entry.Key, entry.MinRole.Label())
	}
	return nil
}

// ListForTarget resolves every declared setting the actor may see.
//
// It warms the whole set in one round trip rather than one query per key, which is
// what makes the settings screen a single request.
func (s *Service) ListForTarget(ctx context.Context, actor Actor, target Target) ([]Value, error) {
	rows, err := s.db.Queries().ResolveSettingsForScope(ctx, dbgen.ResolveSettingsForScopeParams{
		UserID:    target.UserID,
		ProjectID: target.ProjectID,
	})
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}

	byKey := make(map[string][]dbgen.Setting, len(rows))
	for _, row := range rows {
		byKey[row.Key] = append(byKey[row.Key], row)
	}

	entries := s.registry.Entries()
	out := make([]Value, 0, len(entries))
	for _, entry := range entries {
		value, pickErr := s.pick(entry, byKey[entry.Key])
		if pickErr != nil {
			return nil, pickErr
		}
		out = append(out, value)
	}
	return out, nil
}

func auditAction(entry Entry) audit.Action {
	if entry.IsSecret() {
		return audit.ActionSecretRotated
	}
	return audit.ActionSettingChanged
}

func projectIDFor(request WriteRequest) *uuid.UUID {
	if request.Scope == ScopeProject {
		return request.ScopeID
	}
	return nil
}

func scopeList(scopes []Scope) string {
	parts := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		parts = append(parts, string(scope))
	}
	return joinWithOr(parts)
}

// joinWithOr renders a scope list as "project or global", for an error message that
// tells the caller where the setting may actually be written.
func joinWithOr(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
	}
}

func capitalise(text string) string {
	if text == "" {
		return text
	}
	runes := []rune(text)
	if runes[0] >= 'a' && runes[0] <= 'z' {
		runes[0] = runes[0] - 'a' + 'A'
	}
	return string(runes)
}
