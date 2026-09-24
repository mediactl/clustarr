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

package usenet

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/javi11/rapidyenc"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// ErrArticleMissing is the outcome that survives failover: every configured
// server, primary and backup, answered "no such article".
//
// It is a sentinel because the difference between "this segment is gone" and
// "this provider is having a bad day" decides whether the download is failed
// with DownloadFailureMissingArticles or merely retried, and callers must be
// able to tell them apart with errors.Is rather than by string.
var ErrArticleMissing = errors.New("usenet: article not available on any configured server")

// ErrNoProviders is returned by [New] when no usable provider is configured.
var ErrNoProviders = errors.New("usenet: no usable NNTP provider configured")

// ErrProvidersUnavailable is the outcome when NO configured server could be
// asked for an article: every one refused the connection, rejected the
// credentials, sat out a penalty, had spent its quota, or dropped the
// connection before answering for it.
//
// It is deliberately not [ErrArticleMissing]. "Missing" is a verdict on the
// release -- the article is gone -- and it now blocklists the release (gap
// fix Y2); an outage or a wrong password is a verdict on this client's
// providers, and blocklisting every release grabbed while the password was
// wrong would burn through the whole search result. Before Y2 the two were
// the same sentinel, which was survivable only because nothing acted on a
// missingArticles failure. The job waits for a provider instead
// ([job.run]), the way SABnzbd and NZBGet keep a queue waiting on a down
// server rather than failing it.
var ErrProvidersUnavailable = errors.New("usenet: no configured server could be asked for the article")

// Provider is one upstream usenet server, flattened from
// downloadv1alpha1.NNTPProvider with its Secret already resolved. The engine
// resolves credentials; this package never reads a Secret.
type Provider struct {
	// Name identifies the provider in logs, status messages and
	// [ServerStats]. It is never a metric label -- provider names are
	// operator-chosen and unbounded.
	Name string

	Host string
	Port int
	TLS  bool

	// InsecureSkipVerify disables certificate verification. It exists for an
	// on-prem relay with a self-signed certificate and nothing else.
	InsecureSkipVerify bool

	Username string
	Password string

	// Connections is the hard cap on simultaneous connections to this
	// provider. Providers enforce their plan's cap by answering 502 or by
	// disconnecting, and exceeding it repeatedly gets an account throttled,
	// so this is a cap the pool must not round up.
	Connections int

	// Backup marks a fill server: it is only asked for articles the primaries
	// could not supply. This is what makes a small block account cheap.
	Backup bool

	// Priority orders providers within their class; lower is tried first.
	Priority int32

	// QuotaBytes is the plan's transfer allowance. Zero means unmetered. Once
	// the pool has read that many wire bytes from the provider it is skipped
	// as if it were down, which is the difference between a block account
	// that lasts a month and one that lasts a week.
	QuotaBytes int64
}

// Result is the outcome of fetching one article in a batch.
type Result struct {
	// Index is the position of this article in the ids slice passed to
	// [Pool.FetchBatch]. Results are returned in request order, but the index
	// is carried explicitly so a caller may reorder freely.
	Index int

	// Body is the DECODED article payload. Nil when Err is non-nil.
	Body []byte

	// Meta is the yEnc header of the part. Meta.Offset is the authoritative
	// write position inside the file -- the NZB's segment number orders the
	// segments, but only "=ypart begin" says where the bytes go.
	Meta rapidyenc.Meta

	// Server is the name of the provider that served the article.
	Server string

	// Err is nil on success, [ErrArticleMissing] when every server refused,
	// or the last decode failure when servers served corrupt data.
	Err error
}

// ServerStats is one provider's accounting, for [Client.Info] and for
// operators wondering where a month's quota went.
type ServerStats struct {
	Name       string
	Backup     bool
	UsedBytes  int64
	QuotaBytes int64
	// Exhausted is true when QuotaBytes is set and UsedBytes has reached it.
	Exhausted bool
	// Penalised is true while the provider is in backoff after refusing a
	// connection.
	Penalised bool
}

// penaltyRefused is how long a provider sits out after a 400/502. SABnzbd's
// _PENALTY_TOOMANY is 10 minutes, which is right for a human-tended client;
// grabarr is level-driven and re-reconciles, so a shorter penalty recovers
// faster without hammering.
const penaltyRefused = 2 * time.Minute

