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

// Command pack-cardigann packs a directory of Cardigann definitions into the
// zip archive indexarr embeds (app/indexer/bundle/embedded/definitions.zip).
//
//	go run ./hack/pack-cardigann -src .data/Definitions \
//	    -out app/indexer/bundle/embedded/definitions.zip
//
// The archive is deterministic, so re-packing an unchanged directory produces
// a byte-identical file and a clean git diff: entries are the *.yml and
// *.yaml files at the root of -src (not recursive), in name order, flat (no
// directory entries), every one stamped with the same fixed time and mode, and
// deflated at the best compression level. A flat archive is what
// cardigann.LoadBundle reads: it lists the root of the fs.FS it is given,
// which here is the *zip.Reader itself.
package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// epoch is the fixed modification time of every entry. The zip format
// cannot store a time before 1980, so the Unix epoch is not an option.
var epoch = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)

func main() {
	src := flag.String("src", ".data/Definitions", "directory of Cardigann *.yml/*.yaml definitions")
	out := flag.String("out", "app/indexer/bundle/embedded/definitions.zip", "zip archive to write")
	flag.Parse()

	n, err := pack(*src, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pack-cardigann:", err)
		os.Exit(1)
	}
	fmt.Printf("pack-cardigann: %d definitions from %s -> %s\n", n, *src, *out)
}

// pack writes the archive to out (via a temporary file renamed into place) and
// returns the number of definitions it holds.
func pack(src, out string) (int, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", src, err)
	}
	var names []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yml", ".yaml":
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return 0, fmt.Errorf("%s holds no *.yml or *.yaml files", src)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestCompression)
	})
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			return 0, fmt.Errorf("read %s: %w", name, err)
		}
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: epoch}
		hdr.SetMode(0o644)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return 0, fmt.Errorf("add %s: %w", name, err)
		}
		if _, err := w.Write(data); err != nil {
			return 0, fmt.Errorf("write %s: %w", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return 0, fmt.Errorf("finish archive: %w", err)
	}

	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return 0, fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, out); err != nil {
		return 0, fmt.Errorf("rename %s: %w", tmp, err)
	}
	return len(names), nil
}
