package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Marco 11 (platform and partners): instance metadata, filtered listing, scoped API keys
// and hosted pairing links.

func jsonStrings(v string) []string {
	out := []string{}
	_ = json.Unmarshal([]byte(v), &out)
	return out
}

func toJSONStrings(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// InstanceMeta is a partial update of the editable instance fields (nil: unchanged).
type InstanceMeta struct {
	Name        *string
	Tags        *[]string
	IPAllowlist *[]string
}

// UpdateInstanceMeta applies m in one statement.
func (s *Store) UpdateInstanceMeta(ctx context.Context, id string, m InstanceMeta) error {
	sets, args := []string{}, []any{}
	if m.Name != nil {
		sets, args = append(sets, "name = ?"), append(args, *m.Name)
	}
	if m.Tags != nil {
		sets, args = append(sets, "tags_json = ?"), append(args, toJSONStrings(*m.Tags))
	}
	if m.IPAllowlist != nil {
		sets, args = append(sets, "ip_allowlist_json = ?"), append(args, toJSONStrings(*m.IPAllowlist))
	}
	if len(sets) == 0 {
		_, err := s.GetInstance(ctx, id)
		return err
	}
	args = append(args, now(), id)
	return s.exec1(ctx, `UPDATE instances SET `+strings.Join(sets, ", ")+`, updated_at = ? WHERE id = ?`, args...)
}

// InstanceQuery filters ListInstancesPage. Zero fields do not filter.
type InstanceQuery struct {
	States []string
	Tag    string
	// Search matches a substring of the name or the ID (case-insensitive).
	Search string
	// After continues a page: instances created after (CreatedAt, ID) in list order.
	AfterCreated string
	AfterID      string
	Limit        int // 0: no limit
}

// ListInstancesPage lists instances in creation order with the filters of q.
func (s *Store) ListInstancesPage(ctx context.Context, q InstanceQuery) ([]Instance, error) {
	where, args := []string{"1 = 1"}, []any{}
	if len(q.States) > 0 {
		where = append(where, "state IN (?"+strings.Repeat(", ?", len(q.States)-1)+")")
		for _, st := range q.States {
			args = append(args, st)
		}
	}
	if q.Tag != "" {
		where = append(where, "EXISTS (SELECT 1 FROM json_each(instances.tags_json) WHERE value = ?)")
		args = append(args, q.Tag)
	}
	if q.Search != "" {
		pat := "%" + escapeLike(strings.ToLower(q.Search)) + "%"
		where = append(where, `(lower(name) LIKE ? ESCAPE '\' OR lower(id) LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat)
	}
	if q.AfterID != "" {
		where = append(where, "(created_at > ? OR (created_at = ? AND id > ?))")
		args = append(args, q.AfterCreated, q.AfterCreated, q.AfterID)
	}
	query := `SELECT ` + instanceCols + ` FROM instances WHERE ` + strings.Join(where, " AND ") + ` ORDER BY created_at, id`
	if q.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, q.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Instance{}
	for rows.Next() {
		in, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// FormatTS is the storage format of timestamps (used by page cursors).
func FormatTS(t time.Time) string { return fmtTS(t) }

// APIKey is a scoped key bound to one instance.
type APIKey struct {
	ID          string
	InstanceID  string
	Name        string
	KeyHash     string
	Scopes      []string
	IPAllowlist []string
	CreatedAt   time.Time
	ExpiresAt   time.Time // zero: never
	LastUsedAt  time.Time
}

const apiKeyCols = `id, instance_id, name, key_hash, scopes, ip_allowlist_json, created_at, expires_at, last_used_at`

func scanAPIKey(row scanner) (APIKey, error) {
	var k APIKey
	var scopes, allow, created, expires, used string
	err := row.Scan(&k.ID, &k.InstanceID, &k.Name, &k.KeyHash, &scopes, &allow, &created, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	if err != nil {
		return k, err
	}
	if scopes != "" {
		k.Scopes = strings.Split(scopes, ",")
	}
	k.IPAllowlist = jsonStrings(allow)
	k.CreatedAt, k.ExpiresAt, k.LastUsedAt = parseTS(created), parseTS(expires), parseTS(used)
	return k, nil
}

func (s *Store) CreateAPIKey(ctx context.Context, k APIKey) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_keys (`+apiKeyCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '')`,
		k.ID, k.InstanceID, k.Name, k.KeyHash, strings.Join(k.Scopes, ","), toJSONStrings(k.IPAllowlist),
		now(), fmtTS(k.ExpiresAt))
	return err
}

func (s *Store) APIKeyByHash(ctx context.Context, hash string) (APIKey, error) {
	return scanAPIKey(s.db.QueryRowContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE key_hash = ?`, hash))
}