// penaltyAuth is longer: a rejected password will not fix itself, and
// hammering AUTHINFO is how an account gets locked.
const penaltyAuth = 10 * time.Minute

// serverPool is a bounded connection pool for ONE provider.
//
// The cap is enforced by sem and nothing else. A connection is only ever
// dialled while its caller holds a token, and a caller always drains the idle
// list before dialling, so the number of live connections can never exceed
// cap(sem) -- which is the invariant the concurrency test asserts against a
// counting stub server.
type serverPool struct {
	p   Provider
	sem chan struct{}

	mu     sync.Mutex
	idle   []*conn
	closed bool

	used       atomic.Int64
	penaltyEnd atomic.Int64 // unix nanos

	connectTimeout time.Duration
	ioTimeout      time.Duration
	maxArticle     int64
}

func newServerPool(p Provider, connectTimeout, ioTimeout time.Duration, maxArticle int64) *serverPool {
	conns := p.Connections
	if conns < 1 {
		conns = 1
	}
	return &serverPool{
		p:              p,
		sem:            make(chan struct{}, conns),
		connectTimeout: connectTimeout,
		ioTimeout:      ioTimeout,
		maxArticle:     maxArticle,
	}
}

func (s *serverPool) available(now time.Time) bool {
	if s.p.QuotaBytes > 0 && s.used.Load() >= s.p.QuotaBytes {
		return false
	}
	return s.penaltyEnd.Load() <= now.UnixNano()
}

func (s *serverPool) penalise(d time.Duration) {
	end := time.Now().Add(d).UnixNano()
	// Extend, never shorten: a second refusal while penalised should not
	// reset the clock backwards.
	for {
		cur := s.penaltyEnd.Load()
		if cur >= end || s.penaltyEnd.CompareAndSwap(cur, end) {
			return
		}
	}
}

func (s *serverPool) stats() ServerStats {
	used := s.used.Load()
	return ServerStats{
		Name:       s.p.Name,
		Backup:     s.p.Backup,
		UsedBytes:  used,
		QuotaBytes: s.p.QuotaBytes,
		Exhausted:  s.p.QuotaBytes > 0 && used >= s.p.QuotaBytes,
		Penalised:  s.penaltyEnd.Load() > time.Now().UnixNano(),
	}
}

func (s *serverPool) takeIdle() *conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.idle) > 0 {
		c := s.idle[len(s.idle)-1]
		s.idle = s.idle[:len(s.idle)-1]
		if !c.broken {
			return c
		}
		c.close()
	}
	return nil
}

func (s *serverPool) putIdle(c *conn) {
	s.mu.Lock()
	if s.closed || c.broken {
		s.mu.Unlock()
		c.close()
		return
	}
	s.idle = append(s.idle, c)
	s.mu.Unlock()
}

// withConn runs fn against one connection, holding exactly one of the
// provider's connection slots for its duration.
func (s *serverPool) withConn(ctx context.Context, fn func(*conn) error) error {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.sem }()

	c := s.takeIdle()
	if c != nil {
		// A provider drops idle connections without warning. One DATE is
		// cheaper than discovering it halfway through an article.
		if err := c.ping(ctx); err != nil {
			c.close()
			c = nil
		}
	}
	if c == nil {
		var err error
		c, err = dialConn(ctx, s.p, s.connectTimeout, s.ioTimeout, s.maxArticle)
		if err != nil {
			if errors.Is(err, errConnectionRefusedByServer) {
				s.penalise(penaltyRefused)
			}
			if errors.Is(err, errAuth) {
				s.penalise(penaltyAuth)
			}
			return err
		}
	}

	before := c.wireBytes
	err := fn(c)
	s.used.Add(c.wireBytes - before)

	if c.broken {
		c.close()
	} else {
		s.putIdle(c)
	}
	return err
}

func (s *serverPool) close() {
	s.mu.Lock()
	s.closed = true
	idle := s.idle
	s.idle = nil
	s.mu.Unlock()
	for _, c := range idle {
		c.quit()
		c.close()
	}
}

