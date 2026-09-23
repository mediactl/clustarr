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

package cardigann

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// A bundle is a directory of Cardigann definitions loaded together at
// startup -- the corpus a cluster ships with rather than one an operator
// applies as IndexerDefinition objects one at a time.
//
// Clustarr does not embed one. The upstream corpus (Prowlarr/Indexers) has
// no licence at the commit hack/sync-cardigann pins, and its history
// descends from Jackett's GPL-2.0 tree, so vendoring it into a GPL-3.0
// binary is not something this project can do (ruling R-13; the evidence is
// in hack/sync-cardigann's package doc). LoadBundle therefore takes an
// fs.FS: a directory the operator mounts (what hack/sync-cardigann writes),
// or -- should the corpus ever be vendored -- an embed.FS, with no change
// here.

// MaxDefinitionBytes is the largest definition LoadBundle accepts: the
// 1 MiB MaxLength on IndexerDefinition.spec.yaml, so every bundled
// definition can be created as an IndexerDefinition. The v11 corpus's
// largest file is 64 KB.
const MaxDefinitionBytes = 1 << 20

// BundledDefinition is one definition LoadBundle accepted.
type BundledDefinition struct {
	// File is the definition's file name within the bundle.
	File string
	// YAML is the file's bytes, unchanged -- what an IndexerDefinition's
	// spec.yaml would carry.
	YAML []byte
	// Definition is YAML loaded (schema-validated and decoded).
	Definition *Definition
}

// ID is the definition's own id, the name an Indexer's spec.definition
// selects it by.
func (b BundledDefinition) ID() string { return b.Definition.ID }

// objectNameInvalid is every run of characters a Kubernetes object name may
// not contain.
var objectNameInvalid = regexp.MustCompile(`[^a-z0-9.-]+`)

// ObjectName is the IndexerDefinition name a bundled definition is created
// under: its id as a DNS-1123 subdomain -- lower-cased, other characters
// replaced by "-", trimmed of leading and trailing separators, capped at 253.
// The corpus needs this once, for "Bittorrentfiles". The id itself is
// untouched in spec.yaml, which is what an Indexer's spec.definition
// resolves against. Both producers of bundle objects name them through
// this -- hack/sync-cardigann's manifests and indexarr's bundle loader --
// so applying the one and mounting the other converge on the same objects
// instead of creating each definition twice. "" means the id yields no
// valid name.
func ObjectName(id string) string {
	name := objectNameInvalid.ReplaceAllString(strings.ToLower(id), "-")
	name = strings.Trim(name, "-.")
	if len(name) > 253 {
		name = strings.Trim(name[:253], "-.")
	}
	return name
}

// BundleIssue is one file LoadBundle refused, and why. Err is bounded (a
// schema failure is a *SchemaError), so it can go into a condition message.
type BundleIssue struct {
	File string
	Err  error
}

func (i BundleIssue) Error() string { return i.File + ": " + i.Err.Error() }

// Unwrap exposes the cause to errors.Is/As.
func (i BundleIssue) Unwrap() error { return i.Err }

// ErrDuplicateID is a BundleIssue's cause when a second file declares an id
// an earlier file (in name order) already provides.
var ErrDuplicateID = errors.New("cardigann: duplicate definition id")

// ErrDefinitionTooLarge is a BundleIssue's cause when a file exceeds
// MaxDefinitionBytes.
var ErrDefinitionTooLarge = errors.New("cardigann: definition exceeds the size limit")

// LoadBundle loads every *.yml and *.yaml file at the root of fsys (not
// recursively), in name order. A file that fails -- too large, rejected by
// Load, or declaring an id an earlier file already declared -- is reported
// as a BundleIssue and skipped; one bad definition never keeps the rest
// from loading. The error is only for fsys itself failing (an unreadable
// directory), in which case nothing is returned.
func LoadBundle(fsys fs.FS) ([]BundledDefinition, []BundleIssue, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("cardigann: read bundle: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(path.Ext(e.Name())) {
		case ".yml", ".yaml":
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var (
		out    []BundledDefinition
		issues []BundleIssue
		byID   = map[string]string{}
	)
	for _, name := range names {
		data, err := readCapped(fsys, name)
		if err != nil {
			issues = append(issues, BundleIssue{File: name, Err: err})
			continue
		}
		def, err := Load(data)
		if err != nil {
			issues = append(issues, BundleIssue{File: name, Err: err})
			continue
		}
		if first, dup := byID[def.ID]; dup {
			issues = append(issues, BundleIssue{File: name, Err: fmt.Errorf("%w %q, already provided by %s", ErrDuplicateID, def.ID, first)})
			continue
		}
		byID[def.ID] = name
		out = append(out, BundledDefinition{File: name, YAML: data, Definition: def})
	}
	return out, issues, nil
}

// readCapped reads name from fsys, refusing more than MaxDefinitionBytes
// without buffering past it.
func readCapped(fsys fs.FS, name string) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, fmt.Errorf("cardigann: open %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(f, MaxDefinitionBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cardigann: read %s: %w", name, err)
	}
	if n > MaxDefinitionBytes {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrDefinitionTooLarge, MaxDefinitionBytes)
	}
	return buf.Bytes(), nil
}
