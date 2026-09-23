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

package opensubtitlesstub

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxLogBytes caps the request log, mirroring
// test/fixtures/torznabstub/reqlog.go's identical reasoning: the readiness
// probe writes a line every few seconds for the life of the cluster, and a
// re-executed E2E_SKIP_BUILD=1 run keeps appending to the same file.
const maxLogBytes = 4 << 20

// Entry is one line of the JSONL request log. The e2e suite decodes it from
// the host side of the /data mount: it cannot reach this Service directly, so
// this file is the only evidence the stub was contacted on THIS run.
type Entry struct {
	At     time.Time `json:"at"`
	Path   string    `json:"path"`
	Mode   string    `json:"mode"`
	Status int       `json:"status"`
}

// reqLog appends Entries to a file on the shared /data volume.
type reqLog struct {
	mu   sync.Mutex
	path string
}

func newReqLog(path string) *reqLog { return &reqLog{path: path} }

// record appends one entry. A probe request (kube-probe's own User-Agent, or
// the control routes this stub itself serves) is skipped, matching
// torznabstub's reqLog.record: the scenario cares about the handful of real
// provider calls, not about kubelet's own polling or the test process
// flipping this pod's mode.
func (l *reqLog) record(r *http.Request, mode string, status int) {
	if strings.HasPrefix(r.UserAgent(), "kube-probe/") {
		return
	}
	if l.path == "" {
		return
	}
	e := Entry{At: time.Now().UTC(), Path: r.URL.Path, Mode: mode, Status: status}
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