// Pool is the multi-provider article fetcher: a bounded connection pool per
// provider, pipelined commands inside each connection, priority-ordered
// failover between them, and per-provider quota accounting.
//
// Everything nntppool would have supplied lives here, for the reason plan
// ruling R9 gives: nntppool is cgo-only and cmd/clustarr is one static binary.
type Pool struct {
	servers []*serverPool
	depth   int
	// maxArticle caps the decoded body of one article.
	maxArticle int64
	closeOnce  sync.Once
}

// NewPool builds a pool over providers. They are ordered primaries first by
// ascending Priority, then backups by ascending Priority, which is the order
// [Pool.FetchBatch] walks and therefore the order a missing article is chased
// through -- SABnzbd's and NZBGet's "all servers of this level before the
// next" rule.
func NewPool(providers []Provider, depth int, connectTimeout, ioTimeout time.Duration, maxArticle int64) (*Pool, error) {
	if len(providers) == 0 {
		return nil, ErrNoProviders
	}
	ordered := slices.Clone(providers)
	slices.SortStableFunc(ordered, func(a, b Provider) int {
		if a.Backup != b.Backup {
			if a.Backup {
				return 1
			}
			return -1
		}
		return cmp.Compare(a.Priority, b.Priority)
	})

	p := &Pool{depth: depth, maxArticle: maxArticle}
	for _, prov := range ordered {
		p.servers = append(p.servers, newServerPool(prov, connectTimeout, ioTimeout, maxArticle))
	}
	return p, nil
}

// Close shuts every provider pool down and releases its connections.
func (p *Pool) Close() {
	p.closeOnce.Do(func() {
		for _, s := range p.servers {
			s.close()
		}
	})
}

// Stats reports per-provider accounting in configured order.
func (p *Pool) Stats() []ServerStats {
	out := make([]ServerStats, 0, len(p.servers))
	for _, s := range p.servers {
		out = append(out, s.stats())
	}
	return out
}

// FetchBatch fetches every id, pipelined within one connection per provider
// attempt, and fails over between providers in priority order.
//
// maxArticleTries is SABnzbd's max_art_tries: how many connections one
// server gets for the same articles in one batch before it is given up on.
const maxArticleTries = 3

// articleRetryBackoff is the wait before the second and third attempt,
// multiplied by the attempt number. A variable so tests need not wait.
var articleRetryBackoff = 250 * time.Millisecond

