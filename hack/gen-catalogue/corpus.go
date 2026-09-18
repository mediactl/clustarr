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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// loadCorpusIndex reads every *.json file under <corpusDir>/<app>/cf and
// indexes it by trash_id, so resolveFormat can look a format up by the id
// the manifest names without re-scanning the directory per lookup.
func loadCorpusIndex(corpusDir, app string) (map[string]corpusFormat, error) {
	dir := filepath.Join(corpusDir, app, "cf")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	index := make(map[string]corpusFormat, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		doc, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var cf corpusFormat
		if err := json.Unmarshal(doc, &cf); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		if cf.TrashID == "" {
			return nil, fmt.Errorf("%s: no trash_id", path)
		}
		index[cf.TrashID] = cf
	}
	return index, nil
}

// corpusIndexes is every app's corpus, indexed once and reused across every
// manifest format that references it.
type corpusIndexes map[string]map[string]corpusFormat

// loadCorpusIndexes indexes both apps' corpora under corpusDir.
func loadCorpusIndexes(corpusDir string) (corpusIndexes, error) {
	out := corpusIndexes{}
	for _, app := range []string{"radarr", "sonarr"} {
		idx, err := loadCorpusIndex(corpusDir, app)
		if err != nil {
			return nil, err
		}
		out[app] = idx
	}
	return out, nil
}

// find returns the corpus specification named name of the given
// implementation inside app's copy of trashID's format.
func (c corpusIndexes) findSpecification(app, trashID, implementation, name string) (corpusSpecification, corpusFormat, error) {
	appIdx, ok := c[app]
	if !ok {
		return corpusSpecification{}, corpusFormat{}, fmt.Errorf("no corpus loaded for app %q", app)
	}
	cf, ok := appIdx[trashID]
	if !ok {
		return corpusSpecification{}, corpusFormat{}, fmt.Errorf("%s: no corpus file has trash_id %s", app, trashID)
	}
	for _, s := range cf.Specifications {
		if s.Implementation == implementation && s.Name == name {
			return s, cf, nil
		}
	}
	return corpusSpecification{}, corpusFormat{}, fmt.Errorf("%s/%s: no %s specification named %q", app, trashID, implementation, name)
}
