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

// Package subarchive pulls one subtitle file out of the ZIP or RAR archive a
// subtitle provider serves (SubDL, SubSource), without trusting the
// archive: every member is read through a size cap, so a decompression bomb
// costs at most the cap, and the selection never guesses -- an episode
// pack in which no file names the wanted episode yields ErrNoSubtitle, not
// some other episode's subtitle scored as this one's.
//
// The selection is a port of Bazarr's
// custom_libs/subliminal_patch/providers/utils.py get_subtitle_from_archive
// and _get_matching_sub, with SubDL's _first_subtitle_in_archive junk
// filter (directory entries, __MACOSX/ and AppleDouble "._" forks), from
// morpheus65535/bazarr at ec41fe82c03ccd556d168666595bd0be1202f77b.
// guessit's episode guess is pkg/release.Parse.
package subarchive

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/nwaples/rardecode/v2"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

var (
	// ErrNotArchive is returned by Open for bytes that are neither a ZIP
	// nor a RAR archive -- typically a bare subtitle file.
	ErrNotArchive = errors.New("subarchive: not a zip or rar archive")
	// ErrNoSubtitle is returned when the archive holds no subtitle file the
	// selection accepts.
	ErrNoSubtitle = errors.New("subarchive: no matching subtitle in archive")
	// ErrTooLarge is returned when a member, or a RAR archive's subtitle
	// members together, exceed the caller's cap.
	ErrTooLarge = errors.New("subarchive: archive member exceeds size limit")
)

// maxMembers bounds how many entries of an archive are looked at. A real
// season pack is tens of files; the cap only stops a hostile archive's
// directory from being walked forever.
const maxMembers = 4096

// Archive is an opened archive's subtitle members.
type Archive interface {
	// Names lists the members that look like subtitle files, in archive
	// order, junk excluded.
	Names() []string
	// Read returns one member's bytes, read through the cap Open was given.
	Read(name string) ([]byte, error)
}

// Open sniffs raw as a ZIP, then as a RAR archive (SubDL serves some RARs
// under a .zip name, which Bazarr's _open_archive notes, so the name is not
// trusted). limit caps every member read. A RAR is read sequentially, so
// its subtitle members are decompressed up front; together they are held
// to four times limit.
func Open(raw []byte, limit int64) (Archive, error) {
	if zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw))); err == nil {
		return &zipArchive{r: zr, limit: limit}, nil
	}
	if bytes.HasPrefix(raw, []byte("Rar!\x1a\x07")) {
		return openRAR(raw, limit)
	}
	return nil, ErrNotArchive
}

// IsSubtitle reports whether an archive member name is a subtitle file
// worth considering: a .srt, .sub, .ssa or .ass that is not a directory, a
// macOS resource fork or anything under __MACOSX/.
func IsSubtitle(name string) bool {
	if name == "" || strings.HasSuffix(name, "/") || strings.HasPrefix(name, "__MACOSX/") {
		return false
	}
	base := path.Base(name)
	if base == "" || strings.HasPrefix(base, "._") {
		return false
	}
	switch strings.ToLower(path.Ext(base)) {
	case ".srt", ".sub", ".ssa", ".ass":
		return true
	}
	return false
}

// Pick chooses the member to use, following get_subtitle_from_archive:
//
//   - no subtitle members: ErrNoSubtitle;
//   - exactly one: that one, whatever it is named;
//   - otherwise, skipping names whose stem ends in "forced" unless forced
//     subtitles are wanted: for a movie (episode 0) the first; for an
//     episode the first whose name parses to that episode (and to season,
//     when both the name and the caller give one).
//
// An episode that no name confirms is ErrNoSubtitle. Bazarr's version also
// scores names by episode-title similarity, which needs the episode's
// title; that tier is not ported.
func Pick(names []string, season, episode int, forced bool) (string, error) {
	switch len(names) {
	case 0:
		return "", ErrNoSubtitle
	case 1:
		return names[0], nil
	}
	for _, name := range names {
		stem := strings.ToLower(strings.TrimSuffix(path.Base(name), path.Ext(name)))
		if !forced && strings.HasSuffix(stem, "forced") {
			continue
		}
		if episode == 0 {
			return name, nil
		}
		if s, e, ok := EpisodeOf(name); ok && e == episode && (s == 0 || season == 0 || s == season) {
			return name, nil
		}
	}
	return "", ErrNoSubtitle
}

// EpisodeOf is the single season and episode a file name spells, as
// guessit's single-value episode guess: the first episode of the name's
// parse. ok is false when the name spells no episode. season is 0 when the
// name spells none.
func EpisodeOf(name string) (season, episode int, ok bool) {
	base := path.Base(name)
	stem := strings.TrimSuffix(base, path.Ext(base))
	p, err := release.Parse(stem, release.Options{Kind: common.MediaKindEpisode})
	if err != nil || len(p.Episodes) == 0 {
		return 0, 0, false
	}
	if len(p.Seasons) > 0 {
		season = p.Seasons[0]
	}
	return season, p.Episodes[0], true
}

type zipArchive struct {
	r     *zip.Reader
	limit int64
}

func (a *zipArchive) Names() []string {
	var out []string
	for i, f := range a.r.File {
		if i >= maxMembers {
			break
		}
		if !f.FileInfo().IsDir() && IsSubtitle(f.Name) {
			out = append(out, f.Name)
		}
	}
	return out
}

func (a *zipArchive) Read(name string) ([]byte, error) {
	for _, f := range a.r.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("subarchive: open %s: %w", name, err)
		}
		defer func() { _ = rc.Close() }()
		return readCapped(rc, a.limit, name)
	}
	return nil, fmt.Errorf("%w: %s", ErrNoSubtitle, name)
}

// rarArchive holds a RAR archive's subtitle members, decompressed by
// openRAR: rardecode reads an archive front to back only.
type rarArchive struct {
	names []string
	data  map[string][]byte
}

func openRAR(raw []byte, limit int64) (*rarArchive, error) {
	rr, err := rardecode.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("subarchive: open rar: %w", err)
	}
	a := &rarArchive{data: map[string][]byte{}}
	var total int64
	for i := 0; i < maxMembers; i++ {
		h, err := rr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("subarchive: read rar: %w", err)
		}
		if h.IsDir || !IsSubtitle(h.Name) {
			continue
		}
		b, err := readCapped(rr, limit, h.Name)
		if err != nil {
			return nil, err
		}
		if total += int64(len(b)); total > 4*limit {
			return nil, fmt.Errorf("%w: rar subtitle members exceed %d bytes together", ErrTooLarge, 4*limit)
		}
		if _, dup := a.data[h.Name]; !dup {
			a.names = append(a.names, h.Name)
		}
		a.data[h.Name] = b
	}
	return a, nil
}

func (a *rarArchive) Names() []string { return append([]string(nil), a.names...) }

func (a *rarArchive) Read(name string) ([]byte, error) {
	b, ok := a.data[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoSubtitle, name)
	}
	return b, nil
}

// readCapped reads r through limit, one byte past it so an over-long member
// is detected rather than silently truncated.
func readCapped(r io.Reader, limit int64, name string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("subarchive: read %s: %w", name, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: %s is over %d bytes", ErrTooLarge, name, limit)
	}
	return b, nil
}
