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
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Tensai75/nntp"
)

// # Why this file exists at all
//
// Plan ruling R9 settled that grabarr writes its own NNTP pool, because the
// maintained one (javi11/nntppool/v4) is cgo-only and cmd/clustarr is a single
// CGO_ENABLED=0 binary. R9 expected github.com/Tensai75/nntp to supply the one
// connection under that pool. It cannot, and the reason is worth writing down
// so nobody re-tries it:
//
//   - Its *Conn.Body returns a reader that canonicalises CRLF to LF,
//     un-stuffs leading dots and swallows the terminating ".\r\n".
//     github.com/javi11/rapidyenc's Decoder is a RAW NNTP decoder: it does its
//     own dot-unstuffing, and it detects the end of an article by the literal
//     "\r\n.\r\n" byte sequence (decode_generic.go, states StateCRLFDT /
//     StateCRLFDTCR). Feeding it Tensai75's already-cooked stream yields
//     ErrDataCorruption on every article, so the two libraries cannot be
//     composed.
//   - *Conn.cmd writes a command and immediately reads its response, and
//     discards any unread body first. Pipelining -- which the plan requires,
//     because round-trip latency and not bandwidth is the limit on a
//     high-latency provider -- is structurally impossible through it.
//   - It exposes neither the net.Conn nor a context, so there is no deadline
//     and no cancellation. A stalled provider would hang a worker forever.
//
// What remains genuinely reusable is its error type: nntp.Error{Code, Msg} is
// the shape the Go usenet ecosystem returns for a non-2xx status line, so
// [statusError] wraps one inside every sentinel below and errors.As keeps
// working for callers that already know it.
//
// Everything else here is the RFC 3977 subset grabarr needs: greeting,
// AUTHINFO, pipelined BODY/STAT, and a raw multi-line reader that hands
// rapidyenc exactly the bytes the wire carried.

const (
	// defaultConnectTimeout bounds dial plus TLS handshake plus greeting.
	defaultConnectTimeout = 20 * time.Second

	// defaultIOTimeout bounds one command/response exchange. NZBGet's
	// ArticleTimeout is 60s and providers are slow under load, so this is
	// deliberately generous; the pool's own failover is what makes a slow
	// server cheap, not a short timeout.
	defaultIOTimeout = 60 * time.Second

	// defaultPipelineDepth is how many commands may be in flight on one
	// connection. SABnzbd's pipelining_requests defaults to 1 and its
	// maintainers use 2-8; nntppool's Inflight defaults to 3. Four keeps a
	// 200ms-RTT provider busy without holding more than four article bodies'
	// worth of server-side work hostage to one slow read.
	defaultPipelineDepth = 4

	// defaultMaxArticleBytes caps the DECODED body of one article. Real
	// segments are 512KiB-768KiB; 4MiB leaves room for oversized posts while
	// keeping a malicious or broken server from making a worker buffer
	// unbounded memory. Same discipline as every HTTP body in this repo:
	// a package-level max, a cap on the read, and a sentinel.
	defaultMaxArticleBytes = 4 << 20

	// statusLineMax bounds one response line. bufio.Reader.ReadSlice returns
	// ErrBufferFull past its buffer, which is how the bound is enforced.
	statusLineMax = 8 << 10
)

// Wire-level sentinels. They are unexported because no caller outside this
// package should branch on a single server's answer -- [ErrArticleMissing] is
// the outcome that survives failover, and it is what callers see.
var (
	// errArticleNotFound is 430 (and the other "no such article" codes).
	// It is the failover trigger: the article may still exist elsewhere.
	errArticleNotFound = errors.New("usenet: no such article on this server")

	// errConnectionRefusedByServer covers 400 and 502 -- "too many
	// connections", "service temporarily unavailable", "service
	// discontinued". The connection is dead and the server earns a penalty.
	errConnectionRefusedByServer = errors.New("usenet: server refused the connection")

	// errAuth covers 481/482/481 and a 480 mid-session.
	errAuth = errors.New("usenet: authentication rejected")

	// errProtocol is a response this client cannot make sense of.
	errProtocol = errors.New("usenet: protocol error")
)

// ErrArticleTooLarge is returned when one article's body exceeds the
// configured cap. The connection carrying it is poisoned -- there is no way
// to resynchronise mid-body -- so it is closed rather than returned to the
// pool.
var ErrArticleTooLarge = errors.New("usenet: article body exceeds the size limit")

