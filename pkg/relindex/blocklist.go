/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package relindex

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// BlocklistTTL is how long a blocklist row lives when its writer named no
// Until: today's DefaultBlocklistTTL, 90 days (ADR-0019 §6.14).
const BlocklistTTL = 90 * 24 * time.Hour

// maxBlockKeysPerQuery bounds the IN lists BlockState builds, well under
// SQLite's 32766 bound-parameter limit and Postgres' 65535.
const maxBlockKeysPerQuery = 400

// Block is one blocklist row (ADR-0019 §6.14). Unblocked rows are
// tombstones kept until Until so a late block with a lower Seq is a no-op.
//
// The blocklist table sits outside the release retention: Prune never
// touches it, and PruneBlocks deletes a row only at its Until.
type Block struct {
	// Scope is "*" (global) or schema.BlockScopeOf the item.
	Scope string
	// InfoHash, Indexer and GUID are the release's identity: a row is keyed
	// on (Scope, InfoHash) when the hash is known, else on (Scope, Indexer,
	// GUID). Indexer is the Indexer CR's name.
	InfoHash, Indexer, GUID string
	Title, Protocol, Reason string
	// EntryID is the grab entry the block was decided for.
	EntryID string
	// Seq fences the row: a write whose Seq is at or below the stored one is
	// a no-op.
	Seq int64
	// BlockedAt is when the manager decided the block; Until when the row
	// expires (BlockedAt + BlocklistTTL when zero).
	BlockedAt, Until time.Time
	// Unblocked marks a tombstone.
	Unblocked bool
}

// BlockKey is a release's identity as BlockState matches it: by InfoHash
// when set, and by Indexer and GUID when GUID is set.
type BlockKey struct{ InfoHash, Indexer, GUID string }

// blockCols is the select list every read of the table uses, in scanBlock's
// order.
const blockCols = `scope, info_hash, indexer, guid, title, protocol, reason, entry_id, seq, blocked_at, until, unblocked`

// blockDialect is what differs between the two engines: placeholders and
// the time and boolean encodings. Everything else -- the fence, the key
// choice, the matching -- is shared, so storetest holds both to one
// behaviour.
type blockDialect struct {
	// bind is the n-th (1-based) placeholder.
	bind func(n int) string
	// encTime encodes a time for a parameter.
	encTime func(t time.Time) any
	// encBool encodes the unblocked column.
	encBool func(b bool) any
	// scan reads one row of blockCols.
	scan func(rows *sql.Rows) (Block, error)
}

// validateBlock refuses a row that has no key.
func validateBlock(b Block) error {
	if b.Scope == "" {
		return fmt.Errorf("%w: block has no scope", ErrInvalidArg)
	}
	if b.InfoHash == "" && b.GUID == "" {
		return fmt.Errorf("%w: block names neither an info hash nor a guid", ErrInvalidArg)
	}
	if b.Seq <= 0 {
		return fmt.Errorf("%w: block seq %d is not positive", ErrInvalidArg, b.Seq)
	}
	return nil
}

// withUntil fills Until from BlockedAt when the writer named none.
func withUntil(b Block) (Block, error) {
	if !b.Until.IsZero() {
		return b, nil
	}
	if b.BlockedAt.IsZero() {
		return b, fmt.Errorf("%w: block has neither blockedAt nor until", ErrInvalidArg)
	}
	b.Until = b.BlockedAt.Add(BlocklistTTL)
	return b, nil
}

// keyWhere is the predicate selecting every row a block's key names, in its
// scope: the hash row, the indexer-and-guid row, or both.
func (d blockDialect) keyWhere(b Block, first int) (string, []any) {
	var (
		parts []string
		args  = []any{b.Scope}
		n     = first
	)
	scope := "scope = " + d.bind(n)
	n++
	if b.InfoHash != "" {
		parts = append(parts, "info_hash = "+d.bind(n))
		args = append(args, b.InfoHash)
		n++
	}
	if b.GUID != "" {
		parts = append(parts, "(indexer = "+d.bind(n)+" AND guid = "+d.bind(n+1)+")")
		args = append(args, b.Indexer, b.GUID)
	}
	return scope + " AND (" + strings.Join(parts, " OR ") + ")", args
}

