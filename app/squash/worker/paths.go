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

package worker

import (
	"fmt"
	"path/filepath"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// LogicalDataRoot is where every path stored in a CRD lives: RootFolder
// paths must start with /data/media/ (a CEL rule), and MediaFile and
// TranscodeJob paths are under a root folder. [Options.DataDir] is where
// that volume is mounted in THIS process -- /data in a Job pod, so the
// mapping is the identity in production.
const LogicalDataRoot = "/data"

// defaultRecycleBin is RecycleBin.Path's CRD default, used when a
// RootFolder's is somehow empty.
const defaultRecycleBin = "/data/.recycle"

// localPath maps a logical /data path to this process's filesystem. A path
// outside /data is refused rather than guessed at.
func localPath(dataDir, logical string) (string, error) {
	if !filepath.IsAbs(logical) {
		return "", fmt.Errorf("path %q is not absolute", logical)
	}
	clean := filepath.Clean(logical)
	if clean != LogicalDataRoot && !strings.HasPrefix(clean, LogicalDataRoot+"/") {
		return "", fmt.Errorf("path %q is outside %s", logical, LogicalDataRoot)
	}
	rel := strings.TrimPrefix(clean, LogicalDataRoot)
	return filepath.Join(dataDir, rel), nil
}

// within reports whether path is strictly inside dir (both logical, both
// already cleaned by the caller or the CRD).
func within(dir, path string) bool {
	dir = filepath.Clean(dir)
	return strings.HasPrefix(filepath.Clean(path), dir+"/")
}

// rootFolderFor picks the RootFolder whose path contains source, the
// deepest one if root folders nest. nil means none does.
func rootFolderFor(folders []catalogv1alpha1.RootFolder, source string) *catalogv1alpha1.RootFolder {
	var best *catalogv1alpha1.RootFolder
	for i := range folders {
		rf := &folders[i]
		if !within(rf.Spec.Path, source) {
			continue
		}
		if best == nil || len(filepath.Clean(rf.Spec.Path)) > len(filepath.Clean(best.Spec.Path)) {
			best = rf
		}
	}
	return best
}

// OutputPath is where a TranscodeJob's verified output finally lives: the
// output location gap-fix ruling R-11 asks for, from design spec §4.5
// (TranscodeJobSpec.OutputPath, "default <stem>.mkv beside source";
// PolicySpec.ReplaceSource) and §6.4 ("atomic rename over the source path
// (source -> recycle bin)"; "replaces the library hardlink only").
//
//   - spec.outputPath, when set, wins. It must be absolute, carry the
//     profile's container extension (a .mp4 name over mkv data is exactly
//     what ruling R8 refused), and -- with replaceSource=false -- must not
//     be the source itself, which would contradict keeping it.
//   - Otherwise, replaceSource=true: <stem>.<container> beside the source,
//     §4.5's default with the profile's container in place of the mkv
//     default. For a same-container profile that IS the source path and the
//     output replaces it in place; for a container change it is the new
//     name, and the source is retired to the recycle bin once the output is
//     in place.
//   - Otherwise, replaceSource=false: "<stem> - <profile>.<container>" beside
//     the source. §4.5's default would be the source itself for a
//     same-container profile, so a kept source needs another name; the
//     " - <label>" suffix is the multiple-version convention Jellyfin and
//     Plex both read (docs/research/naming.md §A3), so a media server shows
//     the transcode as a version of the same title rather than as a second
//     one.
//
// The extension comparison ignores case: Film.MKV under an mkv profile is
// replaced in place, not renamed. Both the TranscodeJob controller (which
// records the plan) and the worker (which writes the file) call this, so
// they agree on the path by construction.
func OutputPath(spec transcodev1alpha1.TranscodeJobSpec, profileName string,
	container transcodev1alpha1.Container, replaceSource bool,
) (string, error) {
	ext := strings.ToLower(string(container))
	if ext == "" {
		ext = string(transcodev1alpha1.ContainerMKV)
	}
	source := filepath.Clean(spec.SourcePath)

	if spec.OutputPath != nil && *spec.OutputPath != "" {
		out := filepath.Clean(*spec.OutputPath)
		if !filepath.IsAbs(out) {
			return "", fmt.Errorf("spec.outputPath %q is not absolute", *spec.OutputPath)
		}
		if got := strings.TrimPrefix(filepath.Ext(out), "."); !strings.EqualFold(got, ext) {
			return "", fmt.Errorf("spec.outputPath %q has extension %q, but profile %s writes %s", out, got, profileName, ext)
		}
		if !replaceSource && out == source {
			return "", fmt.Errorf("spec.outputPath is the source, but the profile's policy.replaceSource=false keeps the source")
		}
		return out, nil
	}

	srcExt := filepath.Ext(source)
	stem := strings.TrimSuffix(source, srcExt)
	if replaceSource {
		if strings.EqualFold(strings.TrimPrefix(srcExt, "."), ext) {
			return source, nil
		}
		return stem + "." + ext, nil
	}
	return stem + " - " + profileName + "." + ext, nil
}
