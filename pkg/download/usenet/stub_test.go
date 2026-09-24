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
	"bytes"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/javi11/rapidyenc"
)

// stubServer is an in-process NNTP server over loopback. Everything in these
// tests runs against it: no network, no fixture process, no provider account.
//
// It exists to make the behaviours the plan calls out observable rather than
// assumed. It counts what it served, refuses the articles a test tells it to
// refuse, notices when a client pipelines, and records the high-water mark of
// simultaneous connections -- which is the only way to prove a connection cap
// is real rather than declared.
type stubServer struct {
	ln net.Listener

	// articles maps a message-id (no angle brackets) to the part it serves.
	articles map[string]stubArticle
	// refuse maps a message-id to a status code, usually 430.
	refuse map[string]int

	// refuseTimes answers 430 for an article this many times, then serves
	// it: a propagation gap that closes.
	refuseTimes map[string]int

	// dropConn closes the connection instead of answering for an article
	// this many times: a connection that dies mid-batch.
	dropConn map[string]int

	requireAuth bool
	user, pass  string

	// greetBusy makes every new connection be answered 502, which is what a
	// provider does when the plan's connection cap is exceeded.
	greetBusy bool

	// bodyDelay slows each BODY, so concurrent demand actually overlaps.
	bodyDelay time.Duration

	mu            sync.Mutex
	served        map[string]int
	liveConns     int
	peakConns     int
	acceptedConns int
	sawPipelined  bool
	wg            sync.WaitGroup
	closed        bool
}

type stubArticle struct {
	data []byte
	meta rapidyenc.Meta
}

func newStubServer(tb testing.TB) *stubServer {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listen: %v", err)
	}
	s := &stubServer{
		ln:          ln,
		articles:    map[string]stubArticle{},
		refuse:      map[string]int{},
		refuseTimes: map[string]int{},
		dropConn:    map[string]int{},
		served:      map[string]int{},
	}
	go s.acceptLoop()
	tb.Cleanup(s.close)
	return s
}

func (s *stubServer) addr() (string, int) {
	a := s.ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

func (s *stubServer) provider(name string, conns int, priority int32) Provider {
	host, port := s.addr()
	return Provider{
		Name:        name,
		Host:        host,
		Port:        port,
		Connections: conns,
		Priority:    priority,
		Username:    s.user,
		Password:    s.pass,
	}
}

// addArticle stores one yEnc part. payload is the DECODED content; the stub
// encodes it on demand, exactly as a real server stores what a poster encoded.
func (s *stubServer) addArticle(id, fileName string, fileSize, offset int64, part, totalParts int, payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.articles[id] = stubArticle{
		data: payload,
		meta: rapidyenc.Meta{
			FileName:   fileName,
			FileSize:   fileSize,
			PartNumber: int64(part),
			TotalParts: int64(totalParts),
			Offset:     offset,
			PartSize:   int64(len(payload)),
		},
	}
}

func (s *stubServer) servedCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.served[id]
}

func (s *stubServer) stats() (peak, accepted int, pipelined bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peakConns, s.acceptedConns, s.sawPipelined
}

func (s *stubServer) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.ln.Close()
	s.wg.Wait()
}

func (s *stubServer) acceptLoop() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(c)
		}()
	}
}

func (s *stubServer) handle(c net.Conn) {
	defer func() { _ = c.Close() }()

	s.mu.Lock()
	s.acceptedConns++
	s.liveConns++
	if s.liveConns > s.peakConns {
		s.peakConns = s.liveConns
	}
	busy := s.greetBusy
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.liveConns--
		s.mu.Unlock()
	}()

	w := bufio.NewWriter(c)
	br := bufio.NewReader(c)

	if busy {
		_, _ = w.WriteString("502 too many connections\r\n")
		_ = w.Flush()
		return
	}
	_, _ = w.WriteString("200 clustarr stub ready\r\n")
	_ = w.Flush()

	authed := !s.requireAuth
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		// A command already waiting in the buffer means the client sent it
		// before this one was answered: that IS pipelining, observed rather
		// than inferred.
		if br.Buffered() > 0 {
			s.mu.Lock()
			s.sawPipelined = true
			s.mu.Unlock()
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
			s.mu.Lock()
			drop := s.dropConn[id] > 0
			if drop {
				s.dropConn[id]--
			}
			s.mu.Unlock()
			if drop {
				_ = w.Flush()
				return
			}
			s.serveArticle(w, id, strings.HasPrefix(upper, "BODY "))
		default:
			_, _ = w.WriteString("500 unknown command\r\n")
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}

func (s *stubServer) serveArticle(w *bufio.Writer, id string, wantBody bool) {
	s.mu.Lock()
	code, refused := s.refuse[id]
	if !refused && s.refuseTimes[id] > 0 {
		s.refuseTimes[id]--
		code, refused = 430, true
	}
	art, have := s.articles[id]
	delay := s.bodyDelay
	s.mu.Unlock()

	if refused {
		_, _ = fmt.Fprintf(w, "%d no such article\r\n", code)
		return
	}
	if !have {
		_, _ = w.WriteString("430 no such article\r\n")
		return
	}
	if !wantBody {
		_, _ = fmt.Fprintf(w, "223 0 <%s> article exists\r\n", id)
		return
	}
	if delay > 0 {
		time.Sleep(delay)
	}

	encoded, err := encodePart(art)
	if err != nil {
		_, _ = w.WriteString("503 internal stub error\r\n")
		return
	}

	s.mu.Lock()
	s.served[id]++
	s.mu.Unlock()

	_, _ = fmt.Fprintf(w, "222 0 <%s> body follows\r\n", id)
	_, _ = w.Write(dotStuff(encoded))
	_, _ = w.WriteString(".\r\n")
}

// encodePart yEnc-encodes one part with its =ybegin/=ypart/=yend headers.
func encodePart(a stubArticle) ([]byte, error) {
	var buf bytes.Buffer
	enc, err := rapidyenc.NewEncoder(&buf, a.meta)
	if err != nil {
		return nil, err
	}
	if _, err := enc.Write(a.data); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// dotStuff doubles a '.' at the start of a line, which is what RFC 3977
// requires of a multi-line response and what the decoder undoes. The stub does
// it so the decode path is exercised the way a real server exercises it.
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