// statusError turns a status line into an error whose sentinel says what the
// caller should DO, wrapping an nntp.Error that says what the server SAID.
// Both are reachable: errors.Is(err, errArticleNotFound) and
// errors.As(err, &nntp.Error{}) both hold.
func statusError(code int, text string) error {
	wire := nntp.Error{Code: uint(code), Msg: text} //nolint:gosec // NNTP codes are three digits.
	switch code {
	case 411, 412, 420, 423, 430:
		return fmt.Errorf("%w: %w", errArticleNotFound, wire)
	case 400, 502:
		return fmt.Errorf("%w: %w", errConnectionRefusedByServer, wire)
	case 480, 481, 482:
		return fmt.Errorf("%w: %w", errAuth, wire)
	default:
		return fmt.Errorf("%w: %w", errProtocol, wire)
	}
}

// fatalForConn reports whether an error means this connection can no longer be
// trusted to be in a known state. A 430 does not: the server answered a
// question and the stream is still framed.
func fatalForConn(err error) bool {
	if err == nil {
		return false
	}
	return !errors.Is(err, errArticleNotFound)
}

// conn is one NNTP session. It is NOT safe for concurrent use: the pool hands
// exactly one caller a conn at a time, which is also what keeps pipelined
// responses matchable to their commands, since NNTP answers in order.
type conn struct {
	nc net.Conn
	br *bufio.Reader
	bw *bufio.Writer

	// broken marks a connection whose stream position is unknown. The pool
	// closes rather than reuses it.
	broken bool

	// wireBytes is the cumulative count of multi-line body bytes read on this
	// connection. The pool takes a delta from it for quota accounting, which
	// is why it counts WIRE bytes (what the provider meters) and not decoded
	// bytes (what lands on disk).
	wireBytes int64

	ioTimeout  time.Duration
	maxArticle int64
}

// dialConn opens, greets and authenticates one connection to p.
func dialConn(ctx context.Context, p Provider, connectTimeout, ioTimeout time.Duration, maxArticle int64) (*conn, error) {
	addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))

	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	var (
		nc  net.Conn
		err error
	)
	d := &net.Dialer{}
	if p.TLS {
		td := &tls.Dialer{NetDialer: d, Config: &tls.Config{
			ServerName: p.Host,
			// Providers all run TLS 1.2+; SABnzbd enforces the same floor.
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: p.InsecureSkipVerify, //nolint:gosec // opt-in, for a self-signed on-prem relay.
		}}
		nc, err = td.DialContext(dialCtx, "tcp", addr)
	} else {
		nc, err = d.DialContext(dialCtx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("usenet: dial %s: %w", p.Name, err)
	}

	c := &conn{
		nc:         nc,
		br:         bufio.NewReaderSize(nc, statusLineMax),
		bw:         bufio.NewWriter(nc),
		ioTimeout:  ioTimeout,
		maxArticle: maxArticle,
	}

	stop := c.armDeadline(dialCtx)
	defer stop()

	code, text, err := c.readStatus()
	if err != nil {
		c.close()
		return nil, fmt.Errorf("usenet: greeting from %s: %w", p.Name, err)
	}
	// 200 posting allowed, 201 no posting. Anything else is a refusal.
	if code != 200 && code != 201 {
		c.close()
		return nil, fmt.Errorf("usenet: greeting from %s: %w", p.Name, statusError(code, text))
	}

	if p.Username != "" {
		if err := c.authenticate(p.Username, p.Password); err != nil {
			c.close()
			return nil, fmt.Errorf("usenet: authenticate to %s: %w", p.Name, err)
		}
	}
	return c, nil
}

// authenticate performs the AUTHINFO USER/PASS exchange (RFC 4643).
func (c *conn) authenticate(username, password string) error {
	if err := c.writeCommand("AUTHINFO USER " + username); err != nil {
		return err
	}
	code, text, err := c.readStatus()
	if err != nil {
		return err
	}
	switch code {
	case 281: // accepted without a password
		return nil
	case 381: // password required
	default:
		return statusError(code, text)
	}

	if err := c.writeCommand("AUTHINFO PASS " + password); err != nil {
		return err
	}
	code, text, err = c.readStatus()
	if err != nil {
		return err
	}
	if code != 281 {
		return statusError(code, text)
	}
	return nil
}

// ping is the keepalive providers need on an idle connection; DATE is what
// nntppool uses and it is cheap on every server.
func (c *conn) ping(ctx context.Context) error {
	stop := c.armDeadline(ctx)
	defer stop()

	if err := c.writeCommand("DATE"); err != nil {
		return err
	}
	code, text, err := c.readStatus()
	if err != nil {
		return err
	}
	if code != 111 {
		return statusError(code, text)
	}
	return nil
}

// quit is a best-effort QUIT so the provider frees the slot promptly instead
// of waiting for its idle timeout.
func (c *conn) quit() {
	_ = c.nc.SetDeadline(time.Now().Add(2 * time.Second))
	_ = c.writeCommand("QUIT")
	_, _, _ = c.readStatus()
}

