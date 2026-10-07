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

package k8s

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/healthz"
)

// The data check's timings. The kubelet probes every 10 s with a 1 s
// timeout, and a write to /data is four NFS round trips: on a saturated
// link one took 10 s (kind-cluster-plex, 2026-10-05), so a probe that wrote
// inline failed readiness on a mount that was only slow.
const (
	// dataProbeInterval is how old a successful write probe may be before
	// the next check starts another.
	dataProbeInterval = 30 * time.Second
	// dataProbeStall is how long a write probe may run before the mount
	// counts as hung and readiness fails.
	dataProbeStall = time.Minute
	// dataProbeWait is how long a check waits on a running probe before
	// answering from the last finished one; well inside the kubelet's 1 s.
	dataProbeWait = 500 * time.Millisecond
)

// DataReadyChecker reports whether path is a writable directory: the
// readiness gate of every process that writes the library (importarr's roles
// today; the import and caption agents' `import.data` and `caption.data`
// after the split, spec §3.3). /data is an RWX volume mounted from the
// cluster. On a dev box or a misconfigured Deployment it may not exist, which
// must fail readiness with a clear message rather than panic the process.
//
// The write runs in the background (dataCheck): a check answers from the
// last finished write, starts a new one once that is dataProbeInterval old
// (at once after a failure), and fails only on a write error or a write
// still running after dataProbeStall.
func DataReadyChecker(path string) healthz.Checker {
	d := &dataCheck{
		path:     path,
		write:    writeProbe,
		now:      time.Now,
		interval: dataProbeInterval,
		stall:    dataProbeStall,
		wait:     dataProbeWait,
	}
	return d.Check
}

// dataCheck runs at most one write probe at a time and remembers the last
// result.
type dataCheck struct {
	path     string
	write    func(path string) error
	now      func() time.Time
	interval time.Duration
	stall    time.Duration
	wait     time.Duration

	mu       sync.Mutex
	finished bool      // a probe has finished
	err      error     // the last finished probe's result
	at       time.Time // when it finished
	running  chan struct{}
	started  time.Time
}

func (d *dataCheck) Check(_ *http.Request) error {
	d.mu.Lock()
	if d.running == nil && (!d.finished || d.err != nil || d.now().Sub(d.at) >= d.interval) {
		d.start()
	}
	running := d.running
	d.mu.Unlock()

	if running != nil {
		select {
		case <-running:
		case <-time.After(d.wait):
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running != nil {
		if ran := d.now().Sub(d.started); ran >= d.stall {
			return fmt.Errorf("data path %s: a write probe has not finished in %s", d.path, ran.Round(time.Second))
		}
	}
	if !d.finished {
		return fmt.Errorf("data path %s: the first write probe has not finished", d.path)
	}
	return d.err
}

// start launches a probe; d.mu is held.
func (d *dataCheck) start() {
	done := make(chan struct{})
	d.running, d.started = done, d.now()
	go func() {
		err := d.write(d.path)
		d.mu.Lock()
		d.finished, d.err, d.at, d.running = true, err, d.now(), nil
		d.mu.Unlock()
		close(done)
	}()
}

// writeProbe stats path and creates and removes a file in it.
func writeProbe(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("data path %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("data path %s is not a directory", path)
	}
	probe, err := os.CreateTemp(path, ".clustarr-ready-*")
	if err != nil {
		return fmt.Errorf("data path %s is not writable: %w", path, err)
	}
	name := probe.Name()
	if cerr := probe.Close(); cerr != nil {
		_ = os.Remove(name)
		return fmt.Errorf("data path %s: closing probe file: %w", path, cerr)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("data path %s: removing probe file %s: %w", path, filepath.Base(name), err)
	}
	return nil
}
