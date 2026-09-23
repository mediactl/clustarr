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

// Package nntpstub serves a real NNTP upstream -- a real greeting,
// AUTHINFO, DATE, QUIT and BODY/STAT for a real yEnc-encoded article set,
// parsed by the same github.com/javi11/rapidyenc decoder
// pkg/download/usenet uses. It never reaches the Internet: every article it
// can serve comes from a [Fixture] built in-process.
//
// Its command set is deliberately the exact subset
// pkg/download/usenet/conn.go speaks (greeting, AUTHINFO USER/PASS, DATE,
// QUIT, pipelined BODY/STAT) -- proven sufficient because it is the same
// subset pkg/download/usenet/stub_test.go's in-process stub implements, and
// that stub is what the real client's own tests run against.
//
// A Server's defining feature is [Options.Deny]: two Servers built from the
// SAME Fixture but different Deny sets are two "providers" of one release
// that disagree about which articles they carry -- exactly the shape
// pkg/download/usenet's pool needs on the wire to prove 430 cross-server
// failover end to end, not only in its own in-process unit tests.
package nntpstub

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/javi11/rapidyenc"
)

// DefaultDenyStatus is the status [Options.Deny] uses for an id whose
// configured code is 0. 430 ("no such article") is what
// pkg/download/usenet/conn.go's statusError treats as the failover trigger;
// 411, 412, 420 and 423 are accepted too, if a caller wants to prove those
// are handled identically.
const DefaultDenyStatus = 430

// Options configures one [Server].
type Options struct {
	// Addr is the NNTP listen address, "host:port". Empty defaults to
	// ":0" (ephemeral port, all interfaces).
	Addr string

	// Deny maps a message-id (no angle brackets, matching [Article.ID]) this
	// Server refuses to a status code. A zero code means [DefaultDenyStatus].
	// Nil or empty means this Server carries every article in its Fixture --
	// a healthy provider.
	Deny map[string]int

	// Username/Password require AUTHINFO when Username is non-empty,
	// matching NNTPProvider's optional credentials
	// (api/download/v1alpha1/downloadclient_types.go).
	Username, Password string

	// RequestLog is a JSONL file on the shared /data volume every BODY/STAT
	// is appended to. Empty disables logging.
	RequestLog string

	Logger *slog.Logger
}

// Server serves the read-only NNTP subset described in the package doc,
// backed by one [Fixture].
type Server struct {
	fx   Fixture
	byID map[string]Article
	deny map[string]int

	authRequired bool
	user, pass   string

	logger *slog.Logger
	reqlog *reqLog

	ln net.Listener
	wg sync.WaitGroup

	mu     sync.Mutex
	served map[string]int
	denied map[string]int
}

// NewServer binds opts.Addr and returns a Server ready for [Server.Serve].
func NewServer(fx Fixture, opts Options) (*Server, error) {
	addr := opts.Addr
	if addr == "" {
		addr = ":0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("nntpstub: listen %s: %w", addr, err)
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	byID := make(map[string]Article, len(fx.Articles))
	for _, a := range fx.Articles {
		byID[a.ID] = a
	}
	deny := make(map[string]int, len(opts.Deny))
	for id, code := range opts.Deny {
		if code == 0 {
			code = DefaultDenyStatus
		}
		deny[id] = code
	}

	return &Server{
		fx:           fx,
		byID:         byID,
		deny:         deny,
		authRequired: opts.Username != "",
		user:         opts.Username,
		pass:         opts.Password,
		logger:       logger,
		reqlog:       newReqLog(opts.RequestLog),
		ln:           ln,
		served:       map[string]int{},
		denied:       map[string]int{},
	}, nil
}

// Addr is the bound address, with any ephemeral port resolved.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Serve accepts connections until ctx is cancelled or the listener is
// closed by another goroutine. It always returns a non-nil error.
func (s *Server) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { _ = s.ln.Close() })
	defer stop()

	for {
		c, err := s.ln.Accept()
		if err != nil {
			s.wg.Wait()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(c)
		}()
	}
}

// Close closes the listener, causing Serve to return once in-flight
// connections finish.
func (s *Server) Close() error { return s.ln.Close() }

// Served reports how many times id was actually served (BODY, not denied).
func (s *Server) Served(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.served[id]
}

// Denied reports how many times id was refused on this Server.
func (s *Server) Denied(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.denied[id]
}

