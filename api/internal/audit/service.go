package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/paging"
	"github.com/hyscaler/qavia/api/internal/store"
)

// Record is one audit row as the API returns it.
type Record struct {
	ID int64

	// ActorID is nil for something the system did on its own. ActorEmail is kept
	// separately, because the user row may later be deleted and an audit entry
	// that cannot name who acted is not an audit entry.
	ActorID    *uuid.UUID
	ActorEmail string

	Action  string
	Subject string

	ProjectID *uuid.UUID
	IP        *netip.Addr

	// Detail is decoded into a map here and nowhere else. It is the one genuinely
	// open shape in the schema: every action records different context, and the
	// alternative is a column per action.
	Detail map[string]any

	At time.Time
}

// Page is one page of audit records plus the cursor for the next.
type Page struct {
	Items      []Record
	NextCursor string
}

// Filter narrows the log.
//
// Four optional filters is why this query is built rather than generated: as
// sixteen sqlc queries it would be unreadable, and as one query with COALESCE
// tricks it would be unindexable (backend-standards.md 9).
type Filter struct {
	Action    string
	ActorID   *uuid.UUID
	ProjectID *uuid.UUID
	From      *time.Time
	To        *time.Time

	Limit  int
	Cursor string
}

// Service reads the audit log. Writing is the Recorder's job, and the two are
// deliberately separate: every service holds a Recorder, and only the admin
// endpoint holds this.
type Service struct {
	db *store.DB
}

func NewService(db *store.DB) *Service { return &Service{db: db} }

// auditColumns is the explicit column list. SELECT * is a review failure: an added
// column must not silently change the API surface (backend-standards.md 9).
var auditColumns = []string{
	"id", "actor_id", "actor_email", "action", "subject", "project_id", "ip", "detail", "at",
}

// List returns a page of audit records, newest first.
//
// The cursor is the id rather than a timestamp, because the id is a bigserial: two
// entries written in the same millisecond still have a total order, which a
// timestamp cursor could not give.
func (s *Service) List(ctx context.Context, filter Filter) (Page, error) {
	limit := paging.ClampLimit(filter.Limit)

	query := squirrel.
		Select(auditColumns...).
		From("audit_log").
		OrderBy("id DESC").
		// One row beyond the page, to learn whether another page exists without a
		// second count query that could disagree with it.
		Limit(uint64(limit + 1)).
		PlaceholderFormat(squirrel.Dollar)

	// Every value below is a placeholder. Nothing is concatenated into the SQL, and
	// that is not a style preference: this is the one query in the codebase the
	// compiler is not checking.
	if filter.Action != "" {
		query = query.Where(squirrel.Eq{"action": filter.Action})
	}
	if filter.ActorID != nil {
		query = query.Where(squirrel.Eq{"actor_id": *filter.ActorID})
	}
	if filter.ProjectID != nil {
		query = query.Where(squirrel.Eq{"project_id": *filter.ProjectID})
	}
	if filter.From != nil {
		query = query.Where(squirrel.GtOrEq{"at": *filter.From})
	}
	if filter.To != nil {
		query = query.Where(squirrel.LtOrEq{"at": *filter.To})
	}
	if filter.Cursor != "" {
		id, err := paging.DecodeID(filter.Cursor)
		if err != nil {
			return Page{}, err
		}
		query = query.Where(squirrel.Lt{"id": id})
	}

	sql, args, err := query.ToSql()
	if err != nil {
		return Page{}, fmt.Errorf("build audit query: %w", err)
	}

	rows, err := s.db.Pool().Query(ctx, sql, args...)
	if err != nil {
		return Page{}, fmt.Errorf("list audit entries: %w", err)
	}
	defer rows.Close()

	page := Page{Items: make([]Record, 0, limit)}
	for rows.Next() {
		var (
			record Record
			detail []byte
		)
		if err := rows.Scan(
			&record.ID, &record.ActorID, &record.ActorEmail, &record.Action,
			&record.Subject, &record.ProjectID, &record.IP, &detail, &record.At,
		); err != nil {
			return Page{}, fmt.Errorf("scan audit entry: %w", err)
		}

		if len(detail) > 0 {
			if err := json.Unmarshal(detail, &record.Detail); err != nil {
				return Page{}, fmt.Errorf("decode audit detail for %d: %w", record.ID, err)
			}
		}
		page.Items = append(page.Items, record)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("read audit entries: %w", err)
	}

	if len(page.Items) > limit {
		page.NextCursor = paging.EncodeID(page.Items[limit-1].ID)
		page.Items = page.Items[:limit]
	}
	return page, nil
}

// ParseAction validates a filter value against the known actions.
//
// An unknown action returns nothing rather than an error in a normal list, but
// this exists so a caller filtering on a typo is told, instead of concluding that
// nothing was audited.
func ParseAction(value string) (Action, error) {
	action := Action(value)
	if !action.Known() {
		return "", apierr.Validation(
			fmt.Sprintf("%q is not an audited action.", value),
			map[string]any{"field": "action"})
	}
	return action, nil
}