// write is Block and Unblock: in one transaction, read every row the key
// names; when any holds a Seq at or above b.Seq the write is a no-op;
// otherwise the rows are replaced by b.
func (d blockDialect) write(ctx context.Context, db *sql.DB, b Block) (bool, error) {
	if err := validateBlock(b); err != nil {
		return false, err
	}
	b, err := withUntil(b)
	if err != nil {
		return false, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("relindex: blocklist: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	where, args := d.keyWhere(b, 1)
	var maxSeq sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM blocklist WHERE `+where, args...).Scan(&maxSeq); err != nil {
		return false, fmt.Errorf("relindex: blocklist: read row: %w", err)
	}
	if maxSeq.Valid && maxSeq.Int64 >= b.Seq {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blocklist WHERE `+where, args...); err != nil {
		return false, fmt.Errorf("relindex: blocklist: replace row: %w", err)
	}
	ins := `INSERT INTO blocklist (` + strings.ReplaceAll(blockCols, ", ", ",") + `) VALUES (`
	for i := 1; i <= 12; i++ {
		if i > 1 {
			ins += ","
		}
		ins += d.bind(i)
	}
	ins += `)`
	if _, err := tx.ExecContext(ctx, ins,
		b.Scope, b.InfoHash, b.Indexer, b.GUID, b.Title, b.Protocol, b.Reason, b.EntryID,
		b.Seq, d.encTime(b.BlockedAt), d.encTime(b.Until), d.encBool(b.Unblocked),
	); err != nil {
		return false, fmt.Errorf("relindex: blocklist: insert row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("relindex: blocklist: commit: %w", err)
	}
	return true, nil
}

// state answers BlockState: every live row (not unblocked, Until after now)
// matching each key, by the key's index. Keys are matched in batches, by
// hash and by guid, and each candidate row is checked against the key in Go.
func (d blockDialect) state(ctx context.Context, db *sql.DB, keys []BlockKey, now time.Time) (map[int][]Block, error) {
	out := map[int][]Block{}
	if len(keys) == 0 {
		return out, nil
	}
	for start := 0; start < len(keys); start += maxBlockKeysPerQuery {
		end := min(start+maxBlockKeysPerQuery, len(keys))
		batch := keys[start:end]

		var (
			hashes, guids []any
			seenH         = map[string]bool{}
			seenG         = map[string]bool{}
		)
		for _, k := range batch {
			if k.InfoHash != "" && !seenH[k.InfoHash] {
				seenH[k.InfoHash] = true
				hashes = append(hashes, k.InfoHash)
			}
			if k.GUID != "" && !seenG[k.GUID] {
				seenG[k.GUID] = true
				guids = append(guids, k.GUID)
			}
		}
		if len(hashes) == 0 && len(guids) == 0 {
			continue
		}

		args := []any{d.encBool(false), d.encTime(now)}
		n := 3
		var ors []string
		if len(hashes) > 0 {
			ors = append(ors, "info_hash IN ("+d.list(&n, len(hashes))+")")
			args = append(args, hashes...)
		}
		if len(guids) > 0 {
			ors = append(ors, "guid IN ("+d.list(&n, len(guids))+")")
			args = append(args, guids...)
		}
		q := `SELECT ` + blockCols + ` FROM blocklist WHERE unblocked = ` + d.bind(1) +
			` AND until > ` + d.bind(2) + ` AND (` + strings.Join(ors, " OR ") + `)`
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("relindex: blocklist: state: %w", err)
		}
		var found []Block
		for rows.Next() {
			b, err := d.scan(rows)
			if err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("relindex: blocklist: scan: %w", err)
			}
			found = append(found, b)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("relindex: blocklist: rows: %w", err)
		}
		_ = rows.Close()

		for i, k := range batch {
			for _, b := range found {
				if blockMatches(b, k) {
					out[start+i] = append(out[start+i], b)
				}
			}
		}
	}
	return out, nil
}

// blockMatches reports whether a row names a release key.
func blockMatches(b Block, k BlockKey) bool {
	if k.InfoHash != "" && b.InfoHash != "" && strings.EqualFold(k.InfoHash, b.InfoHash) {
		return true
	}
	return k.GUID != "" && b.GUID == k.GUID && b.Indexer == k.Indexer
}

