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

package fileimport

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/release"
)

// minEpisodeTitle is the shortest episode title, cleaned
// (release.CleanTitle), a file name is searched for: a shorter one ("Run",
// "Home") sits inside other words.
const minEpisodeTitle = 6

// placeholderTitleRegex is an episode title that names nothing: what a
// metadata provider puts where it has none.
var placeholderTitleRegex = regexp.MustCompile(`^(episode|ep|chapter|part)?\s*\d*$|^tba$|^tbd$`)

// packLeadGroupsRegex are the bracketed groups a pack's file name may open
// with, which are no part of what its files share or differ in.
var packLeadGroupsRegex = regexp.MustCompile(`^(?:\[[^\]]*\][\s._-]*)+`)

// packNumberRegex is the number a pack's file carries where its name parts
// from the others: one to three digits, a version after them or not, and
// no fourth digit (a year, a resolution).
var packNumberRegex = regexp.MustCompile(`^(\d{1,3})(?:v\d+)?(?:\D|$)`)

// packEpisodes reads the files of a season pack that no pattern reads from
// what is known of their download: that it is of one season of one series,
// and that season's episodes by number and by title. names are those files'
// names, without directory or extension; the answer is each one's episode
// number in season, for the names it can say, and nil when what it reads
// contradicts itself.
//
// The owner, 2026-11-27, of a twenty-four-file Gosick pack held for a
// person ("Gosick 01 The Dark Reaper Finds the Golden Fairy.mkv" ...):
// "Since we have the episode names and numbers, we should be able to build
// each filename ... We know the season when downloading a season pack. The
// import/rename should build the target name intelligently and not just
// verify that it can be perfectly parsed as is."
//
// Two readings, neither a release pattern's:
//
//   - the number where the names part. A pack's files are named alike but
//     for what tells them apart: after any leading group, they share a
//     prefix, and the number that follows it is the episode's. It is taken
//     only when every name has one, no two share one, and each is an
//     episode of the season by its number in it -- or each by its absolute
//     number, for a pack counted through the series. So "Gsk - 01 [BD]",
//     "Gsk - 02 [BD]" are read with no title to go by, and a number an
//     episode's own title holds ("... 24 Hours") is never the one read.
//   - the episode's title. A name that holds the title of one episode of
//     the season, cleaned as release.CleanTitle and at least
//     minEpisodeTitle long, is that episode's; of several it holds, the
//     longest, and none when two as long are different episodes.
//
// Where both read a name they must agree, and no two names may be one
// episode: on either contradiction nothing is read, for any name, and the
// files stay a person's to place. A hashed name (hashedNameRegex) is no
// name and is never read here.
func packEpisodes(names []string, episodes []EpisodeCandidate, season int) map[string]int {
	if season < 1 || len(names) == 0 {
		return nil
	}
	var inSeason []EpisodeCandidate
	for _, e := range episodes {
		if e.Season == season {
			inSeason = append(inSeason, e)
		}
	}
	if len(inSeason) == 0 {
		return nil
	}
	var readable []string
	for _, n := range names {
		if !hashedNameRegex.MatchString(n) {
			readable = append(readable, n)
		}
	}
	byNumber := packSequence(readable, inSeason)
	out := map[string]int{}
	taken := map[int]string{}
	for _, n := range readable {
		number, numbered := byNumber[n]
		titled, hasTitle := titledBy(n, inSeason)
		switch {
		case numbered && hasTitle && number != titled:
			return nil
		case hasTitle:
			number = titled
		case !numbered:
			continue
		}
		if other, dup := taken[number]; dup && other != n {
			return nil
		}
		taken[number] = n
		out[n] = number
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// packSequence is packEpisodes' first reading: the number that follows the
// names' common prefix, as each one's episode number in the season. Nil
// unless there are two names or more and every one carries such a number.
func packSequence(names []string, inSeason []EpisodeCandidate) map[string]int {
	if len(names) < 2 {
		return nil
	}
	stripped := make([]string, len(names))
	for i, n := range names {
		stripped[i] = n[len(packLeadGroupsRegex.FindString(n)):]
	}
	prefix := stripped[0]
	for _, s := range stripped[1:] {
		for !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	// The names part inside the number where they share its first digits
	// ("0" of 01 to 09), and never inside a rune.
	prefix = strings.TrimRight(prefix, "0123456789")
	for len(prefix) > 0 && !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	numbers := make([]int, len(names))
	seen := map[int]bool{}
	for i, s := range stripped {
		m := packNumberRegex.FindStringSubmatch(s[len(prefix):])
		if m == nil {
			return nil
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 || seen[n] {
			return nil
		}
		seen[n] = true
		numbers[i] = n
	}
	own, absolute := map[int]int{}, map[int]int{}
	for _, e := range inSeason {
		own[e.Number] = e.Number
		if e.Absolute > 0 {
			absolute[e.Absolute] = e.Number
		}
	}
	for _, by := range []map[int]int{own, absolute} {
		out := make(map[string]int, len(names))
		for i, n := range numbers {
			number, ok := by[n]
			if !ok {
				out = nil
				break
			}
			out[names[i]] = number
		}
		if out != nil {
			return out
		}
	}
	return nil
}

// titledBy is packEpisodes' second reading: the episode of inSeason whose
// title name holds, the longest of those it holds; false when it holds
// none, or two as long that are different episodes.
func titledBy(name string, inSeason []EpisodeCandidate) (int, bool) {
	clean := release.CleanTitle(name)
	best, number, tied := 0, 0, false
	for _, e := range inSeason {
		t := release.CleanTitle(e.Title)
		if len(t) < minEpisodeTitle || placeholderTitleRegex.MatchString(t) || !strings.Contains(clean, t) {
			continue
		}
		switch {
		case len(t) > best:
			best, number, tied = len(t), e.Number, false
		case len(t) == best && e.Number != number:
			tied = true
		}
	}
	return number, best > 0 && !tied
}

// packEpisode is a pack reading's answer as the parse of the file it
// names: the episode, in the download's season, and nothing else -- the
// quality and the group are the release title's to say (fillFromRelease)
// and the languages the probe's.
func packEpisode(season, number int) *release.ParsedRelease {
	return &release.ParsedRelease{
		Seasons: []int{season}, Episodes: []int{number},
		LanguageUnknown: true,
		ReleaseType:     commonv1.ReleaseTypeSingle,
	}
}

// unreadPackFiles lists the media files under root that neither a release
// pattern nor bareEpisode nor titledEpisode reads, by path, for
// packEpisodes: a classification-only walk, as countMedia's. An unreadable
// entry is left to the import's own walk to report.
func unreadPackFiles(ctx context.Context, c fsops.Classifier, root, sceneName string, titles []string, season int) ([]string, error) {
	var out []string
	err := c.Walk(ctx, root, func(path string, _ os.FileInfo, class fsops.FileClass) error {
		if class != fsops.ClassMedia {
			return nil
		}
		if _, err := parseMediaFile(path, sceneName, commonv1.MediaKindEpisode); err == nil {
			return nil
		}
		if bareEpisode(path, season) != nil || titledEpisode(path, titles, season) != nil {
			return nil
		}
		out = append(out, path)
		return nil
	}, func(string, error) error { return nil })
	return out, err
}

// baseName is a path's file name without its extension.
func baseName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}
