package settings

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/role"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Handler implements the settings slice of the generated server interface.
type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// GetSettingsRegistry serves the declarations.
//
// This is the whole contract for the settings screen (seam item S2). Entries above
// the caller's role are withheld, and the server enforces the same rule on write, so
// hiding a field is convenience rather than the control.
func (h *Handler) GetSettingsRegistry(
	ctx context.Context,
	_ api.GetSettingsRegistryRequestObject,
) (api.GetSettingsRegistryResponseObject, error) {
	principal := httpx.MustCurrentUser(ctx)

	entries := h.service.Registry().Entries()
	body := api.SettingsRegistry{Entries: make([]api.SettingEntry, 0, len(entries))}

	for _, entry := range entries {
		if !visibleTo(entry, principal.Role) {
			continue
		}
		body.Entries = append(body.Entries, toAPIEntry(entry))
	}
	return api.GetSettingsRegistry200JSONResponse(body), nil
}

func (h *Handler) GetSettings(
	ctx context.Context,
	request api.GetSettingsRequestObject,
) (api.GetSettingsResponseObject, error) {
	principal := httpx.MustCurrentUser(ctx)

	target := Target{UserID: &principal.UserID, ProjectID: request.Params.ProjectID}

	values, err := h.service.ListForTarget(ctx, actorFrom(principal), target)
	if err != nil {
		return nil, err
	}

	body := api.SettingValues{Values: make([]api.SettingValue, 0, len(values))}
	for _, value := range values {
		entry, declared := h.service.Registry().Lookup(value.Key)
		if !declared || !visibleTo(entry, principal.Role) {
			continue
		}
		mapped, mapErr := toAPIValue(entry, value)
		if mapErr != nil {
			return nil, mapErr
		}
		body.Values = append(body.Values, mapped)
	}
	return api.GetSettings200JSONResponse(body), nil
}

// UpdateSettings applies a batch.
//
// All or nothing: one invalid value rejects the whole request. Applying half a form
// leaves an admin guessing which half took effect.
func (h *Handler) UpdateSettings(
	ctx context.Context,
	request api.UpdateSettingsRequestObject,
) (api.UpdateSettingsResponseObject, error) {
	principal := httpx.MustCurrentUser(ctx)
	actor := actorFrom(principal)

	// The service validates and authorises the whole batch before writing any of it,
	// and writes it in one transaction. This loop only maps the request shape.
	writes := make([]WriteRequest, 0, len(request.Body.Changes))
	for _, change := range request.Body.Changes {
		write, err := toWriteRequest(principal, change)
		if err != nil {
			return nil, err
		}
		writes = append(writes, write)
	}

	values, err := h.service.WriteBatch(ctx, actor, writes)
	if err != nil {
		return nil, err
	}

	body := api.SettingValues{Values: make([]api.SettingValue, 0, len(values))}
	for _, value := range values {
		entry, _ := h.service.Registry().Lookup(value.Key)
		mapped, mapErr := toAPIValue(entry, value)
		if mapErr != nil {
			return nil, mapErr
		}
		body.Values = append(body.Values, mapped)
	}
	return api.UpdateSettings200JSONResponse(body), nil
}

func (h *Handler) ClearSetting(
	ctx context.Context,
	request api.ClearSettingRequestObject,
) (api.ClearSettingResponseObject, error) {
	principal := httpx.MustCurrentUser(ctx)

	scope := Scope(request.Params.Scope)

	var scopeID *uuid.UUID
	switch scope {
	case ScopeProject:
		scopeID = request.Params.ProjectID
	case ScopeUser:
		// A caller clears their own override. Clearing another user's preference is
		// not an operation the product needs.
		scopeID = &principal.UserID
	}

	if err := h.service.Clear(ctx, actorFrom(principal), request.Key, scope, scopeID); err != nil {
		return nil, err
	}
	return api.ClearSetting204Response{}, nil
}

// toWriteRequest maps one change and resolves its owner.
func toWriteRequest(principal httpx.Principal, change api.SettingChange) (WriteRequest, error) {
	write := WriteRequest{Key: change.Key, Scope: Scope(change.Scope)}

	switch write.Scope {
	case ScopeProject:
		if change.ProjectID == nil {
			return WriteRequest{}, apierr.Validation(
				"A project setting needs the project it belongs to.",
				map[string]any{"key": change.Key, "field": "projectID"})
		}
		write.ScopeID = change.ProjectID

	case ScopeUser:
		// Defaults to the caller, so the common case of editing your own preferences
		// cannot accidentally target somebody else.
		owner := principal.UserID
		if change.UserID != nil {
			owner = *change.UserID
		}
		write.ScopeID = &owner
	}

	raw, err := json.Marshal(change.Value)
	if err != nil {
		return WriteRequest{}, apierr.Validation("That value cannot be stored.",
			map[string]any{"key": change.Key})
	}
	write.Raw = raw

	return write, nil
}

// visibleTo hides entries the caller could not write anyway.
//
// A user-scoped entry is always visible: everyone edits their own preferences, and
// the scope rather than the role is what stops one user editing another's.
func visibleTo(entry Entry, callerRole role.Role) bool {
	if entry.AllowsScope(ScopeUser) {
		return true
	}
	return callerRole.AtLeast(entry.MinRole)
}

func toAPIEntry(entry Entry) api.SettingEntry {
	out := api.SettingEntry{
		Key:             entry.Key,
		Category:        entry.Category,
		Label:           entry.Label,
		Kind:            api.SettingKind(entry.Kind),
		MinRole:         api.Role(entry.MinRole),
		IsSecret:        entry.IsSecret(),
		RestartRequired: entry.RestartRequired,
		Schema:          entry.JSONSchema(),
		Scopes:          make([]api.SettingScope, 0, len(entry.Scopes)),
	}

	if entry.HelpText != "" {
		help := entry.HelpText
		out.HelpText = &help
	}
	if entry.Default != nil {
		out.Default = entry.Default
	}
	for _, scope := range entry.Scopes {
		out.Scopes = append(out.Scopes, api.SettingScope(scope))
	}
	return out
}

// toAPIValue maps a resolved value.
//
// A secret never carries its value. It reports {isSet, updatedAt, hint}, which is
// enough for an admin to recognise what is stored and offer to replace it (F-1.8).
func toAPIValue(entry Entry, value Value) (api.SettingValue, error) {
	out := api.SettingValue{
		Key:         value.Key,
		Source:      api.SettingScope(value.Source),
		FromDefault: value.FromDefault,
	}
	if value.UpdatedAt != nil {
		out.UpdatedAt.Set(*value.UpdatedAt)
	}

	if entry.IsSecret() {
		read := api.SecretRead{IsSet: !value.FromDefault && len(value.Raw) > 0}
		if read.IsSet {
			secret, err := DecodeSecret(value.Raw)
			if err != nil {
				return api.SettingValue{}, err
			}
			if secret.Hint != "" {
				hint := secret.Hint
				read.Hint = &hint
			}
			updatedAt := secret.UpdatedAt
			read.UpdatedAt = &updatedAt
		}
		out.Secret = &read
		return out, nil
	}

	var decoded any
	if len(value.Raw) > 0 {
		if err := json.Unmarshal(value.Raw, &decoded); err != nil {
			return api.SettingValue{}, err
		}
	}
	out.Value = decoded
	return out, nil
}

func actorFrom(principal httpx.Principal) Actor {
	return Actor{UserID: principal.UserID, Email: principal.Email, Role: principal.Role}
}