// sleepCtx waits d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// # The 430 failover
//
// This is the behaviour real-world completion rates hang on, and it is the one
// a naive client omits. An article that one provider answers 430 for is very
// often present on the next -- different backbones expire at different times,
// and takedowns are per-provider. So: every id the current provider refused
// stays pending and the NEXT provider is asked for exactly those, again
// pipelined. Only when the last configured provider (backup ones included) has
// refused an id does it become [ErrArticleMissing].
//
// A provider that refuses the CONNECTION (400, 502, auth) is a different
// failure: its whole batch stays pending, it earns a penalty, and the next
// provider is tried. Failing that over too is what keeps one provider's bad
// hour from failing a download.
//
// An article no server gave an answer for at all -- not a 430, not a
// corrupt body, because none could be asked -- is [ErrProvidersUnavailable],
// not ErrArticleMissing. "430 on every server" is the release's fault;
// "every server unreachable" is not.
//
// Results are returned in request order and are always len(ids) long. The
// returned error is non-nil only when ctx was cancelled; per-article outcomes
// are in Result.Err.
func (p *Pool) FetchBatch(ctx context.Context, ids []string) ([]Result, error) {
	ctx, span := tracing.Start(ctx, "usenet.pool.fetchBatch")
	defer span.End()

	res := make([]Result, len(ids))
	// done is tracked explicitly rather than inferred from res[i].Body: a
	// zero-length body is indistinguishable from an unfetched one, and
	// "never finishes" is a worse failure than "fetched an empty article".
	done := make([]bool, len(ids))
	// answered is true once some server gave a per-article response for
	// the id -- a refusal, a corrupt body or the article itself -- which is
	// what separates a missing article from one nobody could be asked for.
	answered := make([]bool, len(ids))
	pending := make([]int, 0, len(ids))
	for i := range ids {
		res[i] = Result{Index: i, Err: ErrArticleMissing}
		if !validMessageID(ids[i]) {
			res[i].Err = fmt.Errorf("%w: invalid message id %q", errProtocol, ids[i])
			continue
		}
		pending = append(pending, i)
	}

	log := logging.FromContext(ctx)

	for _, s := range p.servers {
		if len(pending) == 0 {
			break
		}
		if err := ctxDone(ctx); err != nil {
			return res, err
		}
		if !s.available(time.Now()) {
			continue
		}

		// SABnzbd's max_art_tries: a connection that dies mid-batch -- a
		// timeout, a reset, an idle drop the DATE probe did not catch --
		// says nothing about the articles it had not answered, so they are
		// asked again on a fresh connection to the SAME server, up to
		// maxArticleTries in all, before the server is given up on for this
		// batch. A refusal or an auth failure penalises the server and is
		// not retried; a 430 is an answer, not a failure, and never gets
		// here. Before this, one dropped connection on the only server
		// stalled the whole job for the provider retry delay (2026-09-24).
		answeredHere := make([]bool, len(ids))
		for attempt := 1; ; attempt++ {
			batch := make([]string, 0, len(pending))
			asked := make([]int, 0, len(pending))
			for _, idx := range pending {
				if !done[idx] && !answeredHere[idx] {
					batch = append(batch, ids[idx])
					asked = append(asked, idx)
				}
			}
			if len(batch) == 0 {
				break
			}
			err := s.withConn(ctx, func(c *conn) error {
				return c.pipeline(ctx, "BODY", batch, p.depth, 222, true, func(i int, r io.Reader, ferr error) error {
					idx := asked[i]
					answered[idx] = true
					answeredHere[idx] = true
					if ferr != nil {
						// 430 and friends: leave it pending for the next server.
						res[idx].Err = ferr
						return nil
					}
					body, meta, derr := decodeArticle(r, p.maxArticle)
					if derr != nil {
						// Corrupt or truncated on THIS server. Leave it pending:
						// the next provider may hold a good copy, which is the
						// same argument as for a 430.
						res[idx].Err = derr
						return nil
					}
					res[idx] = Result{Index: idx, Body: body, Meta: meta, Server: s.p.Name}
					done[idx] = true
					return nil
				})
			})
			if err == nil {
				break
			}
			if ctxErr := ctxDone(ctx); ctxErr != nil {
				return res, ctxErr
			}
			switch {
			case errors.Is(err, errConnectionRefusedByServer):
				s.penalise(penaltyRefused)
			case errors.Is(err, errAuth):
				s.penalise(penaltyAuth)
			}
			retry := attempt < maxArticleTries && !errors.Is(err, errConnectionRefusedByServer) &&
				!errors.Is(err, errAuth) && s.available(time.Now())
			if retry {
				log.WarnContext(ctx, "nntp connection failed mid-batch; retrying the rest on the same provider",
					"provider", s.p.Name, "attempt", attempt, "of", maxArticleTries, "error", err)
				if err := sleepCtx(ctx, articleRetryBackoff*time.Duration(attempt)); err != nil {
					return res, err
				}
				continue
			}
			log.WarnContext(ctx, "nntp provider failed a batch, failing over",
				"provider", s.p.Name, "articles", len(batch), "attempts", attempt, "error", err)
			for _, idx := range asked {
				if !done[idx] {
					res[idx].Err = err
				}
			}
			break
		}
		still := make([]int, 0, len(pending))
		for _, idx := range pending {
			if !done[idx] {
				still = append(still, idx)
			}
		}
		pending = still
	}

	if err := ctxDone(ctx); err != nil {
		return res, err
	}
	for _, idx := range pending {
		// Every server refused, failed, or could not be asked. Keep the
		// last cause visible but make the sentinel the one callers test for.
		sentinel := ErrArticleMissing
		if !answered[idx] {
			sentinel = ErrProvidersUnavailable
		}
		if res[idx].Err != nil && !errors.Is(res[idx].Err, ErrArticleMissing) {
			res[idx].Err = fmt.Errorf("%w: last error: %w", sentinel, res[idx].Err)
		} else {
			res[idx].Err = sentinel
		}
	}
	return res, nil
}

// Fetch is FetchBatch for one article.
func (p *Pool) Fetch(ctx context.Context, id string) (Result, error) {
	out, err := p.FetchBatch(ctx, []string{id})
	if err != nil {
		return Result{}, err
	}
	return out[0], nil
}

