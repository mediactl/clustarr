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

package nntpstub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// maxLogBytes caps the request log, the same discipline and the same limit
// test/fixtures/torznabstub/reqlog.go uses, and for the same reason: this
// log is the only evidence, from the host side of the /data mount, of which
// server actually served or refused an article on a given e2e run.
const maxLogBytes = 4 << 20

// Entry is one line of the JSONL request log. It is what lets D2-10 assert
// "server A denied seg1, server B served it" from outside the cluster
// network this Server's NNTP port lives on.
type Entry struct {
	At     time.Time `json:"at"`
	ID     string    `json:"id"`
	Action string    `json:"action"` // "served", "denied", "unknown", "stat"
	Status int       `json:"status"`
}

// reqLog appends Entries to a file on the shared /data volume.
type reqLog struct {
	mu   sync.Mutex
	path string
}

func newReqLog(path string) *reqLog { return &reqLog{path: path} }

// record appends one entry. Every failure path here is swallowed on
// purpose, exactly as torznabstub's reqlog explains: a stub that crashed
// because it could not write a diagnostic line would turn a missing
// directory into a CrashLoopBackOff, and the SCENARIO is what must fail
// loudly, not this fixture.
func (l *reqLog) record(id, action string, status int) {
	if l == nil || l.path == "" {
		return
	}
	e := Entry{At: time.Now().UTC(), ID: id, Action: action, Status: status}
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
