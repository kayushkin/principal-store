package principalstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const principalColumns = `
	p.id, p.seq, p.kind, p.display_name, p.email, p.disabled_at, p.created_at, p.updated_at`

func scanPrincipal(scan func(...any) error) (*Principal, error) {
	var p Principal
	err := scan(&p.ID, &p.Seq, &p.Kind, &p.DisplayName, &p.Email, &p.DisabledAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// validateForWrite checks the fields every principal must carry, whatever the
// write path. A blank display name is refused rather than stored: the name is
// the only thing a card or a permission can render, and a row that renders as
// nothing is indistinguishable from a broken join.
func validateForWrite(p *Principal) error {
	kind, ok := NormalizeKind(p.Kind)
	if !ok {
		return ErrUnknownKind(p.Kind)
	}
	p.Kind = kind
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	if p.DisplayName == "" {
		return fmt.Errorf("%w: display_name is required", ErrInvalidPrincipal)
	}
	p.Email = strings.TrimSpace(p.Email)
	return nil
}

// Create records a new principal and hands back its id.
func (s *Store) Create(input *Principal) (*Principal, error) {
	if err := validateForWrite(input); err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM principals`).Scan(&maxSeq); err != nil {
		return nil, err
	}
	seq := maxSeq.Int64 + 1
	id := formatID(seq)
	ts := now()

	_, err = tx.Exec(`
		INSERT INTO principals (id, seq, kind, display_name, email, disabled_at, created_at, updated_at)
		VALUES (?,?,?,?,?,0,?,?)`,
		id, seq, input.Kind, input.DisplayName, input.Email, ts, ts)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(id, false)
}

// getRow reads one principal without expanding its memberships. Disabled rows
// are returned like any other: a card assigned to a departed human still has
// to render their name.
func (s *Store) getRow(id string) (*Principal, error) {
	row := s.db.QueryRow(`SELECT `+principalColumns+` FROM principals p WHERE p.id = ?`, id)
	p, err := scanPrincipal(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: principal %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// Get returns one principal with its memberships expanded: the groups a human
// belongs to, or the members a group holds. Disabled principals are always
// returned; includeDisabledMemberships says whether disabled rows appear in
// the expanded list as well.
func (s *Store) Get(id string, includeDisabledMemberships bool) (*Principal, error) {
	p, err := s.getRow(id)
	if err != nil {
		return nil, err
	}
	switch p.Kind {
	case KindHuman:
		if p.Groups, err = s.ListGroups(id, includeDisabledMemberships); err != nil {
			return nil, err
		}
	case KindGroup:
		if p.Members, err = s.ListMembers(id, includeDisabledMemberships); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// buildQuery assembles the shared WHERE for List and Count.
func (s *Store) buildQuery(f Filter, selectClause string) (string, []any, error) {
	var args []any
	where := ` FROM principals p WHERE 1=1`
	if !f.IncludeDisabled {
		where += ` AND p.disabled_at = 0`
	}
	if f.Kind != "" {
		kind, ok := NormalizeKind(f.Kind)
		if !ok {
			return "", nil, ErrUnknownKind(f.Kind)
		}
		where += ` AND p.kind = ?`
		args = append(args, kind)
	}
	if strings.TrimSpace(f.Query) != "" {
		expression := searchExpression(f.Query)
		if err := s.validateSearchQuery(expression); err != nil {
			return "", nil, err
		}
		where += ` AND p.seq IN (SELECT rowid FROM principals_fts WHERE principals_fts MATCH ?)`
		args = append(args, expression)
	}
	return selectClause + where, args, nil
}

// searchExpression turns what a caller typed into the FTS5 MATCH expression.
//
// Bare words become prefix terms — "pri" finds Priya Raman — because the main
// caller is an assignee picker reading keystrokes, and FTS5's default is a
// whole-token match that would find her only once the whole name was typed.
// Anything that already carries FTS5 syntax (a quote, a star, a colon,
// parentheses, or an uppercase AND / OR / NOT / NEAR) is passed through
// untouched, so an exact-phrase lookup like "Vlad Kayushkin" still means
// exactly that, which is what the seed script relies on.
func searchExpression(q string) string {
	q = strings.TrimSpace(q)
	if strings.ContainsAny(q, `"*:()`) {
		return q
	}
	words := strings.Fields(q)
	for _, w := range words {
		switch w {
		case "AND", "OR", "NOT", "NEAR":
			return q
		}
	}
	terms := make([]string, 0, len(words))
	for _, w := range words {
		terms = append(terms, `"`+w+`"*`)
	}
	return strings.Join(terms, " ")
}

// validateSearchQuery probes the FTS index so a malformed query is the caller's
// 400 rather than a 500 from deep inside the list handler.
func (s *Store) validateSearchQuery(expression string) error {
	var discard int
	err := s.db.QueryRow(
		`SELECT 1 FROM principals_fts WHERE principals_fts MATCH ? LIMIT 1`, expression).Scan(&discard)
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return fmt.Errorf("%w: bad search query %q: %v", ErrInvalidPrincipal, expression, err)
}

// List returns principals in display-name order. Memberships are not expanded
// here; Get does that for one row.
func (s *Store) List(f Filter) ([]*Principal, error) {
	query, args, err := s.buildQuery(f, `SELECT `+principalColumns)
	if err != nil {
		return nil, err
	}
	query += ` ORDER BY p.display_name COLLATE NOCASE ASC, p.seq ASC`
	if f.Limit > 0 {
		query += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	if f.Offset > 0 {
		if f.Limit <= 0 {
			query += ` LIMIT -1`
		}
		query += fmt.Sprintf(` OFFSET %d`, f.Offset)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Principal{}
	for rows.Next() {
		p, err := scanPrincipal(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Count is List's total, ignoring limit and offset.
func (s *Store) Count(f Filter) (int, error) {
	query, args, err := s.buildQuery(f, `SELECT COUNT(*)`)
	if err != nil {
		return 0, err
	}
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Counts is the /health summary over every row, disabled included.
func (s *Store) Counts() (Counts, error) {
	var c Counts
	err := s.db.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(kind = ?), 0),
		       COALESCE(SUM(kind = ?), 0),
		       COALESCE(SUM(disabled_at > 0), 0),
		       (SELECT COUNT(*) FROM principal_resources)
		FROM principals`, KindHuman, KindGroup).
		Scan(&c.Principals, &c.Humans, &c.Groups, &c.Disabled, &c.Resources)
	return c, err
}

// Patch edits the display fields. It refuses kind and disabled_at by having
// nowhere to put them: kind is fixed at creation, and disabled_at moves through
// Disable and Enable so removal is always an explicit act.
func (s *Store) Patch(id string, patch Patch) (*Principal, error) {
	current, err := s.getRow(id)
	if err != nil {
		return nil, err
	}
	if patch.DisplayName != nil {
		current.DisplayName = *patch.DisplayName
	}
	if patch.Email != nil {
		current.Email = *patch.Email
	}
	if err := validateForWrite(current); err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`UPDATE principals SET display_name=?, email=?, updated_at=? WHERE id=?`,
		current.DisplayName, current.Email, now(), id)
	if err != nil {
		return nil, err
	}
	return s.Get(id, false)
}

// Disable is the only removal. The row stays, so every reference from another
// store keeps resolving; it just drops out of default listings. Idempotent: a
// second call leaves the original disabled_at alone.
func (s *Store) Disable(id string) (*Principal, error) {
	if _, err := s.getRow(id); err != nil {
		return nil, err
	}
	ts := now()
	if _, err := s.db.Exec(`UPDATE principals SET disabled_at=?, updated_at=? WHERE id=? AND disabled_at=0`, ts, ts, id); err != nil {
		return nil, err
	}
	return s.Get(id, false)
}

// Enable undoes Disable. Idempotent.
func (s *Store) Enable(id string) (*Principal, error) {
	if _, err := s.getRow(id); err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`UPDATE principals SET disabled_at=0, updated_at=? WHERE id=? AND disabled_at<>0`, now(), id); err != nil {
		return nil, err
	}
	return s.Get(id, false)
}
