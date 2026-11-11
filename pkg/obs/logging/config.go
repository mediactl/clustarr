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
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// DefaultConfigPath is where the chart mounts the logging ConfigMap and
// --log-config's default (design §6).
const DefaultConfigPath = DefaultConfigDir + "/logging.yaml"

// DefaultConfigDir is the directory the installers and the manager mount the
// logging ConfigMap at, as a directory and never a subPath (a subPath mount
// never receives the ConfigMap's updates).
const DefaultConfigDir = "/etc/clustarr"

// FileConfig is the config file's "logging:" section (design §4).
type FileConfig struct {
	Level      string            `json:"level,omitempty"`
	Format     string            `json:"format,omitempty"`
	AddSource  *bool             `json:"addSource,omitempty"`
	Subsystems map[string]string `json:"subsystems,omitempty"`
}

// ReadConfig reads path's "logging:" key. A missing file's error wraps
// fs.ErrNotExist, so a caller can tell it from a bad one.
func ReadConfig(path string) (FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FileConfig{}, fmt.Errorf("read logging config: %w", err)
	}
	return parseConfig(data)
}

// parseConfig reads the "logging:" key, strictly within the section and
// leaving the file's other top-level sections alone (cluster-plex's
// config.yaml carries its own).
func parseConfig(data []byte) (FileConfig, error) {
	var raw struct {
		Logging map[string]any `json:"logging"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return FileConfig{}, fmt.Errorf("parse logging config: %w", err)
	}
	section, err := yaml.Marshal(raw.Logging)
	if err != nil {
		return FileConfig{}, fmt.Errorf("parse logging config: %w", err)
	}
	var f FileConfig
	if err := yaml.UnmarshalStrict(section, &f); err != nil {
		return FileConfig{}, fmt.Errorf("parse logging config: %w", err)
	}
	return f, nil
}

// Resolve settles the options a process runs with (design §4): for the
// level and the subsystems the file (when haveFile), then a flag that was
// set, then LOG_LEVEL; for the format and addSource the same order over
// --log-format, --log-add-source and LOG_FORMAT, read at start only. The
// file's subsystems replace the flags' whole. changed reports whether a
// flag was set on the command line; getenv reads the environment.
func Resolve(file FileConfig, haveFile bool, flags Options, changed func(string) bool, getenv func(string) string) (Options, error) {
	out := flags
	switch {
	case haveFile && file.Level != "":
		l, err := ParseLevel(file.Level)
		if err != nil {
			return out, fmt.Errorf("logging config level %q: %w", file.Level, err)
		}
		out.Level = l
	case changed("log-level"):
		out.Level = flags.Level
	case getenv("LOG_LEVEL") != "":
		l, err := ParseLevel(getenv("LOG_LEVEL"))
		if err != nil {
			return out, fmt.Errorf("LOG_LEVEL %q: %w", getenv("LOG_LEVEL"), err)
		}
		out.Level = l
	default:
		out.Level = slog.LevelInfo
	}

	switch {
	case haveFile && file.Format != "":
		out.Format = file.Format
	case changed("log-format") && flags.Format != "":
		out.Format = flags.Format
	case getenv("LOG_FORMAT") != "":
		out.Format = getenv("LOG_FORMAT")
	default:
		out.Format = "json"
	}
	if out.Format != "json" && out.Format != "text" {
		return out, fmt.Errorf("log format %q: want json or text", out.Format)
	}

	switch {
	case haveFile && file.AddSource != nil:
		out.AddSource = *file.AddSource
	case changed("log-add-source"):
		out.AddSource = flags.AddSource
	default:
		out.AddSource = false
	}

	if haveFile && file.Subsystems != nil {
		subs, err := ParseSubsystems(file.Subsystems)
		if err != nil {
			return out, err
		}
		out.Subsystems = subs
	}
	return out, nil
}

// ParseSubsystems reads a subsystem-to-level map as the file spells it.
func ParseSubsystems(in map[string]string) (map[string]slog.Level, error) {
	out := make(map[string]slog.Level, len(in))
	names := make([]string, 0, len(in))
	for name := range in {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		l, err := ParseLevel(in[name])
		if err != nil {
			return nil, fmt.Errorf("logging config subsystem %q: level %q: %w", name, in[name], err)
		}
		out[strings.TrimSpace(name)] = l
	}
	return out, nil
}
