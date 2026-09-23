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

package rescan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/release"
)

// Series attribution: a series root folder.
//
// Like the non-video kinds, a file is attributed to an EXISTING Series and
// Episode only. Creating a Series needs its TheTVDB id, and although a
// folder can carry one ("Breaking Bad (2008) [tvdbid-81189]"), a Series
// created by a scan would then need its whole episode list fetched before
// any file could be attributed to an Episode of it -- the Series
// controller's fan-out, not something a walk can wait on. So the folder's
// id identifies an existing series and never creates one.
//
// The series is identified by the file's folder, in layers, each exact:
// the series' own resolved folder (status.path); a TheTVDB id in the first
// folder under the root; the series' spec.folder; its title and year from
// the folder name, by release.CleanTitle equality. The episode is then
// identified by the numbering the file's name carries, through the same
// fileimport.MatchEpisodes the completed-download import uses.

// SeriesCandidate is one existing Series under the root folder, with its
// episodes.
type SeriesCandidate struct {
	Name   string
	Title  string // status.metadata.title
	Year   int    // status.metadata.year
	TvdbID int64
	Path   string // status.path
	Folder string // spec.folder

	SeriesType        catalogv1alpha1.SeriesType
	QualityProfileRef string
	OriginalLanguage  string // status.metadata.originalLanguage, BCP-47

	Episodes []fileimport.EpisodeCandidate
}

// tvdbFolderRE is a TheTVDB id token in a folder name, in the spellings
// the naming dialects use: Plex's "{tvdb-81189}", Emby's "[tvdb-81189]",
// Jellyfin's "[tvdbid-81189]".
var tvdbFolderRE = regexp.MustCompile(`(?i)\s*[\[{]tvdb(?:id)?[-=](\d+)[\]}]`)

// MatchSeries identifies the Series a file under a series root folder
// belongs to, by the layers this file's doc describes. absPath is the file's
// absolute path, rel its path relative to the root folder.
func MatchSeries(absPath, rel string, cands []SeriesCandidate) (*SeriesCandidate, ItemMatch) {
	var inFolder []int
	for i := range cands {
		if underFolder(absPath, cands[i].Path) {
			inFolder = append(inFolder, i)
		}
	}
	if len(inFolder) == 1 {
		return &cands[inFolder[0]], matched(commonv1.MediaKindSeries, cands[inFolder[0]].Name)
	}

	segs := strings.Split(filepath.ToSlash(rel), "/")
	if len(segs) < 2 {
		return nil, unmatchedLayout("an episode file must sit in its series' folder under the root folder " +
			"(<series>/[Season NN/]<episode>)")
	}
	folder := segs[0]

	pick := func(hits []int, none string) (*SeriesCandidate, ItemMatch) {
		names := make([]string, 0, len(hits))
		for _, i := range hits {
			names = append(names, cands[i].Name)
		}
		m := decide(commonv1.MediaKindSeries, names, none)
		if m.Unmatched {
			return nil, m
		}
		return &cands[hits[0]], m
	}

	if id := tvdbFolderRE.FindStringSubmatch(folder); id != nil {
		tvdb, _ := strconv.ParseInt(id[1], 10, 64)
		var hits []int
		for i := range cands {
			if cands[i].TvdbID == tvdb {
				hits = append(hits, i)
			}
		}
		if len(hits) == 0 {
			return nil, ItemMatch{Unmatched: true, Code: CodeUnknownID, Reason: fmt.Sprintf(
				"the series folder carries TheTVDB id %d, which no existing series under this root folder has; "+
					"a scan does not create series (add it, and a later scan attributes its files)", tvdb)}
		}
		// The id is the identity: never fall back to the title when it
		// names nothing.
		return pick(hits, "")
	}

	var hits []int
	for i := range cands {
		if cands[i].Folder != "" && filepath.Base(cands[i].Folder) == folder {
			hits = append(hits, i)
		}
	}
	if len(hits) > 0 {
		return pick(hits, "")
	}
	title, year := splitTitleYear(folder)
	for i := range cands {
		if cleanEq(cands[i].Title, title) && yearOK(cands[i].Year, year) {
			hits = append(hits, i)
		}
	}
	return pick(hits, fmt.Sprintf("no existing series titled %q among %d series under this root folder", title, len(cands)))
}

// loadSeries lists the series stored under root and their episodes.
func (w *Worker) loadSeries(ctx context.Context, ns string, root *catalogv1alpha1.RootFolder) ([]SeriesCandidate, error) {
	in := client.InNamespace(ns)
	var series catalogv1alpha1.SeriesList
	if err := w.Client.List(ctx, &series, in); err != nil {
		return nil, fmt.Errorf("rescan: list series in %s: %w", ns, err)
	}
	var episodes catalogv1alpha1.EpisodeList
	if err := w.Client.List(ctx, &episodes, in); err != nil {
		return nil, fmt.Errorf("rescan: list episodes in %s: %w", ns, err)
	}
	bySeries := map[string][]fileimport.EpisodeCandidate{}
	for i := range episodes.Items {
		ep := &episodes.Items[i]
		bySeries[ep.Spec.SeriesRef] = append(bySeries[ep.Spec.SeriesRef], fileimport.EpisodeCandidateFor(ep))
	}
	var out []SeriesCandidate
	for i := range series.Items {
		s := &series.Items[i]
		if s.Spec.RootFolderRef != root.Name {
			continue
		}
		c := SeriesCandidate{
			Name: s.Name, TvdbID: s.Spec.TvdbID, Path: s.Status.Path, Folder: ptr.Deref(s.Spec.Folder, ""),
			SeriesType: s.Spec.SeriesType, QualityProfileRef: s.Spec.QualityProfileRef,
			Episodes: bySeries[s.Name],
		}
		if md := s.Status.Metadata; md != nil {
			c.Title, c.Year, c.OriginalLanguage = md.Title, int(md.Year), md.OriginalLanguage
		}
		out = append(out, c)
	}
	return out, nil
}

// handleEpisodeFile attributes one file under a series root folder, which
// no MediaFile records yet, to the episode (or episodes) of an existing
// series it holds, and records it, scored against the series'
// QualityProfile like any scanned video file.
func (w *Worker) handleEpisodeFile(ctx context.Context, st *scanState, path, rel string, info os.FileInfo) error {
	now := w.now()
	series, m := MatchSeries(path, rel, st.series)
	if m.Unmatched {
		st.unmatched(rel, m.Code, m.Reason, m.Candidates, now)
		return nil
	}
	parsed, perr := release.ParsePath(path, release.Options{Kind: commonv1.MediaKindEpisode})
	if perr != nil {
		st.unmatched(rel, CodeParseError, fmt.Sprintf("could not parse the filename: %v", perr), nil, now)
		return nil
	}
	eps, reason := fileimport.MatchEpisodes(parsed, series.SeriesType, series.Episodes)
	if reason != "" {
		st.unmatched(rel, CodeNoChild, fmt.Sprintf("in series %s: %s", series.Name, reason), nil, now)
		return nil
	}
	fresh := w.freshVideoSpec(ctx, st, parsed, series.QualityProfileRef, series.OriginalLanguage)
	return w.recordAttribution(ctx, st, path, rel, info, nil, fileimport.EpisodeFileRef(eps), fresh)
}
