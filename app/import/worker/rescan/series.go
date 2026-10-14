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
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/names"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/release"
)

// Series attribution: a series root folder.
//
// A folder that carries a TheTVDB id no series has ("Breaking Bad (2008)
// [tvdbid-81189]") proposes that series, which the manager's scan applier
// creates, as a {tmdb-N} folder proposes its Movie (amendment §A1.5): the
// id is an embedded identifier, not a guess. A folder without one never
// proposes anything. A new series has no
// episodes until the Series controller fans them out from its metadata --
// not something a walk can wait on -- so its files are counted in
// Progress.AwaitingEpisodes, as are those of any series whose episodes have
// not arrived, and the scan of the series' folder its first episodes ask
// for attributes them (app/catalog/controller/folderscan).
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
// absolute path, rel its path relative to the root folder. A matched
// candidate with an empty Name is a series to create: its folder carries a
// TheTVDB id no candidate has, as MatchResult's empty ExistingName asks for
// a Movie.
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
			title, year := splitTitleYear(tvdbFolderRE.ReplaceAllString(folder, ""))
			return &SeriesCandidate{TvdbID: tvdb, Folder: folder, Title: title, Year: year}, ItemMatch{}
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
// no MediaFile records yet, to the episode (or episodes) of the series it
// holds -- proposing that series first when its folder's TheTVDB id names
// none -- and reports it, scored against the series' QualityProfile like
// any scanned video file.
func (w *Worker) handleEpisodeFile(ctx context.Context, st *scanState, path, rel string, info os.FileInfo) error {
	now := w.now()
	series, m := MatchSeries(path, rel, st.series)
	if m.Unmatched {
		st.unmatched(rel, m.Code, m.Reason, m.Candidates, now)
		return nil
	}
	if series.Name == "" {
		created, err := w.createSeries(ctx, st, path, info, series)
		if err != nil || created == nil {
			return err
		}
		series = created
	}
	if len(series.Episodes) == 0 {
		st.progress.AwaitingEpisodes++
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
	if st.task.DryRun {
		st.predict(false)
		return nil
	}
	fresh := w.freshVideoSpec(ctx, st, path, parsed, series.QualityProfileRef, series.OriginalLanguage)
	return w.proposeFile(ctx, st, path, info, nil, fileimport.EpisodeFileRef(eps), &fresh, nil, "series:"+series.Name)
}

// createSeries proposes the series want describes -- MatchSeries' unnamed
// candidate for a folder's TheTVDB id -- with the file that found it, and
// adds it to the walk's candidates, so the folder's other files find it
// rather than proposing it again. The manager's scan applier creates it,
// pinned to that folder, or refuses: an ImportExclusion naming the id
// (P37), or a root folder with no default qualityProfileRef (P38), which
// Series.spec.qualityProfileRef requires. A new series has no episodes, so
// its files await them either way.
func (w *Worker) createSeries(
	ctx context.Context, st *scanState, path string, info os.FileInfo, want *SeriesCandidate,
) (*SeriesCandidate, error) {
	c := *want
	c.Name = names.Series(want.Title, want.TvdbID)
	c.QualityProfileRef = st.root.Spec.Defaults.QualityProfileRef
	st.series = append(st.series, c)
	if st.task.DryRun {
		st.progress.ItemsCreated++
		return &st.series[len(st.series)-1], nil
	}
	create := &schema.ItemCreate{
		Kind: string(commonv1.MediaKindSeries), Name: c.Name, TvdbID: c.TvdbID,
		QualityProfileRef: c.QualityProfileRef, RootFolderRef: st.root.Name,
		Title: c.Title, Year: int32(c.Year), //nolint:gosec // a release year
	}
	obs := schema.ScanObservation{
		Kind: schema.ScanObservationPresent, Path: path,
		SizeBytes: info.Size(), ModTime: info.ModTime().UTC(),
		Attribution: &schema.ScanAttribution{
			Kind: string(commonv1.MediaKindSeries), Name: c.Name, ProviderID: "tvdb:" + strconv.FormatInt(c.TvdbID, 10),
			Evidence: "folder", Create: create,
		},
	}
	if err := w.observe(ctx, st, obs); err != nil {
		return nil, err
	}
	logging.FromContext(ctx).Info("proposed a series from its folder's TheTVDB id",
		"series", c.Name, "tvdbID", c.TvdbID)
	return &st.series[len(st.series)-1], nil
}