func (c *conn) close() {
	_ = c.nc.Close()
}

// armDeadline bounds the next exchange and makes ctx cancellation actually
// interrupt a blocked read. A net.Conn has no context, so cancellation is
// expressed by moving the deadline into the past; the returned func must be
// called to release the watchdog.
func (c *conn) armDeadline(ctx context.Context) func() {
	deadline := time.Now().Add(c.ioTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.nc.SetDeadline(deadline)

	stop := context.AfterFunc(ctx, func() {
		// A deadline in the past makes every in-flight and subsequent I/O
		// fail immediately. The connection is unusable afterwards, which is
		// correct: a cancelled fetch leaves the stream mid-body.
		_ = c.nc.SetDeadline(time.Now())
	})
	return func() { stop() }
}

// writeCommand buffers one command line. Callers flush.
//
// It rejects a command containing CR or LF. That is not defensive
// hand-wringing: message-ids come out of an NZB, which is attacker-controlled
// input from an indexer, and an id containing "\r\nQUIT" would otherwise
// inject a second command onto the wire.
func (c *conn) writeCommand(cmd string) error {
	if err := c.bufferCommand(cmd); err != nil {
		return err
	}
	return c.bw.Flush()
}

// bufferCommand queues a command without flushing, so [conn.pipeline] can put
// a whole window on the wire in one write instead of one per command.
func (c *conn) bufferCommand(cmd string) error {
	if strings.ContainsAny(cmd, "\r\n") {
		return fmt.Errorf("%w: command contains a line break", errProtocol)
	}
	if _, err := c.bw.WriteString(cmd); err != nil {
		c.broken = true
		return err
	}
	if _, err := c.bw.WriteString("\r\n"); err != nil {
		c.broken = true
		return err
	}
	return nil
}

// readStatus reads one response line and splits off its three-digit code.
func (c *conn) readStatus() (int, string, error) {
	line, err := c.br.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			c.broken = true
			return 0, "", fmt.Errorf("%w: status line longer than %d bytes", errProtocol, statusLineMax)
		}
		return 0, "", err
	}
	s := strings.TrimRight(string(line), "\r\n")
	if len(s) < 3 {
		c.broken = true
		return 0, "", fmt.Errorf("%w: short response %q", errProtocol, s)
	}
	code, err := strconv.Atoi(s[:3])
	if err != nil {
		c.broken = true
		return 0, "", fmt.Errorf("%w: unparseable response %q", errProtocol, s)
	}
	return code, strings.TrimSpace(s[3:]), nil
}

// validMessageID reports whether id is usable on the wire. NZB segment bodies
// carry the id WITHOUT angle brackets and this client adds them, so an id that
// already has them, or that contains whitespace or control characters, is
// refused rather than escaped.
func validMessageID(id string) bool {
	if id == "" || len(id) > 250 {
		return false
	}
	for i := range len(id) {
		ch := id[i]
		if ch <= 0x20 || ch >= 0x7f || ch == '<' || ch == '>' {
			return false
		}
	}
	return true
}

// articleSink receives one pipelined response. Exactly one of r and err is
// meaningful: r is non-nil only when the server returned the success code.
// The sink must not retain r past its call.
//
// A non-nil return aborts the pipeline; the connection is drained and closed,
// because there is no way to know how many responses are still queued behind
// the abort.
type articleSink func(i int, r io.Reader, err error) error