func (s *Server) handle(c net.Conn) {
	defer func() { _ = c.Close() }()

	w := bufio.NewWriter(c)
	br := bufio.NewReader(c)

	// 200: posting allowed. pkg/download/usenet/conn.go treats 200 or 201
	// as the only acceptable greetings.
	if _, err := w.WriteString("200 clustarr nntp-stub ready\r\n"); err != nil {
		return
	}
	if err := w.Flush(); err != nil {
		return
	}

	authed := !s.authRequired
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(cmd)

		switch {
		case strings.HasPrefix(upper, "AUTHINFO USER"):
			_, _ = w.WriteString("381 password required\r\n")
		case strings.HasPrefix(upper, "AUTHINFO PASS"):
			if strings.TrimSpace(cmd[len("AUTHINFO PASS"):]) == s.pass {
				authed = true
				_, _ = w.WriteString("281 authenticated\r\n")
			} else {
				_, _ = w.WriteString("481 authentication rejected\r\n")
			}
		case upper == "DATE":
			_, _ = w.WriteString("111 20260922120000\r\n")
		case upper == "QUIT":
			_, _ = w.WriteString("205 bye\r\n")
			_ = w.Flush()
			return
		case strings.HasPrefix(upper, "BODY "), strings.HasPrefix(upper, "STAT "):
			if !authed {
				_, _ = w.WriteString("480 authentication required\r\n")
				break
			}
			id := strings.Trim(strings.TrimSpace(cmd[5:]), "<>")
			s.serveArticle(w, id, strings.HasPrefix(upper, "BODY "))
		default:
			_, _ = w.WriteString("500 unknown command\r\n")
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}

func (s *Server) serveArticle(w *bufio.Writer, id string, wantBody bool) {
	s.mu.Lock()
	code, refused := s.deny[id]
	art, have := s.byID[id]
	s.mu.Unlock()

	if refused {
		s.logger.Info("nntpstub: refusing article on purpose", "id", id, "status", code)
		s.mu.Lock()
		s.denied[id]++
		s.mu.Unlock()
		s.reqlog.record(id, "denied", code)
		_, _ = fmt.Fprintf(w, "%d no such article\r\n", code)
		return
	}
	if !have {
		s.reqlog.record(id, "unknown", 430)
		_, _ = w.WriteString("430 no such article\r\n")
		return
	}
	if !wantBody {
		s.reqlog.record(id, "stat", 223)
		_, _ = fmt.Fprintf(w, "223 0 <%s> article exists\r\n", id)
		return
	}

	encoded, err := encodeArticle(art)
	if err != nil {
		s.logger.Warn("nntpstub: yEnc encode failed", "id", id, "error", err)
		_, _ = w.WriteString("503 internal stub error\r\n")
		return
	}

	s.mu.Lock()
	s.served[id]++
	s.mu.Unlock()
	s.reqlog.record(id, "served", 222)

	_, _ = fmt.Fprintf(w, "222 0 <%s> body follows\r\n", id)
	_, _ = w.Write(dotStuff(encoded))
	_, _ = w.WriteString(".\r\n")
}

// encodeArticle yEnc-encodes one part with its =ybegin/=ypart/=yend
// headers, exactly as a real poster's client would have produced it.
func encodeArticle(a Article) ([]byte, error) {
	var buf bytes.Buffer
	enc, err := rapidyenc.NewEncoder(&buf, a.Meta)
	if err != nil {
		return nil, err
	}
	if _, err := enc.Write(a.Data); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// dotStuff doubles a '.' at the start of a line, which RFC 3977 requires of
// a multi-line response and which the decoder undoes. The stub does it so
// the decode path is exercised the way a real server exercises it.
func dotStuff(b []byte) []byte {
	var out bytes.Buffer
	atLineStart := true
	for _, ch := range b {
		if atLineStart && ch == '.' {
			out.WriteByte('.')
		}
		out.WriteByte(ch)
		atLineStart = ch == '\n'
	}
	if out.Len() > 0 && !bytes.HasSuffix(out.Bytes(), []byte("\r\n")) {
		out.WriteString("\r\n")
	}
	return out.Bytes()
}

// NZBHandler serves fx's NZB body over HTTP, for a
// DownloadSource.nzbURL-style direct fetch
// (api/download/v1alpha1/download_types.go) that needs no indexer
// credentials.
func NZBHandler(fx Fixture) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/x-nzb; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(fx.NZB)
		}
	})
}
