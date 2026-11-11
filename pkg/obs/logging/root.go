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
	"errors"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"os"

	"github.com/mediactl/clustarr/pkg/version"
)

// ServiceNamespace is the product every clustarr process logs under
// (design §3; cluster-plex's is cluster-plex).
const ServiceNamespace = "clustarr"

// ServiceFor is the identity of the process named name: clustarr, the
// build's version, and the pod's name ($POD_NAME, else the hostname).
func ServiceFor(name string) Service {
	id := os.Getenv("POD_NAME")
	if id == "" {
		id, _ = os.Hostname()
	}
	return Service{Namespace: ServiceNamespace, Name: name, Version: version.Version, InstanceID: id}
}

// Root builds the one logger a process runs with (design §4, §5): the
// options resolved from opts.ConfigPath's logging: section, the flags that
// were set and the environment, with svc stamped on every record; installed
// on ctx for FromContext; slog's default and the log package drained
// through it under the stdlog subsystem; and, when opts.ConfigPath is set,
// the file watched under ctx for the rest of the process. A missing file
// is not an error, and a file that does not parse at start is reported once
// by the watcher while the flags and environment decide the levels.
func Root(ctx context.Context, opts Options, svc Service) (context.Context, *slog.Logger, error) {
	var (
		fc       FileConfig
		haveFile bool
	)
	if opts.ConfigPath != "" {
		f, err := ReadConfig(opts.ConfigPath)
		switch {
		case err == nil:
			fc, haveFile = f, true
		case errors.Is(err, fs.ErrNotExist):
		default:
			// Unreadable or unparsable: the watcher says so once, and the
			// process starts on the flags and the environment meanwhile.
		}
	}
	// Options built in code rather than from flags (a test's, a tool's)
	// are what their caller decided: every field counts as set.
	changed := opts.Changed
	if opts.flags == nil {
		changed = func(string) bool { return true }
	}
	resolved, err := Resolve(fc, haveFile, opts, changed, os.Getenv)
	if err != nil && haveFile {
		resolved, err = Resolve(FileConfig{}, false, opts, changed, os.Getenv)
	}
	if err != nil {
		return ctx, nil, fmt.Errorf("logging: %w", err)
	}
	resolved.Service = svc
	root := New(resolved)
	ctx = NewContext(ctx, root)
	slog.SetDefault(SubsystemLogger(root, "stdlog"))
	if opts.ConfigPath != "" {
		go WatchFile(ctx, opts.ConfigPath, LevelsOf(root), opts, changed, os.Getenv, DefaultPollInterval)
	}
	return ctx, root, nil
}

// StdLogger is a *log.Logger writing at level under subsystem through the
// context's logger, for the libraries that take one (http.Server.ErrorLog).
func StdLogger(ctx context.Context, subsystem string, level slog.Level) *log.Logger {
	return slog.NewLogLogger(SubsystemLogger(FromContext(ctx), subsystem).Handler(), level)
}
