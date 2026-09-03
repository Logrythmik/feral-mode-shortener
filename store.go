package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	errNotFound = errors.New("not found")
	errConflict = errors.New("code already exists")
)

type Link struct {
	ID          int        `json:"id"`
	Code        string     `json:"code"`
	TargetURL   string     `json:"targetUrl"`
	Description *string    `json:"description"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	Clicks      int64      `json:"clicks"`
	LastClickAt *time.Time `json:"lastClickAt"`
}

type Click struct {
	Code      string
	LinkID    *int // nil for unknown codes that fell through to the fallback
	Referrer  string
	UserAgent string
	IP        string
}

type DayCount struct {
	Day   string `json:"day"` // YYYY-MM-DD (UTC)
	Count int64  `json:"count"`
}

type LabelCount struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

type LinkStats struct {
	Link         Link         `json:"link"`
	TotalClicks  int64        `json:"totalClicks"`
	ClicksByDay  []DayCount   `json:"clicksByDay"` // last 30 days
	TopReferrers []LabelCount `json:"topReferrers"`
}

// Store is the persistence boundary; handlers_test.go provides an in-memory
// implementation.
type Store interface {
	GetLinkByCode(ctx context.Context, code string) (*Link, error)
	ListLinks(ctx context.Context) ([]Link, error)
	CreateLink(ctx context.Context, code, targetURL string, description *string) (*Link, error)
	UpdateLink(ctx context.Context, code string, targetURL, description *string) (*Link, error)
	DeleteLink(ctx context.Context, code string) error
	RecordClick(ctx context.Context, c Click) error
	Stats(ctx context.Context, code string) (*LinkStats, error)
	TopMisses(ctx context.Context, limit int) ([]LabelCount, error)
	Ping(ctx context.Context) error
}

type pgStore struct {
	pool *pgxpool.Pool
}

func newPGStore(ctx context.Context, databaseURL string) (*pgStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	s := &pgStore{pool: pool}
	if err := s.ensureSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *pgStore) Close() { s.pool.Close() }

// ensureSchema applies the (idempotent) schema on boot — the whole schema is
// two tables, so this replaces a migration tool.
func (s *pgStore) ensureSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS links (
			id SERIAL PRIMARY KEY,
			code TEXT NOT NULL UNIQUE,
			target_url TEXT NOT NULL,
			description TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE IF NOT EXISTS clicks (
			id BIGSERIAL PRIMARY KEY,
			code TEXT NOT NULL,
			link_id INTEGER REFERENCES links(id) ON DELETE SET NULL,
			at TIMESTAMPTZ NOT NULL DEFAULT now(),
			referrer TEXT,
			user_agent TEXT,
			ip TEXT
		);
		CREATE INDEX IF NOT EXISTS clicks_code_at_idx ON clicks (code, at);
		CREATE INDEX IF NOT EXISTS clicks_link_id_idx ON clicks (link_id);
	`)
	return err
}

const linkColumns = `
	l.id, l.code, l.target_url, l.description, l.created_at, l.updated_at,
	count(c.id) AS clicks, max(c.at) AS last_click_at
`

func scanLink(row pgx.Row) (*Link, error) {
	var l Link
	err := row.Scan(&l.ID, &l.Code, &l.TargetURL, &l.Description, &l.CreatedAt, &l.UpdatedAt, &l.Clicks, &l.LastClickAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (s *pgStore) GetLinkByCode(ctx context.Context, code string) (*Link, error) {
	return scanLink(s.pool.QueryRow(ctx, `
		SELECT `+linkColumns+`
		FROM links l LEFT JOIN clicks c ON c.link_id = l.id
		WHERE l.code = $1
		GROUP BY l.id`, code))
}

func (s *pgStore) ListLinks(ctx context.Context) ([]Link, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+linkColumns+`
		FROM links l LEFT JOIN clicks c ON c.link_id = l.id
		GROUP BY l.id
		ORDER BY l.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	links := []Link{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, *l)
	}
	return links, rows.Err()
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *pgStore) CreateLink(ctx context.Context, code, targetURL string, description *string) (*Link, error) {
	var l Link
	err := s.pool.QueryRow(ctx, `
		INSERT INTO links (code, target_url, description) VALUES ($1, $2, $3)
		RETURNING id, code, target_url, description, created_at, updated_at`,
		code, targetURL, description,
	).Scan(&l.ID, &l.Code, &l.TargetURL, &l.Description, &l.CreatedAt, &l.UpdatedAt)
	if isUniqueViolation(err) {
		return nil, errConflict
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (s *pgStore) UpdateLink(ctx context.Context, code string, targetURL, description *string) (*Link, error) {
	var l Link
	err := s.pool.QueryRow(ctx, `
		UPDATE links SET
			target_url = COALESCE($2, target_url),
			description = COALESCE($3, description),
			updated_at = now()
		WHERE code = $1
		RETURNING id, code, target_url, description, created_at, updated_at`,
		code, targetURL, description,
	).Scan(&l.ID, &l.Code, &l.TargetURL, &l.Description, &l.CreatedAt, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (s *pgStore) DeleteLink(ctx context.Context, code string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM links WHERE code = $1`, code)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	return nil
}

func (s *pgStore) RecordClick(ctx context.Context, c Click) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO clicks (code, link_id, referrer, user_agent, ip)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''))`,
		c.Code, c.LinkID, c.Referrer, c.UserAgent, c.IP)
	return err
}

func (s *pgStore) Stats(ctx context.Context, code string) (*LinkStats, error) {
	link, err := s.GetLinkByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	stats := &LinkStats{Link: *link, TotalClicks: link.Clicks, ClicksByDay: []DayCount{}, TopReferrers: []LabelCount{}}

	rows, err := s.pool.Query(ctx, `
		SELECT to_char(date_trunc('day', at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day, count(*)
		FROM clicks WHERE link_id = $1 AND at > now() - interval '30 days'
		GROUP BY day ORDER BY day`, link.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d DayCount
		if err := rows.Scan(&d.Day, &d.Count); err != nil {
			return nil, err
		}
		stats.ClicksByDay = append(stats.ClicksByDay, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	refRows, err := s.pool.Query(ctx, `
		SELECT COALESCE(referrer, '(direct)'), count(*)
		FROM clicks WHERE link_id = $1
		GROUP BY 1 ORDER BY 2 DESC LIMIT 10`, link.ID)
	if err != nil {
		return nil, err
	}
	defer refRows.Close()
	for refRows.Next() {
		var r LabelCount
		if err := refRows.Scan(&r.Label, &r.Count); err != nil {
			return nil, err
		}
		stats.TopReferrers = append(stats.TopReferrers, r)
	}
	return stats, refRows.Err()
}

// TopMisses surfaces the most-hit unknown codes — typo'd campaign links show
// up here.
func (s *pgStore) TopMisses(ctx context.Context, limit int) ([]LabelCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT code, count(*) FROM clicks
		WHERE link_id IS NULL
		GROUP BY code ORDER BY 2 DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	misses := []LabelCount{}
	for rows.Next() {
		var m LabelCount
		if err := rows.Scan(&m.Label, &m.Count); err != nil {
			return nil, err
		}
		misses = append(misses, m)
	}
	return misses, rows.Err()
}

func (s *pgStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}
