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

package logging

import (
	"context"
	"log/slog"
	"sync"
)

// Subsystem returns ctx carrying its logger under name: every line below
// carries subsystem=name and is gated by name's level (design §5). A
// package's entry point -- a Reconcile, a worker's handler, a server's
// constructor -- names its subsystem once; code below inherits it. A nested
// call replaces the name, so the innermost unit wins and the key is one.
func Subsystem(ctx context.Context, name string) context.Context {
	return NewContext(ctx, SubsystemLogger(FromContext(ctx), name))
}

// SubsystemLogger is l writing under name, for a component that takes a
// *slog.Logger rather than a context. A logger that is not this package's
// (a test's) gets the key as a plain attribute.
func SubsystemLogger(l *slog.Logger, name string) *slog.Logger {
	if h, ok := l.Handler().(*Handler); ok {
		return slog.New(h.WithSubsystem(name))
	}
	return l.With("subsystem", name)
}

// LevelsOf is the Levels l's handler reads, or nil for a logger that is
// not this package's.
func LevelsOf(l *slog.Logger) *Levels {
	if h, ok := l.Handler().(*Handler); ok {
		return h.levels
	}
	return nil
}

// Watch calls fn with whether subsystem logs at level, once now and again
// whenever that changes (a bridge that sets a library's own level from the
// override, such as FFmpeg's, design §4), until cancel is called. A context
// whose logger is not this package's is watched as never enabled.
func Watch(ctx context.Context, subsystem string, at slog.Level, fn func(enabled bool)) (cancel func()) {
	lv := LevelsOf(FromContext(ctx))
	if lv == nil {
		fn(false)
		return func() {}
	}
	var mu sync.Mutex
	last := at >= lv.For(subsystem)
	fn(last)
	return lv.OnChange(func() {
		mu.Lock()
		defer mu.Unlock()
		now := at >= lv.For(subsystem)
		if now != last {
			last = now
			fn(now)
		}
	})
}
