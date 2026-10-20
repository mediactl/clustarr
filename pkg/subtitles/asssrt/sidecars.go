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

package asssrt

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// Report is what ConvertSidecars did, or would do in a dry run.
type Report struct {
	// Converted is .ass sidecars written as .srt, Redundant those beside an
	// .srt of their name already, which were only moved away.
	Converted, Redundant int
	// Orphans is .ass files beside no video of their stem -- a replaced
	// release's leftovers -- which no player matches and which are left.
	Orphans int
	// Failed names each sidecar left as it was, and why.
	Failed []string
}

// ConvertSidecars replaces every .ass or .ssa sidecar of a video under root
// with the .srt of the same name, Convert's plain SubRip, and moves the
// original under backup at its path relative to root, so the replacement can
// be undone until backup is cleared. An .srt already there is never
// overwritten: its .ass is moved away as redundant. A script Convert cannot
// turn into cues is left. Directories named .clustarr, and backup itself, are
// not walked. dryRun reports and changes nothing.
func ConvertSidecars(root, backup string, dryRun bool) (Report, error) {
	var rep Report
	root, backup = filepath.Clean(root), filepath.Clean(backup)
	err := filepath.WalkDir(root, func(dir string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if dir != root && (d.Name() == ".clustarr" || dir == backup) {
			return filepath.SkipDir
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		var stems, scripts []string
		for _, e := range entries {
			if !e.Type().IsRegular() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			switch {
			case fsops.MediaExtensions[fsops.KindVideo][ext]:
				stems = append(stems, strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
			case ext == ".ass" || ext == ".ssa":
				scripts = append(scripts, e.Name())
			}
		}
		for _, name := range scripts {
			if !sidecarOfAny(stems, name) {
				rep.Orphans++
				continue
			}
			if err := replace(root, backup, dir, name, dryRun, &rep); err != nil {
				rep.Failed = append(rep.Failed, fmt.Sprintf("%s: %v", filepath.Join(dir, name), err))
			}
		}
		return nil
	})
	return rep, err
}

func sidecarOfAny(stems []string, name string) bool {
	for _, stem := range stems {
		if _, ok := subtitles.ParseSidecar(stem, name); ok {
			return true
		}
	}
	return false
}

// replace converts dir/name to its .srt, unless one is there, and moves the
// original to backup.
func replace(root, backup, dir, name string, dryRun bool, rep *Report) error {
	src := filepath.Join(dir, name)
	dst := strings.TrimSuffix(src, filepath.Ext(src)) + ".srt"
	_, err := os.Stat(dst)
	switch {
	case err == nil:
		if !dryRun {
			if err := moveToBackup(root, backup, src); err != nil {
				return err
			}
		}
		rep.Redundant++
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	script, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	out, err := Convert(script)
	if err != nil {
		return err
	}
	if !dryRun {
		if err := fsops.AtomicWrite(dst, strings.NewReader(string(out)), info.Mode().Perm()); err != nil {
			return err
		}
		if err := moveToBackup(root, backup, src); err != nil {
			return err
		}
	}
	rep.Converted++
	return nil
}

func moveToBackup(root, backup, src string) error {
	rel, err := filepath.Rel(root, src)
	if err != nil {
		return err
	}
	dst := filepath.Join(backup, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return fsops.MoveAtomic(src, dst)
}
