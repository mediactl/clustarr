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

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	corpusDir := flag.String("corpus", "testdata/trash/docs/json", "vendored TRaSH corpus root (contains radarr/ and sonarr/)")
	manifestDir := flag.String("manifest", "hack/gen-catalogue/manifest", "curated selection manifest (which corpus formats/conditions to embed, and in what order)")
	outDir := flag.String("out", "pkg/quality/catalogue/data/formats", "output directory for the generated family files")
	flag.Parse()

	if err := run(*corpusDir, *manifestDir, *outDir); err != nil {
		fmt.Fprintln(os.Stderr, "gen-catalogue:", err)
		os.Exit(1)
	}
}

func run(corpusDir, manifestDir, outDir string) error {
	families, err := GenerateAll(corpusDir, manifestDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	for name, doc := range families {
		path := filepath.Join(outDir, name)
		if err := os.WriteFile(path, doc, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}
