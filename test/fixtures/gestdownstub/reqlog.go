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

package gestdownstub

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
// test/fixtures/torznabstub/reqlog.go's identical reasoning.
const maxLogBytes = 4 << 20

// Entry is one line of the JSONL request log.
type Entry struct {
	At     time.Time `json:"at"`
	Path   string    `json:"path"`
	Status int       `json:"status"`
}

// reqLog appends Entries to a file on the shared /data volume.
type reqLog struct {
	mu   sync.Mutex
	path string
}

func newReqLog(path string) *reqLog { return &reqLog{path: path} }

// record appends one entry, skipping kubelet's own probe traffic -- see
// test/fixtures/torznabstub/reqlog.go's identical record for why every
// failure here is swallowed rather than surfaced.
func (l *reqLog) record(r *http.Request, status int) {
	if strings.HasPrefix(r.UserAgent(), "kube-probe/") {
		return
	}
	if l.path == "" {
		return
	}
	e := Entry{At: time.Now().UTC(), Path: r.URL.Path, Status: status}
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
