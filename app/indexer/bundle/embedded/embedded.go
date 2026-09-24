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

// Package embedded is the Cardigann definition corpus compiled into the
// binary: a deflated zip of every definition, served as an fs.FS.
//
// The archive is definitions.zip beside this file, built by
// hack/pack-cardigann from the project's definitions directory
// (`make cardigann-bundle`). It is stored compressed -- 752 definitions are
// 6.2 MB as YAML and 1.5 MB deflated -- and never unpacked to disk:
// [FS] opens it with archive/zip, whose *zip.Reader implements fs.FS and
// inflates an entry only when it is read. That is exactly the shape
// cardigann.LoadBundle consumes (the *.yml files at the root of an fs.FS),
// so indexarr loads the embedded corpus through the same path as an
// operator-mounted --cardigann-definitions-dir.
//
// Provenance: the definitions are Prowlarr's Cardigann definitions, added to
// the project by its owner on 2026-09-23 from a Prowlarr Definitions
// directory. See hack/sync-cardigann's package doc for the upstream licensing
// record.
package embedded

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"fmt"
	"io/fs"
)

//go:embed definitions.zip
var archive []byte

// FS returns the embedded definitions as a read-only fs.FS whose root holds
// one *.yml file per definition. Each call opens a fresh reader over the
// embedded bytes; nothing is inflated until a file is read.
func FS() (fs.FS, error) {
	r, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("embedded: open the Cardigann definition archive: %w", err)
	}
	return r, nil
}
