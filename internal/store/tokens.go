package store

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// tokenPrefix starts every API token, so one pasted somewhere it shouldn't
// be is easy to spot.
const tokenPrefix = "cal_"

// Token kinds: read is for the read API (websites, phones' calendars);
// source is an app that pushes its own bookings (PLAN.md step 5).
const (
	TokenRead   = "read"
	TokenSource = "source"
)

// Token lets another site or app use the API. The token itself is only
// known when it's made; afterwards it's Prefix, its first characters.
type Token struct {
	ID         int64
	Name       string // what uses it, e.g. "Church website"
	Kind       string
	Source     string // the app a source token is for; "" for read tokens
	SeePrivate bool   // a read token that sees private bookings (never locked ones)
	Prefix     string
	CreatedBy  string
	CreatedAt  string
	LastUsed   string // "" if never
}

// Access is the calendar access a read token grants.
func (t *Token) Access() Access {
	if t.SeePrivate {
		return AccessPrivate
	}
	return AccessPublic
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateReadToken makes a read token and returns it. This is the only time
// it can be seen.
func (s *Store) CreateReadToken(name string, seePrivate bool, createdBy int64) (string, error) {
	token := tokenPrefix + RandomToken()
	_, err := s.DB.Exec(`INSERT INTO tokens (name, kind, see_private, hash, prefix, created_by) VALUES (?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(name), TokenRead, seePrivate, hashToken(token), token[:len(tokenPrefix)+6], nullInt(createdBy))
	return token, err
}

// ListTokens lists the tokens of one kind by name.
func (s *Store) ListTokens(kind string) ([]Token, error) {
	rows, err := s.DB.Query(`
		SELECT t.id, t.name, t.kind, COALESCE(t.source, ''), t.see_private, t.prefix,
		       COALESCE(NULLIF(u.display_name, ''), u.username, ''), t.created_at, COALESCE(t.last_used, '')
		FROM tokens t LEFT JOIN users u ON u.id = t.created_by
		WHERE t.kind = ? ORDER BY t.name COLLATE NOCASE, t.id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Name, &t.Kind, &t.Source, &t.SeePrivate, &t.Prefix,
			&t.CreatedBy, &t.CreatedAt, &t.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteToken revokes a token: it stops working at once.
func (s *Store) DeleteToken(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM tokens WHERE id = ?`, id)
	return err
}

// SetTokenPrivate turns a read token's access to private bookings on or off.
func (s *Store) SetTokenPrivate(id int64, private bool) error {
	_, err := s.DB.Exec(`UPDATE tokens SET see_private = ? WHERE id = ? AND kind = ?`, private, id, TokenRead)
	return err
}

// AuthenticateToken finds the token and notes that it was used (at most
// once a minute, to spare the database a write on every request). Unknown
// tokens give ErrNotFound.
func (s *Store) AuthenticateToken(raw string) (*Token, error) {
	if !strings.HasPrefix(raw, tokenPrefix) {
		return nil, ErrNotFound
	}
	var t Token
	if err := s.DB.QueryRow(`SELECT id, name, kind, COALESCE(source, ''), see_private, prefix FROM tokens WHERE hash = ?`,
		hashToken(raw)).Scan(&t.ID, &t.Name, &t.Kind, &t.Source, &t.SeePrivate, &t.Prefix); err != nil {
		return nil, notFound(err)
	}
	_, err := s.DB.Exec(`UPDATE tokens SET last_used = datetime('now')
		WHERE id = ? AND (last_used IS NULL OR last_used < datetime('now', '-1 minute'))`, t.ID)
	return &t, err
}
