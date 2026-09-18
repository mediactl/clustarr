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

// Package logging is Clustarr's *slog.Logger, carried through context.
//
// There is no package-level logger, no global default and no logger field on
// any struct (see the amendment, §A2.1, and CLAUDE.md's logging invariant):
// the logger travels in the context, and handlers and reconcilers enrich it
// once at entry with [With] so every line logged below carries the request's
// identity without threading an extra parameter through every call.
//
// A service builds its root logger once at startup with [New], installs it
// on the base context with [NewContext], and bridges it to controller-runtime
// with [LogrBridge] for ctrl.SetLogger so controller-runtime's own output
// joins the same stream in the same format.
package logging

import (
	"io"
	"log/slog"
	"os"

	"github.com/spf13/pflag"
)

// Options configures the logger [New] builds.
type Options struct {
	// Level is the minimum level the logger emits. The zero value is
	// slog.LevelInfo.
	Level slog.Level

	// Format selects the handler. "text" selects slog.NewTextHandler for
	// output meant to be read directly, such as a developer's terminal.
	// Anything else, including the zero value, selects slog.NewJSONHandler,
	// which every Clustarr Deployment runs with in-cluster so log lines are
	// structured for collection.
	Format string

	// AddSource adds the file and line of the log call to every record.
	AddSource bool

	// Output is where the logger writes. Nil defaults to os.Stderr.
	Output io.Writer
}

// New builds a root *slog.Logger from opts.
func New(opts Options) *slog.Logger {
	out := opts.Output
	if out == nil {
		out = os.Stderr
	}

	handlerOpts := &slog.HandlerOptions{
		AddSource: opts.AddSource,
		Level:     opts.Level,
	}

	var handler slog.Handler
	if opts.Format == "text" {
		handler = slog.NewTextHandler(out, handlerOpts)
	} else {
		handler = slog.NewJSONHandler(out, handlerOpts)
	}
	return slog.New(handler)
}

// BindFlags registers the flags that configure opts on fs, using opts'
// current field values as each flag's default.
//
// A command builds its default Options first, then calls BindFlags so cobra
// fills opts in place during flag parsing -- the same pattern
// cmd/clustarr/flags.go's bindCommonFlags uses for k8s.Options.
func BindFlags(fs *pflag.FlagSet, opts *Options) {
	fs.Var((*levelValue)(&opts.Level), "log-level",
		"Minimum log level: debug, info, warn, or error.")
	fs.StringVar(&opts.Format, "log-format", opts.Format,
		`Log encoding: "json" or "text".`)
	fs.BoolVar(&opts.AddSource, "log-add-source", opts.AddSource,
		"Add the source file and line to every log record.")
}

// levelValue adapts *slog.Level to pflag.Value by way of slog's own text
// codec, so --log-level accepts the same case-insensitive spellings
// (DEBUG, info, WARN+4, ...) that slog.Level.UnmarshalText accepts
// everywhere else in the stack.
type levelValue slog.Level

func (l *levelValue) String() string {
	return (*slog.Level)(l).String()
}

func (l *levelValue) Set(s string) error {
	return (*slog.Level)(l).UnmarshalText([]byte(s))
}

func (l *levelValue) Type() string {
	return "level"
}