func (s *Store) ListAPIKeys(ctx context.Context, instanceID string) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+apiKeyCols+` FROM api_keys WHERE instance_id = ? ORDER BY created_at, id`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) CountAPIKeys(ctx context.Context, instanceID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys WHERE instance_id = ?`, instanceID).Scan(&n)
	return n, err
}

func (s *Store) DeleteAPIKey(ctx context.Context, instanceID, id string) error {
	return s.exec1(ctx, `DELETE FROM api_keys WHERE id = ? AND instance_id = ?`, id, instanceID)
}

func (s *Store) TouchAPIKey(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, fmtTS(at), id)
	return err
}

// PairingLink is a hosted pairing link (the signed token names its ID).
type PairingLink struct {
	ID         string
	InstanceID string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	UsedAt     time.Time // zero: not used
	RevokedAt  time.Time // zero: not revoked
}

const pairingCols = `id, instance_id, created_at, expires_at, used_at, revoked_at`

func (s *Store) CreatePairingLink(ctx context.Context, l PairingLink) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO pairing_links (`+pairingCols+`) VALUES (?, ?, ?, ?, '', '')`,
		l.ID, l.InstanceID, fmtTS(l.CreatedAt), fmtTS(l.ExpiresAt))
	return err
}

func (s *Store) GetPairingLink(ctx context.Context, id string) (PairingLink, error) {
	var l PairingLink
	var created, expires, used, revoked string
	err := s.db.QueryRowContext(ctx, `SELECT `+pairingCols+` FROM pairing_links WHERE id = ?`, id).
		Scan(&l.ID, &l.InstanceID, &created, &expires, &used, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, err
	}
	l.CreatedAt, l.ExpiresAt, l.UsedAt, l.RevokedAt = parseTS(created), parseTS(expires), parseTS(used), parseTS(revoked)
	return l, nil
}

// MarkPairingLinksUsed spends the instance's live links (a pairing completed at at) and
// returns how many it spent.
func (s *Store) MarkPairingLinksUsed(ctx context.Context, instanceID string, at time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE pairing_links SET used_at = ?
		WHERE instance_id = ? AND used_at = '' AND revoked_at = '' AND expires_at > ?`, fmtTS(at), instanceID, fmtTS(at))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// RevokePairingLinks revokes the instance's links that are not spent yet.
func (s *Store) RevokePairingLinks(ctx context.Context, instanceID string, at time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE pairing_links SET revoked_at = ?
		WHERE instance_id = ? AND used_at = '' AND revoked_at = ''`, fmtTS(at), instanceID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// CountLivePairingLinks counts links that are neither expired, spent nor revoked.
func (s *Store) CountLivePairingLinks(ctx context.Context, instanceID string, at time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pairing_links
		WHERE instance_id = ? AND used_at = '' AND revoked_at = '' AND expires_at > ?`, instanceID, fmtTS(at)).Scan(&n)
	return n, err
}

// PrunePairingLinks deletes links that expired before cutoff.
func (s *Store) PrunePairingLinks(ctx context.Context, cutoff time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pairing_links WHERE expires_at < ?`, fmtTS(cutoff))
	return err
}
