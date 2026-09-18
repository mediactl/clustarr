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

package obs

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// syncBuffer is an io.Writer a logger can be pointed at from several
// goroutines. slog.Handler does not serialize writes for us.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestBootstrapBridgesControllerRuntimeToSlog is the assertion whose absence
// let the bridge ship as dead code.
//
// cmd/clustarr's PersistentPreRunE used to call ctrl.SetLogger(zap.New(...))
// before any RunE, and controller-runtime's delegating sink honours only the
// FIRST SetLogger per process. Every service's own
// ctrl.SetLogger(logging.LogrBridge(...)) was therefore a silent no-op, and
// one process emitted two different JSON schemas: slog's {time,level,msg}
// from the services and zap's {ts,level,logger,msg} from controller-runtime.
// Nothing failed, so six reviews missed it.
//
// The test asserts what an operator would actually have seen: a line written
// through ctrl.Log -- controller-runtime's own logger, the one
// ctrl.LoggerFrom(ctx) falls back to -- has to land in the slog handler
// Bootstrap built, with slog's keys and not zap's.
func TestBootstrapBridgesControllerRuntimeToSlog(t *testing.T) {
	var out syncBuffer

	ctx, shutdown, err := Bootstrap(context.Background(),
		logging.Options{Output: &out, Level: slog.LevelDebug},
		tracing.Options{ServiceName: "bootstrap-test"})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(shutdown)

	if logging.FromContext(ctx) == nil {
		t.Fatal("Bootstrap returned a context with no logger")
	}

	ctrl.Log.Info("probe", "k", "v")

	line := out.String()
	if line == "" {
		t.Fatalf("ctrl.Log.Info wrote nothing to Bootstrap's logger: " +
			"controller-runtime is still logging somewhere else")
	}

	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(strings.TrimSpace(line), "\n", 2)[0]), &rec); err != nil {
		t.Fatalf("ctrl.Log output is not the JSON slog emits: %v\n%s", err, line)
	}

	// slog's JSONHandler keys. zap's are "ts" and "logger"; if those show up
	// the bridge was not installed and controller-runtime kept its own
	// backend.
	for _, key := range []string{"time", "level", "msg"} {
		if _, ok := rec[key]; !ok {
			t.Errorf("ctrl.Log record has no %q key, want slog's schema: %v", key, rec)
		}
	}
	for _, key := range []string{"ts", "logger", "stacktrace"} {
		if _, ok := rec[key]; ok {
			t.Errorf("ctrl.Log record has zap's %q key: controller-runtime is not on the slog bridge: %v",
				key, rec)
		}
	}
	if rec["msg"] != "probe" {
		t.Errorf("msg = %v, want \"probe\"", rec["msg"])
	}
	if rec["k"] != "v" {
		t.Errorf("k = %v, want \"v\": the bridge dropped the key/value args", rec["k"])
	}

	// Part two, in the same test function because ctrl.SetLogger is
	// genuinely single-shot per PROCESS: only the first Bootstrap in this
	// test binary can bind controller-runtime at all, so a second test
	// function could not observe the binding no matter what it did.
	//
	// This pins why Bootstrap holds a sync.Once instead of calling
	// ctrl.SetLogger directly: `clustarr all` calls Bootstrap seven times in
	// one process, and a later caller's logger legitimately does not win.
	// What must not happen is the code pretending otherwise.
	t.Run("a second Bootstrap does not rebind controller-runtime", func(t *testing.T) {
		var second syncBuffer
		ctx2, shutdown2, err := Bootstrap(context.Background(),
			logging.Options{Output: &second}, tracing.Options{})
		if err != nil {
			t.Fatalf("second Bootstrap: %v", err)
		}
		t.Cleanup(shutdown2)

		before := len(out.String())
		ctrl.Log.Info("after the second bootstrap")

		if second.String() != "" {
			t.Errorf("the second Bootstrap rebound controller-runtime's logger; it honours "+
				"only the first SetLogger per process: %s", second.String())
		}
		if !strings.Contains(out.String()[before:], "after the second bootstrap") {
			t.Errorf("controller-runtime's logger is no longer the first Bootstrap's: %s",
				out.String()[before:])
		}

		// Each caller still gets its OWN logger on its OWN context, which
		// is what every service actually logs through; only the
		// controller-runtime binding is process-wide.
		logging.FromContext(ctx2).Info("service line")
		if !strings.Contains(second.String(), "service line") {
			t.Errorf("the second Bootstrap's context does not carry its own logger: %s", second.String())
		}
	})
}
