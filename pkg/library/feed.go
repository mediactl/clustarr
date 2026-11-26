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

package library

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Change is what a reader of the feed is told of one object: its state at
// the end of the window read, or that it is gone.
type Change struct {
	Raw
	// Op is 'A' for an object the window created and nothing has changed
	// since, 'M' for a changed one, 'D' for a deleted one. Body is then the
	// object as it was and Version the sequence number of the delete.
	Op byte
}

// feedWindow is how many sequence numbers one read of the feed spans.
const feedWindow = 1000

// Changes reads the feed of a kind after cursor: at most one change per
// object, in the order of the objects' versions, and the sequence number
// the read reached, which is the next cursor. An object changed again
// beyond the window is left for the read that reaches that change, so every
// change answered carries the object's current version and a reader that
// resumes from any version it was given misses nothing.
//
// It answers a ResourceExpired error when cursor is below what has been
// trimmed: the reader lists again.
func (s *Store) Changes(ctx context.Context, sch *Schema, cursor int64) (changes []Change, next int64, err error) {
	return s.changes(ctx, sch, cursor, false)
}

// changes is Changes, with each object's ownership records when asked.
func (s *Store) changes(ctx context.Context, sch *Schema, cursor int64, withManaged bool) (changes []Change, next int64, err error) {
	managed := `NULL::jsonb`
	if withManaged {
		managed = `o.managed`
	}
	rows, err := s.db.QueryContext(ctx, `
WITH h AS (SELECT seq, floor FROM library.head WHERE kind = $1),
w AS (
	SELECT DISTINCT ON (namespace, name) seq, namespace, name, op, body
	FROM library.change
	WHERE kind = $1 AND seq > $2 AND seq <= $2 + $3
	ORDER BY namespace, name, seq DESC
)
SELECT h.seq, h.floor, w.seq, w.namespace, w.name, w.op, w.body, o.version, `+objectSQL("o.")+`, `+managed+`
FROM h LEFT JOIN w ON true
LEFT JOIN library.`+sch.Table+` o ON o.namespace = w.namespace AND o.name = w.name
ORDER BY w.seq`, sch.GVK.Kind, cursor, feedWindow)
	if err != nil {
		return nil, 0, fmt.Errorf("library: read the %s feed: %w", sch.GVK.Kind, err)
	}
	defer func() { _ = rows.Close() }()
	var head, floor int64
	for rows.Next() {
		var (
			seq, version      sql.NullInt64
			namespace, name   sql.NullString
			op                sql.NullString
			last, body, owned []byte
			headSeq, headFlor int64
		)
		if err := rows.Scan(&headSeq, &headFlor, &seq, &namespace, &name, &op, &last, &version, &body, &owned); err != nil {
			return nil, 0, fmt.Errorf("library: read the %s feed: %w", sch.GVK.Kind, err)
		}
		head, floor = headSeq, headFlor
		if !seq.Valid {
			continue // no change in the window: the head row alone
		}
		c := Change{Raw: Raw{Namespace: namespace.String, Name: name.String}}
		switch {
		case version.Valid && version.Int64 == seq.Int64:
			// The row is as this change left it.
			c.Version, c.Body, c.Managed, c.Op = version.Int64, body, owned, 'M'
			if op.String == "A" {
				c.Op = 'A'
			}
		case op.String == "D" && !version.Valid:
			c.Version, c.Body, c.Op = seq.Int64, last, 'D'
		default:
			// Changed, deleted or made again beyond the window.
			continue
		}
		changes = append(changes, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("library: read the %s feed: %w", sch.GVK.Kind, err)
	}
	if cursor < floor {
		return nil, 0, apierrors.NewResourceExpired(fmt.Sprintf("library: the %s feed is trimmed up to %d; %d is too old", sch.GVK.Kind, floor, cursor))
	}
	next = min(head, cursor+feedWindow)
	if next < cursor {
		// The kind's sequence is behind the reader: the table was made
		// again, or a failover lost commits the reader had seen.
		return nil, 0, apierrors.NewResourceExpired(fmt.Sprintf("library: the %s feed is at %d, behind the reader's %d", sch.GVK.Kind, head, cursor))
	}
	return changes, next, nil
}

// Head is the sequence number of a kind's last committed write.
func (s *Store) Head(ctx context.Context, sch *Schema) (int64, error) {
	var seq int64
	if err := s.db.QueryRowContext(ctx, `SELECT seq FROM library.head WHERE kind = $1`, sch.GVK.Kind).Scan(&seq); err != nil {
		return 0, fmt.Errorf("library: read the %s head: %w", sch.GVK.Kind, err)
	}
	return seq, nil
}

// Trim deletes the feed's entries older than keep and raises each kind's
// floor to the last entry deleted. The manager runs it; a reader whose
// cursor is below a floor lists again.
func (s *Store) Trim(ctx context.Context, keep time.Duration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("library: trim the feed: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
WITH gone AS (
	DELETE FROM library.change WHERE at < now() - make_interval(secs => $1) RETURNING kind, seq
), top AS (
	SELECT kind, max(seq) AS seq FROM gone GROUP BY kind
)
UPDATE library.head h SET floor = top.seq FROM top WHERE h.kind = top.kind AND h.floor < top.seq`, keep.Seconds()); err != nil {
		return fmt.Errorf("library: trim the feed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("library: trim the feed: %w", err)
	}
	return nil
}

// notifier wakes the watchers of a kind when a write to it commits: one
// connection listening on the store's channel, shared by every watcher.
type notifier struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
	once sync.Once
}

// subscribe answers a channel that is signalled after a write to kind
// commits, and whenever the listener cannot tell (its connection was lost).
// stop releases it.
func (s *Store) subscribe(kind string) (wake <-chan struct{}, stop func()) {
	n := &s.notify
	n.once.Do(func() {
		n.subs = map[string]map[chan struct{}]struct{}{}
		go s.listen()
	})
	ch := make(chan struct{}, 1)
	n.mu.Lock()
	if n.subs[kind] == nil {
		n.subs[kind] = map[chan struct{}]struct{}{}
	}
	n.subs[kind][ch] = struct{}{}
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		delete(n.subs[kind], ch)
		n.mu.Unlock()
	}
}

// wake signals the watchers of kind, or of every kind when kind is empty.
func (n *notifier) wake(kind string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for k, subs := range n.subs {
		if kind != "" && k != kind {
			continue
		}
		for ch := range subs {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

// listen holds the LISTEN connection until the store closes, reconnecting
// when it is lost. A notification is only a wake-up: every watcher also
// polls, so a lost one delays a change and never loses it.
func (s *Store) listen() {
	ctx := logging.Subsystem(s.closing, "library.feed")
	log := logging.FromContext(ctx)
	backoff := time.Second
	for ctx.Err() == nil {
		err := s.listenOnce(ctx, func() { backoff = time.Second })
		if ctx.Err() != nil {
			return
		}
		log.Warn("the library feed's listener lost its connection; watchers poll until it is back", "err", err, "retryIn", backoff)
		s.notify.wake("")
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (s *Store) listenOnce(ctx context.Context, connected func()) error {
	conn, err := pgx.Connect(ctx, s.dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctx, `LISTEN library`); err != nil {
		return err
	}
	connected()
	// A write may have committed between the last connection and this one.
	s.notify.wake("")
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		s.notify.wake(n.Payload)
	}
}