// Exists STATs every id and returns the ones no provider holds. It is the
// pre-check SABnzbd calls "Check before download": a cheap sweep that decides
// whether to commit disk and quota to a download at all.
//
// An id no server answered for at all is not counted missing: when there are
// any, Exists returns the ones it did establish as missing together with
// [ErrProvidersUnavailable], and the caller skips the verdict rather than
// failing a release it could not look at.
//
// STAT transfers nothing, so it pipelines much deeper than BODY.
func (p *Pool) Exists(ctx context.Context, ids []string) ([]string, error) {
	ctx, span := tracing.Start(ctx, "usenet.pool.exists")
	defer span.End()

	const statDepth = 32

	found := make([]bool, len(ids))
	// An invalid id is answered by construction: no server will ever hold it.
	answered := make([]bool, len(ids))
	pending := make([]int, 0, len(ids))
	for i := range ids {
		if validMessageID(ids[i]) {
			pending = append(pending, i)
		} else {
			answered[i] = true
		}
	}

	for _, s := range p.servers {
		if len(pending) == 0 {
			break
		}
		if err := ctxDone(ctx); err != nil {
			return nil, err
		}
		if !s.available(time.Now()) {
			continue
		}
		batch := make([]string, len(pending))
		for i, idx := range pending {
			batch[i] = ids[idx]
		}
		depth := max(p.depth, statDepth)
		_ = s.withConn(ctx, func(c *conn) error {
			return c.pipeline(ctx, "STAT", batch, depth, 223, false, func(i int, _ io.Reader, ferr error) error {
				answered[pending[i]] = true
				if ferr == nil {
					found[pending[i]] = true
				}
				return nil
			})
		})
		still := make([]int, 0, len(pending))
		for _, idx := range pending {
			if !found[idx] {
				still = append(still, idx)
			}
		}
		pending = still
	}

	missing := make([]string, 0, len(pending))
	unasked := 0
	for i := range ids {
		switch {
		case found[i]:
		case answered[i]:
			missing = append(missing, ids[i])
		default:
			unasked++
		}
	}
	if unasked > 0 {
		return missing, fmt.Errorf("%w: %d of %d articles could not be checked", ErrProvidersUnavailable, unasked, len(ids))
	}
	return missing, nil
}

// ctxDone reports ctx's cancellation, including the instant before its own
// timer goroutine has run.
//
// This is not pedantry. armDeadline sets the socket deadline to exactly the
// context's deadline, so on a timeout the two fire together and the socket
// often loses the race by microseconds: ctx.Err() is still nil while the read
// has already failed with i/o timeout. Reporting that as "the article is
// missing on every server" would blocklist a release because a reconcile ran
// out of time, so the deadline is compared directly rather than trusted to
// have been noticed yet.
func ctxDone(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
		return context.DeadlineExceeded
	}
	return nil
}

// capWriter fails past max instead of growing without bound. It is the article
// equivalent of the io.LimitReader+ErrResponseTooLarge pattern every HTTP
// client in this repo uses, and it exists for the same reason: a broken or
// hostile server must not be able to decide how much memory a worker uses.
type capWriter struct {
	buf bytes.Buffer
	n   int64
	max int64
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.n+int64(len(p)) > w.max {
		return 0, fmt.Errorf("%w: more than %d decoded bytes", ErrArticleTooLarge, w.max)
	}
	w.n += int64(len(p))
	return w.buf.Write(p)
}

// decodeArticle yEnc-decodes one raw article body.
//
// r must carry the WIRE bytes, dots stuffed and ".\r\n" included: rapidyenc is
// a raw NNTP decoder and does the un-stuffing itself. It also verifies the
// part's pcrc32 as it goes, so a server that serves corrupt bytes is caught
// here rather than by par2 half an hour later.
func decodeArticle(r io.Reader, max int64) ([]byte, rapidyenc.Meta, error) {
	dec := rapidyenc.NewDecoder(r)
	var out capWriter
	out.max = max
	// The buffer must comfortably exceed rapidyenc's internal remainder, which
	// it refuses to hand back into a shorter slice.
	buf := make([]byte, 64<<10)
	if _, err := io.CopyBuffer(&out, dec, buf); err != nil {
		return nil, dec.Meta.Meta, fmt.Errorf("usenet: decode article: %w", err)
	}
	return out.buf.Bytes(), dec.Meta.Meta, nil
}
