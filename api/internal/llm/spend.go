package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/store/dbgen"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Spend aggregates llm_calls over a window (BE-1.14).
//
// Four genuine SQL aggregates rather than a scan and a loop, so the numbers
// reconcile exactly against a direct sum over the table: they are the same sum.
// The three groupings are separate queries because they group differently, not
// because the data differs.
func (s *Service) Spend(
	ctx context.Context,
	from, to time.Time,
	projectID *uuid.UUID,
) (api.AISpend, error) {
	window := spendWindow{From: from.UTC(), To: to.UTC()}

	total, err := s.db.Queries().SpendTotal(ctx, dbgen.SpendTotalParams{
		At: window.From, At_2: window.To, ProjectID: projectID,
	})
	if err != nil {
		return api.AISpend{}, fmt.Errorf("total spend: %w", err)
	}

	byProvider, err := s.spendByProvider(ctx, window, projectID)
	if err != nil {
		return api.AISpend{}, err
	}
	byProject, err := s.spendByProject(ctx, window, projectID)
	if err != nil {
		return api.AISpend{}, err
	}
	byAgent, err := s.spendByAgent(ctx, window, projectID)
	if err != nil {
		return api.AISpend{}, err
	}

	return api.AISpend{
		From:                 window.From,
		To:                   window.To,
		TotalCalls:           total.Calls,
		TotalCostUsd:         total.CostUsd.StringFixed(6),
		TotalInputTokens:     &total.InputTokens,
		TotalOutputTokens:    &total.OutputTokens,
		TotalCacheReadTokens: &total.CacheReadTokens,
		ByProvider:           byProvider,
		ByProject:            byProject,
		ByAgent:              byAgent,
	}, nil
}

func (s *Service) spendByProvider(
	ctx context.Context,
	window spendWindow,
	projectID *uuid.UUID,
) ([]api.AISpendGroup, error) {
	rows, err := s.db.Queries().SpendByProvider(ctx, dbgen.SpendByProviderParams{
		At: window.From, At_2: window.To, ProjectID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("spend by provider: %w", err)
	}

	// Names are resolved once here rather than joined in SQL, because a provider
	// deleted after its calls were recorded has no row to join to, and the kind
	// recorded on the call is what keeps the group meaningful.
	names := map[uuid.UUID]string{}
	if providers, err := s.ListProviders(ctx); err == nil {
		for _, provider := range providers {
			names[provider.ID] = provider.Name
		}
	}

	groups := make([]api.AISpendGroup, 0, len(rows))
	for _, row := range rows {
		label := ""
		if row.ProviderID != nil {
			label = names[*row.ProviderID]
		}
		if label == "" {
			label = row.ProviderKind
		}
		if label == "" {
			label = "Deleted provider"
		}

		groups = append(groups, toAPISpendGroup(spendGroup{
			Key:             keyOf(row.ProviderID),
			Label:           label,
			Calls:           row.Calls,
			InputTokens:     row.InputTokens,
			OutputTokens:    row.OutputTokens,
			CacheReadTokens: row.CacheReadTokens,
			Cost:            row.CostUsd,
		}))
	}
	return groups, nil
}

func (s *Service) spendByProject(
	ctx context.Context,
	window spendWindow,
	projectID *uuid.UUID,
) ([]api.AISpendGroup, error) {
	rows, err := s.db.Queries().SpendByProject(ctx, dbgen.SpendByProjectParams{
		At: window.From, At_2: window.To, ProjectID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("spend by project: %w", err)
	}

	groups := make([]api.AISpendGroup, 0, len(rows))
	for _, row := range rows {
		label := ""
		if row.ProjectID != nil {
			label = row.ProjectID.String()
		}
		if row.ProjectID == nil || *row.ProjectID == nilUUID {
			// Platform work rather than a project's: a smoke test, or a scheduled
			// check that belongs to nobody.
			label = "Platform"
		}

		groups = append(groups, toAPISpendGroup(spendGroup{
			Key:             keyOf(row.ProjectID),
			Label:           label,
			Calls:           row.Calls,
			InputTokens:     row.InputTokens,
			OutputTokens:    row.OutputTokens,
			CacheReadTokens: row.CacheReadTokens,
			Cost:            row.CostUsd,
		}))
	}
	return groups, nil
}

func (s *Service) spendByAgent(
	ctx context.Context,
	window spendWindow,
	projectID *uuid.UUID,
) ([]api.AISpendGroup, error) {
	rows, err := s.db.Queries().SpendByAgent(ctx, dbgen.SpendByAgentParams{
		At: window.From, At_2: window.To, ProjectID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("spend by agent: %w", err)
	}

	groups := make([]api.AISpendGroup, 0, len(rows))
	for _, row := range rows {
		label := row.Agent
		if label == "" {
			label = "Unattributed"
		}

		groups = append(groups, toAPISpendGroup(spendGroup{
			Key:             row.Agent,
			Label:           label,
			Calls:           row.Calls,
			InputTokens:     row.InputTokens,
			OutputTokens:    row.OutputTokens,
			CacheReadTokens: row.CacheReadTokens,
			Cost:            row.CostUsd,
		}))
	}
	return groups, nil
}

// keyOf renders a group key, using an empty string for the sentinel the aggregate
// returns when the owning row is gone.
func keyOf(id *uuid.UUID) string {
	if id == nil || *id == nilUUID {
		return ""
	}
	return id.String()
}

// Recent lists individual calls, newest first, for the detail table under the
// dashboard summary.
func (s *Service) Recent(
	ctx context.Context,
	from, to time.Time,
	projectID *uuid.UUID,
	limit int,
	cursor int64,
) ([]Call, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	params := dbgen.ListLLMCallsParams{
		At: from.UTC(), At_2: to.UTC(), ProjectID: projectID, PageSize: int32(limit),
	}
	if cursor > 0 {
		params.Cursor = &cursor
	}

	rows, err := s.db.Queries().ListLLMCalls(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list llm calls: %w", err)
	}

	calls := make([]Call, 0, len(rows))
	for _, row := range rows {
		calls = append(calls, toCall(row))
	}
	return calls, nil
}