// pipeline issues verb for each id, keeping up to depth commands in flight,
// and delivers responses to sink IN ORDER -- which is safe because NNTP
// answers in order and this conn has exactly one caller.
//
// This is the behaviour Tensai75/nntp cannot express and the reason a
// high-latency provider is not limited to one article per round trip.
//
// multiline says whether the success code is followed by a dot-terminated
// block (BODY) or not (STAT). The returned error is fatal to the connection;
// per-article outcomes go to sink.
func (c *conn) pipeline(ctx context.Context, verb string, ids []string, depth, okCode int, multiline bool, sink articleSink) error {
	if depth < 1 {
		depth = 1
	}
	// Validate the whole batch before a single byte goes out. Skipping one id
	// mid-flight would leave the response stream one short of the commands
	// sent, which is how a client starts serving one article's bytes for
	// another; callers filter invalid ids instead (see [Pool.FetchBatch]).
	for _, id := range ids {
		if !validMessageID(id) {
			return fmt.Errorf("%w: invalid message id %q", errProtocol, id)
		}
	}

	stop := c.armDeadline(ctx)
	defer stop()

	sent := 0
	for got := range ids {
		queued := false
		for sent < len(ids) && sent-got < depth {
			if err := c.bufferCommand(verb + " <" + ids[sent] + ">"); err != nil {
				c.broken = true
				return err
			}
			sent++
			queued = true
		}
		if queued {
			// One write for the whole window: that is what makes this
			// pipelining rather than a loop of round trips.
			if err := c.bw.Flush(); err != nil {
				c.broken = true
				return err
			}
		}

		code, text, err := c.readStatus()
		if err != nil {
			c.broken = true
			return err
		}
		if code != okCode {
			serr := statusError(code, text)
			if fatalForConn(serr) {
				c.broken = true
				return serr
			}
			if err := sink(got, nil, serr); err != nil {
				c.broken = true
				return err
			}
			continue
		}

		if !multiline {
			if err := sink(got, nil, nil); err != nil {
				c.broken = true
				return err
			}
			continue
		}

		dr := &dotReader{br: c.br, limit: c.maxArticle * 2}
		sinkErr := sink(got, dr, nil)
		// Always drain: the next response starts after this body's ".\r\n",
		// and a sink that read only the yEnc part would otherwise leave the
		// stream pointing into the middle of an article.
		drainErr := dr.drain()
		c.wireBytes += dr.n
		switch {
		case dr.overflow:
			c.broken = true
			return fmt.Errorf("%w: %d bytes on the wire for one article", ErrArticleTooLarge, dr.n)
		case drainErr != nil:
			c.broken = true
			return drainErr
		case sinkErr != nil:
			c.broken = true
			return sinkErr
		}
	}
	return nil
}

// dotReader hands out the bytes of one multi-line response VERBATIM, including
// the terminating ".\r\n" and without un-stuffing leading dots.
//
// That is deliberate and it is the whole reason this type exists.
// github.com/javi11/rapidyenc decodes raw NNTP: it un-stuffs dots itself and
// uses the literal "\r\n.\r\n" to detect the end of the article. A reader that
// helpfully cooked the stream first -- as Tensai75/nntp's bodyReader does --
// makes every decode fail with ErrDataCorruption.
type dotReader struct {
	br    *bufio.Reader
	st    dotState
	done  bool
	n     int64
	limit int64
	// overflow records that limit was passed. The connection is unusable
	// afterwards because the rest of the body is still on the wire.
	overflow bool
}

type dotState int

const (
	// stLineStart is the position immediately after a LF, which is where the
	// reader begins: the status line's own LF has just been consumed.
	stLineStart dotState = iota
	stInLine
	stDot
	stDotCR
)

func (d *dotReader) Read(p []byte) (int, error) {
	switch {
	case d.done:
		return 0, io.EOF
	case d.overflow:
		return 0, ErrArticleTooLarge
	case len(p) == 0:
		return 0, nil
	}

	if d.br.Buffered() == 0 {
		// Force one read from the network. Peek blocks until at least one
		// byte is available or the read fails.
		if _, err := d.br.Peek(1); err != nil {
			return 0, err
		}
	}
	avail := min(d.br.Buffered(), len(p))
	buf, err := d.br.Peek(avail)
	if err != nil {
		return 0, err
	}

	n, term := d.scan(buf)
	copy(p, buf[:n])
	if _, err := d.br.Discard(n); err != nil {
		return 0, err
	}
	d.n += int64(n)
	if d.limit > 0 && d.n > d.limit {
		d.overflow = true
		return n, ErrArticleTooLarge
	}
	if term {
		d.done = true
	}
	return n, nil
}

// scan advances the terminator state machine across buf and returns how many
// bytes belong to this response, plus whether the terminator completed.
func (d *dotReader) scan(buf []byte) (int, bool) {
	for i := range buf {
		switch ch := buf[i]; d.st {
		case stLineStart:
			switch ch {
			case '.':
				d.st = stDot
			case '\n':
				d.st = stLineStart
			default:
				d.st = stInLine
			}
		case stInLine:
			if ch == '\n' {
				d.st = stLineStart
			}
		case stDot:
			switch ch {
			case '\r':
				d.st = stDotCR
			case '\n':
				// ".\n" -- a server that omits the CR. Accept it; the
				// alternative is hanging on a stream that will never frame.
				d.st = stLineStart
				return i + 1, true
			default:
				d.st = stInLine
			}
		case stDotCR:
			if ch == '\n' {
				d.st = stLineStart
				return i + 1, true
			}
			d.st = stInLine
		}
	}
	return len(buf), false
}

// drain consumes whatever the sink did not, so the connection is positioned at
// the next response.
func (d *dotReader) drain() error {
	if d.done || d.overflow {
		return nil
	}
	var scratch [4096]byte
	for {
		_, err := d.Read(scratch[:])
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.done {
			return nil
		}
	}
}
