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

package subtitleplan

import (
	"path/filepath"
	"slices"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// carriedSidecars is status.sidecars carried from the stored status when
// this pass did not list (§2.7): every entry with a name, sorted by name, at
// most 32. An entry with none (a 0.4.x install's, whose path the schema no
// longer has) is dropped until the next listing finds the file again.
func carriedSidecars(stored []catalogv1alpha1.Sidecar) []catalogv1alpha1.Sidecar {
	if len(stored) == 0 {
		return nil
	}
	out := make([]catalogv1alpha1.Sidecar, 0, len(stored))
	for _, s := range stored {
		if s.Name == "" {
			continue
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b catalogv1alpha1.Sidecar) int { return strings.Compare(a.Name, b.Name) })
	return out[:min(len(out), maxSidecars)]
}

// listedSidecars is status.sidecars from a listing (§6.4.3): one entry per
// name subtitles.ParseSidecar attributes to the video's stem, hand-placed
// ones and the MP4 standard's included, with its language, forced and hi
// from the key, sorted by name, at most 32. Not filtered by profile.
func listedSidecars(videoPath string, names []string) []catalogv1alpha1.Sidecar {
	base := filepath.Base(videoPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	var out []catalogv1alpha1.Sidecar
	for _, name := range names {
		raw, ok := subtitles.ParseSidecar(stem, name)
		if !ok {
			continue
		}
		key := raw
		if n, ok := normalizeKey(raw); ok {
			key = n
		}
		lang, forced, hi, err := subtitles.ParseLangKey(key)
		if err != nil {
			continue
		}
		out = append(out, catalogv1alpha1.Sidecar{Name: name, Language: lang, Forced: forced, HI: hi})
	}
	slices.SortFunc(out, func(a, b catalogv1alpha1.Sidecar) int { return strings.Compare(a.Name, b.Name) })
	return out[:min(len(out), maxSidecars)]
}

// sidecarNames is the names of sidecars, the existing half a plan without a
// listing reads (§6.4.4).
func sidecarNames(sidecars []catalogv1alpha1.Sidecar) []string {
	out := make([]string, 0, len(sidecars))
	for _, s := range sidecars {
		out = append(out, s.Name)
	}
	return out
}

// rebind keeps a downloaded item's name true to the directory (§6.4.3): a
// sidecar the rename actuator moved is found under its canonical name or
// the .ass twin; one that is gone clears the candidate, so the language is
// wanted and searched again.
func rebind(it *catalogv1alpha1.SubtitleItemStatus, videoPath, hiExt string, names []string) {
	if it.Name == "" || slices.Contains(names, it.Name) {
		return
	}
	canon := filepath.Base(subtitles.SidecarName(videoPath, subtitles.LangKey(it.LangKey), hiExt))
	for _, n := range []string{canon, strings.TrimSuffix(canon, ".srt") + ".ass"} {
		if slices.Contains(names, n) {
			it.Name = n
			return
		}
	}
	gone := it.Name
	it.State = catalogv1alpha1.SubtitleStatePending
	it.Score, it.Provider, it.SubtitleID, it.Name, it.DownloadedAt = 0, "", "", "", nil
	it.LastError = clip("sidecar "+gone+" is gone", maxLastError)
}
