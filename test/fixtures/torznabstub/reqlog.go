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

package torznabstub

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxLogBytes caps the request log. The readiness probe alone writes a line
// every few seconds for the life of the cluster, and an e2e run that is
// re-executed with E2E_SKIP_BUILD=1 keeps appending to the same file, so the
// log is truncated rather than allowed to grow without bound. Scenarios only
// ever read entries newer than their own start time, so losing old ones
// costs nothing.
const maxLogBytes = 4 << 20

// Entry is one line of the JSONL request log. The e2e suite decodes it from
// the host side of the /data mount: it cannot reach this Service, so this
// file is the only evidence that the fixture was contacted on THIS run.
type Entry struct {
	At     time.Time `json:"at"`
	Path   string    `json:"path"`
	Query  string    `json:"query"`
	T      string    `json:"t"`
	Status int       `json:"status"`
}

// reqLog appends Entries to a file on the shared /data volume.
type reqLog struct {
	mu   sync.Mutex
	path string
}

func newReqLog(path string) *reqLog { return &reqLog{path: path} }

// record appends one entry. A probe request is skipped: kubelet hits the
// caps route every few seconds, and those lines would drown the handful a
// scenario actually cares about.
//
// Every failure path here is swallowed on purpose. A stub that crashed
// because it could not write a diagnostic line would turn a missing
// directory into a CrashLoopBackOff and a twenty-minute mystery; the
// SCENARIO is what fails loudly when the log is absent, and it says why.
func (l *reqLog) record(r *http.Request, status int) {
	if strings.HasPrefix(r.UserAgent(), "kube-probe/") {
		return
	}
	if l.path == "" {
		return
	}
	e := Entry{
		At:     time.Now().UTC(),
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		T:      r.URL.Query().Get("t"),
		Status: status,
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o777); err != nil {
		return
	}
	if fi, err := os.Stat(l.path); err == nil && fi.Size() > maxLogBytes {
		_ = os.Truncate(l.path, 0)
	}
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(b, '\n'))
}