// list renders count placeholders from *n on.
func (d blockDialect) list(n *int, count int) string {
	var sb strings.Builder
	for i := range count {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(d.bind(*n))
		*n++
	}
	return sb.String()
}

// listRows answers ListBlocks: the scope's live rows (every scope when
// scope is empty), newest first.
func (d blockDialect) listRows(ctx context.Context, db *sql.DB, scope string, limit, offset int) ([]Block, error) {
	q := `SELECT ` + blockCols + ` FROM blocklist WHERE unblocked = ` + d.bind(1)
	args := []any{d.encBool(false)}
	n := 2
	if scope != "" {
		q += ` AND scope = ` + d.bind(n)
		args = append(args, scope)
		n++
	}
	q += ` ORDER BY blocked_at DESC, id DESC`
	if limit > 0 {
		q += ` LIMIT ` + d.bind(n)
		args = append(args, limit)
		n++
		if offset > 0 {
			q += ` OFFSET ` + d.bind(n)
			args = append(args, offset)
		}
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("relindex: blocklist: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Block
	for rows.Next() {
		b, err := d.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("relindex: blocklist: scan: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("relindex: blocklist: rows: %w", err)
	}
	return out, nil
}

// prune deletes every row whose Until is at or before now.
func (d blockDialect) prune(ctx context.Context, db *sql.DB, now time.Time) (int, error) {
	if now.IsZero() {
		return 0, fmt.Errorf("%w: now is the zero time", ErrInvalidArg)
	}
	res, err := db.ExecContext(ctx, `DELETE FROM blocklist WHERE until <= `+d.bind(1), d.encTime(now))
	if err != nil {
		return 0, fmt.Errorf("relindex: blocklist: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("relindex: blocklist: prune rows: %w", err)
	}
	return int(n), nil
}

// sqliteBlocks is SQLite's dialect: ? placeholders, Unix nanoseconds, 0/1.
var sqliteBlocks = blockDialect{
	bind:    func(int) string { return "?" },
	encTime: func(t time.Time) any { return t.UTC().UnixNano() },
	encBool: func(b bool) any {
		if b {
			return 1
		}
		return 0
	},
	scan: func(rows *sql.Rows) (Block, error) {
		var (
			b                   Block
			blockedAt, until, u int64
		)
		if err := rows.Scan(&b.Scope, &b.InfoHash, &b.Indexer, &b.GUID, &b.Title, &b.Protocol,
			&b.Reason, &b.EntryID, &b.Seq, &blockedAt, &until, &u); err != nil {
			return Block{}, err
		}
		b.BlockedAt = time.Unix(0, blockedAt).UTC()
		b.Until = time.Unix(0, until).UTC()
		b.Unblocked = u != 0
		return b, nil
	},
}

// Block implements Store.Block. The write mutex serialises it with Upsert
// and Prune, as every SQLite writer is.
func (s *sqliteStore) Block(ctx context.Context, b Block) (bool, error) {
	b.Unblocked = false
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return sqliteBlocks.write(ctx, s.db, b)
}

// Unblock implements Store.Unblock.
func (s *sqliteStore) Unblock(ctx context.Context, b Block) (bool, error) {
	b.Unblocked = true
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return sqliteBlocks.write(ctx, s.db, b)
}

// BlockState implements Store.BlockState.
func (s *sqliteStore) BlockState(ctx context.Context, keys []BlockKey, now time.Time) (map[int][]Block, error) {
	return sqliteBlocks.state(ctx, s.db, keys, now)
}

// ListBlocks implements Store.ListBlocks.
func (s *sqliteStore) ListBlocks(ctx context.Context, scope string, limit, offset int) ([]Block, error) {
	return sqliteBlocks.listRows(ctx, s.db, scope, limit, offset)
}

// PruneBlocks implements Store.PruneBlocks.
func (s *sqliteStore) PruneBlocks(ctx context.Context, now time.Time) (int, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return sqliteBlocks.prune(ctx, s.db, now)
}

// pgBind is Postgres' numbered placeholder.
func pgBind(n int) string { return "$" + strconv.Itoa(n) }
