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

// Package donoraudio cuts an audio donor down to the tracks a graft needs
// (anime dual-audio spec §7.2; loop spec §4.12): the import agent reduces a
// donor at import, and the graft run reduces a donor imported before it
// did. It loads FFmpeg through pkg/transcode/engine, so only the agent's
// import domain and cmd/transcode link it, never the manager.
package donoraudio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
)

// ErrLacksLanguage is a donor without the anchor or a required language:
// the release's fault, never retried for this donor.
var ErrLacksLanguage = errors.New("the donor lacks a language")

// ReducedPath is where a donor's audio lives once reduced: the donor
// itself when it is Matroska audio, else its stem as .mka.
func ReducedPath(donor string) string {
	if strings.EqualFold(filepath.Ext(donor), ".mka") {
		return donor
	}
	return strings.TrimSuffix(donor, filepath.Ext(donor)) + ".mka"
}

// Pick is the first track in language l (BCP-47 base compared); with
// untagged, a lone track with no language counts (an untagged track is the
// original, as status.audio reads it). -1 when none.
func Pick(tracks []engine.Track, l string, untagged bool) int {
	want, ok := lang.Normalize(l)
	if !ok {
		return -1
	}
	base := func(t lang.Tag) string { b, _, _ := strings.Cut(string(t), "-"); return strings.ToLower(b) }
	for _, tr := range tracks {
		if got, ok := lang.Normalize(tr.Language); ok && base(got) == base(want) {
			return tr.Index
		}
	}
	if untagged && len(tracks) == 1 {
		if _, ok := lang.Normalize(tracks[0].Language); !ok {
			return tracks[0].Index
		}
	}
	return -1
}

// Describe names each track's language ("untagged" for none), for a
// message.
func Describe(tracks []engine.Track) string {
	var s []string
	for _, t := range tracks {
		l := t.Language
		if l == "" {
			l = "untagged"
		}
		s = append(s, l)
	}
	if len(s) == 0 {
		return "no audio"
	}
	return strings.Join(s, ", ")
}

// Keep is the audio a reduction keeps: the anchor's track and each
// required language's, all of which must be there, then each optional
// language's the donor carries, every track once.
func Keep(tracks []engine.Track, anchor string, required, optional []string) ([]int, error) {
	var keep []int
	add := func(i int) {
		if i >= 0 && !slices.Contains(keep, i) {
			keep = append(keep, i)
		}
	}
	for _, l := range append([]string{anchor}, required...) {
		i := Pick(tracks, l, false)
		if i < 0 {
			return nil, fmt.Errorf("%w: the donor needs tagged %s and %s tracks; it has %s",
				ErrLacksLanguage, anchor, strings.Join(required, ", "), Describe(tracks))
		}
		add(i)
	}
	for _, l := range optional {
		add(Pick(tracks, l, false))
	}
	return keep, nil
}

// Reduce cuts src, a local path, to the audio Keep picks, written as
// ReducedPath(src) through a part file, and removes src. A Matroska audio
// src is returned unopened, and so is the .mka an earlier run left when src
// is gone.
func Reduce(ctx context.Context, src, anchor string, required, optional []string) (string, error) {
	if strings.EqualFold(filepath.Ext(src), ".mka") {
		return src, nil
	}
	mka := ReducedPath(src)
	if exists(mka) && !exists(src) {
		return mka, nil
	}
	tracks, err := engine.AudioTracks(src)
	if err != nil {
		return "", fmt.Errorf("read the donor's tracks: %w", err)
	}
	keep, err := Keep(tracks, anchor, required, optional)
	if err != nil {
		return "", err
	}
	part := strings.TrimSuffix(src, filepath.Ext(src)) + ".part.mka"
	if err := engine.ExtractAudio(ctx, src, part, keep); err != nil {
		_ = os.Remove(part)
		return "", fmt.Errorf("extract the donor's audio: %w", err)
	}
	if err := fsops.MoveAtomic(part, mka); err != nil {
		_ = os.Remove(part)
		return "", fmt.Errorf("place the donor's audio: %w", err)
	}
	if err := os.Remove(src); err != nil && !errors.Is(err, os.ErrNotExist) {
		logging.FromContext(ctx).Info("donoraudio: the donor's source stays", "path", src, "error", err)
	}
	return mka, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
