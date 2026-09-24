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
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// metricKindEpisode is the bounded `kind` label on the import metrics.
const metricKindEpisode = "episode"

// episodePlan is everything an episode import needs, resolved from the
// target and its Series before any file is touched.
type episodePlan struct {
	namespace  string
	series     *catalogv1alpha1.Series
	rootFolder *catalogv1alpha1.RootFolder
	profile    quality.Profile
	engine     naming.Engine

	// folder is the series' absolute folder in the library.
	folder string
	// seasonFolder is spec.seasonFolder, defaulted.
	seasonFolder bool
	// originalLanguageName is the series' original language, in the
	// vocabulary pkg/release and the custom-format catalogue share.
	originalLanguageName string

	// episodes are every Episode of the series.
	episodes []EpisodeCandidate

	// named is the one episode the target names (an episode target, or a
	// series target keyed to one episode); nil for a pack.
	named *EpisodeCandidate

	// sceneName is the Download's release title when it names the one
	// episode file the download holds, else "": see runEpisodes.
	sceneName string
}

// importEpisodes is Handle's path for an episode or series target: a single
// episode's download, or a season pack whose spec.target names the Series
// and its episodes in keys.
//
// Each media file is attributed by the episode numbering its name carries
// (MatchEpisodes) to episodes of the target's series -- whatever the pack
// holds, as Sonarr imports every episode of the series a release contains
// -- and then cleared per episode exactly as a movie file is: the profile
// must allow its quality, an automatic grab never replaces a covered
// episode's transcoded file (transcoded.go), and unless the import is manual
// it must be an upgrade over each covered episode's current file. A file whose name
// settles no episode is a rejection, except under a manual import to one
// named episode, where a person has said which it is.
func (w *Worker) importEpisodes(
	ctx context.Context, m events.Message, dl *downloadv1alpha1.Download, target ImportTarget, manual bool,
) error {
	plan, err := w.resolveSeries(ctx, dl, target)
	if err != nil {
		if errors.Is(err, errBlocked) || w.finalAttempt(m) {
			return w.finishBlocked(ctx, dl, nil, nil, blockedMessage(err))
		}
		return err
	}
	if dl.Status.ContentRoot == "" {
		return fmt.Errorf("fileimport: download %s/%s has no status.contentRoot yet", dl.Namespace, dl.Name)
	}
	if _, err := statDir(dl.Status.ContentRoot); err != nil {
		if w.finalAttempt(m) {
			return w.finishBlocked(ctx, dl, nil, nil,
				fmt.Sprintf("content root %q is not accessible: %v", dl.Status.ContentRoot, err))
		}
		return fmt.Errorf("fileimport: stat content root %s: %w", dl.Status.ContentRoot, err)
	}

	outcome, walkErr := w.runEpisodes(ctx, m, dl, plan, manual)
	if walkErr != nil {
		if w.finalAttempt(m) {
			return w.finishBlocked(ctx, dl, outcome.imported, outcome.rejections, walkErr.Error())
		}
		return fmt.Errorf("fileimport: import %s/%s: %w", dl.Namespace, dl.Name, walkErr)
	}
	if len(outcome.imported) == 0 {
		msg := "no episode files found"
		if len(outcome.rejections) > 0 {
			msg = downloadv1alpha1.ImportMessageEveryFileRejected
		}
		return w.finishBlocked(ctx, dl, outcome.imported, outcome.rejections, msg)
	}
	return w.finishImported(ctx, dl, outcome.imported, outcome.rejections)
}

// resolveSeries reads the target's Series, its root folder, profile and
// episodes. Errors wrapping errBlocked are terminal for this Download.
func (w *Worker) resolveSeries(ctx context.Context, dl *downloadv1alpha1.Download, target ImportTarget) (episodePlan, error) {
	ns := dl.Namespace
	plan := episodePlan{namespace: ns}

	seriesName := target.Name
	namedEpisode := ""
	switch ref := target.FileRef(); {
	case target.Kind == commonv1.MediaKindEpisode:
		var ep catalogv1alpha1.Episode
		if err := w.getItem(ctx, ns, "episode", target.Name, &ep); err != nil {
			return plan, err
		}
		seriesName, namedEpisode = ep.Spec.SeriesRef, ep.Name
	case ref.Kind == commonv1.MediaKindEpisode:
		namedEpisode = ref.Name
	}

	var series catalogv1alpha1.Series
	if err := w.getItem(ctx, ns, "series", seriesName, &series); err != nil {
		return plan, err
	}
	if series.Status.Metadata == nil || series.Status.Metadata.Title == "" {
		return plan, fmt.Errorf("fileimport: series %s/%s has no metadata yet", ns, series.Name)
	}
	plan.series = &series

	root, err := w.getRoot(ctx, ns, series.Spec.RootFolderRef)
	if err != nil {
		return plan, err
	}
	if !FileRefFitsRoot(commonv1.MediaRef{Kind: commonv1.MediaKindEpisode}, root.Spec.Kind) {
		return plan, blocked("root folder %q is a %s root; an episode file does not belong there", root.Name, root.Spec.Kind)
	}
	plan.rootFolder = root

	profile, err := w.resolveProfile(ctx, dl.Spec.QualityProfileRef, series.Spec.QualityProfileRef)
	if err != nil {
		return plan, err
	}
	plan.profile = profile

	plan.engine = engineFor(root)
	plan.folder, err = SeriesFolder(root, &series, plan.engine)
	if err != nil {
		return plan, blocked("render series folder: %v", err)
	}
	plan.seasonFolder = ptr.Deref(series.Spec.SeasonFolder, true)
	if lang := series.Status.Metadata.OriginalLanguage; lang != "" {
		if n, ok := catalogue.LanguageName(lang); ok {
			plan.originalLanguageName = n
		}
	}

	var episodes catalogv1alpha1.EpisodeList
	if err := w.Client.List(ctx, &episodes, client.InNamespace(ns)); err != nil {
		return plan, fmt.Errorf("fileimport: list episodes in %s: %w", ns, err)
	}
	for i := range episodes.Items {
		if ep := &episodes.Items[i]; ep.Spec.SeriesRef == series.Name {
			plan.episodes = append(plan.episodes, EpisodeCandidateFor(ep))
		}
	}
	if namedEpisode != "" {
		for i := range plan.episodes {
			if plan.episodes[i].Name == namedEpisode {
				plan.named = &plan.episodes[i]
			}
		}
		if plan.named == nil {
			return plan, blocked("episode %q is not an episode of series %q", namedEpisode, series.Name)
		}
	}
	return plan, nil
}

// SeriesFolder is a Series' absolute folder in the library: status.path once
// the Series controller has resolved it, else the same rule that controller
// resolves it by (catalogarr/controller/series.Path) -- spec.folder under the
// root folder, else the dialect's series preset.
func SeriesFolder(root *catalogv1alpha1.RootFolder, s *catalogv1alpha1.Series, eng naming.Engine) (string, error) {
	if s.Status.Path != "" {
		return s.Status.Path, nil
	}
	if f := ptr.Deref(s.Spec.Folder, ""); f != "" {
		return path.Join(root.Spec.Path, f), nil
	}
	nctx := naming.Context{Kind: commonv1.MediaKindSeries, TvdbID: strconv.FormatInt(s.Spec.TvdbID, 10)}
	if md := s.Status.Metadata; md != nil {
		nctx.SeriesTitle, nctx.SeriesYear = md.Title, int(md.Year)
	}
	folder, err := eng.SeriesFolder(nctx)
	if err != nil {
		return "", err
	}
	return path.Join(root.Spec.Path, folder), nil
}

// runEpisodes walks the content root and imports every episode file.
func (w *Worker) runEpisodes(
	ctx context.Context, m events.Message, dl *downloadv1alpha1.Download, plan episodePlan, manual bool,
) (importOutcome, error) {
	var (
		out           importOutcome
		lastHeartbeat time.Time
		dests         = map[string]string{}
		replaced      = map[string]bool{}
	)
	root := dl.Status.ContentRoot
	classifier := ClassifierFor(commonv1.MediaKindEpisode, root, w.SampleMaxBytes)
	videos, err := countMedia(ctx, classifier, root, 2)
	if err != nil {
		return out, err
	}
	besideMedia := manual && videos > 0
	// The release title is the scene name of an episode file only when it
	// is not a full season's and the download holds no other video file --
	// Sonarr's SceneNameCalculator.GetSceneName: a pack's name says nothing
	// about which of its episodes one file is, nor what that file is.
	if videos == 1 {
		if p, perr := release.Parse(dl.Spec.Release.Title, release.Options{Kind: commonv1.MediaKindEpisode}); perr == nil && !p.FullSeason {
			plan.sceneName = dl.Spec.Release.Title
		}
	}
	var cands []fileCandidate
	err = classifier.Walk(ctx, root, func(srcPath string, info os.FileInfo, class fsops.FileClass) error {
		if err := w.beat(ctx, m, &lastHeartbeat); err != nil {
			return err
		}
		if rejection, candidate := w.admit(root, srcPath, info, class, manual, besideMedia); !candidate {
			if rejection != "" {
				out.rejections = append(out.rejections, rejection)
			}
			return nil
		}
		c := fileCandidate{path: srcPath, info: info}
		if p, err := release.ParsePath(srcPath, release.Options{Kind: commonv1.MediaKindEpisode}); err == nil {
			c.ranked(plan.profile, p.Quality, p.Revision, true)
		}
		cands = append(cands, c)
		return nil
	}, out.unreadable(root))
	if err != nil {
		return out, err
	}

	// Each episode holds one file: candidates are imported best first, and
	// a file whose episodes this import already filled is rejected
	// (order.go), as Sonarr's ImportApprovedEpisodes does.
	sortCandidates(cands)
	filled := map[string]string{}
	for _, c := range cands {
		if err := w.beat(ctx, m, &lastHeartbeat); err != nil {
			return out, err
		}
		imported, rejection, err := w.importEpisodeFile(ctx, dl, plan, manual, c.path, c.info, dests, replaced, filled)
		if err != nil {
			return out, err
		}
		if rejection != "" {
			out.rejections = append(out.rejections, rejection)
			continue
		}
		out.imported = append(out.imported, imported)
	}
	return out, nil
}

// importEpisodeFile imports one episode file, or says why it was rejected.
// A non-nil error aborts the walk, exactly as in processConfig.processFile.
// replaced records the MediaFiles this import has already recycled, so a
// multi-episode file that an import replaces with two single-episode files
// is recycled once.
func (w *Worker) importEpisodeFile(
	ctx context.Context, dl *downloadv1alpha1.Download, plan episodePlan, manual bool,
	srcPath string, info os.FileInfo, dests map[string]string, replaced map[string]bool, filled map[string]string,
) (*downloadac.ImportedFileApplyConfiguration, string, error) {
	log := logging.FromContext(ctx)
	rel := relPath(dl.Status.ContentRoot, srcPath)
	series := plan.series

	parsed, perr := release.ParsePath(srcPath, release.Options{Kind: commonv1.MediaKindEpisode})
	if perr != nil {
		return nil, fmt.Sprintf("%s: could not parse the filename: %v", rel, perr), nil
	}
	eps, reason := MatchEpisodes(parsed, series.Spec.SeriesType, plan.episodes)
	if reason != "" {
		if !manual || plan.named == nil || len(parsed.Episodes)+len(parsed.Absolute) > 0 || parsed.AirDate != nil {
			return nil, fmt.Sprintf("%s: not attributable to an episode of series %s: %s", rel, series.Name, reason), nil
		}
		// A manual import to one named episode, of a file whose name
		// carries no numbering at all: the person said which it is.
		eps = []EpisodeCandidate{*plan.named}
	}

	parsed.Languages = parsed.LanguagesFor(plan.originalLanguageName)
	if !plan.profile.Allowed(parsed.Quality) {
		return nil, notAllowedRejection(rel, parsed.Quality), nil
	}
	for _, e := range eps {
		if by, ok := filled[e.Name]; ok {
			return nil, filledRejection(rel, "episode "+e.Name, by), nil
		}
	}
	// ReleaseTitle custom formats read the scene name when the download
	// has one for this file (runEpisodes), else the file name, and the file
	// name as Filename: Sonarr's LocalEpisode input.
	releaseTitle := plan.sceneName
	if releaseTitle == "" {
		releaseTitle = filepath.Base(srcPath)
	}
	ic := catalogue.ItemContext{
		OriginalLanguageName: plan.originalLanguageName, ReleaseType: parsed.ReleaseType,
		ReleaseTitle: releaseTitle, Filename: filepath.Base(srcPath),
	}
	score, matched := plan.profile.Score(ctx, w.Catalogue, parsed, ic)

	existing, err := w.existingForEpisodes(ctx, plan.namespace, eps)
	if err != nil {
		return nil, "", err
	}
	// A transcoded file is final: only a person's choice replaces it
	// (transcoded.go), whichever of the covered episodes it backs.
	for i := range existing {
		if r := transcodedRejection(rel, &existing[i], dl, manual); r != "" {
			return nil, r, nil
		}
	}
	if !manual {
		candidate := quality.Candidate{Quality: parsed.Quality, Revision: parsed.Revision, FormatScore: score}
		for _, mf := range existing {
			current := quality.Candidate{Quality: mf.Spec.Quality, Revision: mf.Spec.Revision, FormatScore: int(mf.Spec.FormatScore)}
			if verdict := plan.profile.UpgradeDecision(current, candidate); verdict != quality.Upgrade {
				return nil, fmt.Sprintf("%s: %s (%s)", rel, verdictMessage(verdict), mf.Spec.MediaRef.Name), nil
			}
		}
	}

	dest, derr := episodeDestination(plan, eps, parsed, matched, srcPath)
	if derr != nil {
		return nil, fmt.Sprintf("%s: could not render a destination path: %v", rel, derr), nil
	}
	if other, dup := dests[dest]; dup {
		return nil, fmt.Sprintf("%s: resolves to the same library path as %s, which this import already placed", rel, other), nil
	}
	if err := fsops.EnsureFreeSpace(plan.rootFolder.Spec.Path, info.Size()+plan.rootFolder.Spec.MinFreeBytes); err != nil {
		return nil, "", fmt.Errorf("fileimport: %w", err)
	}
	mode := fsops.ImportHardlink
	if dl.Status.CanMoveFiles {
		mode = fsops.ImportMove
	}
	if err := placeFile(ctx, plan.rootFolder.Spec.RecycleBin.Path, srcPath, info, dest, mode); err != nil {
		if errors.Is(err, errWouldOverwrite) {
			return nil, fmt.Sprintf("%s: %v", rel, err), nil
		}
		return nil, "", err
	}
	dests[dest] = rel
	destInfo, err := os.Stat(dest)
	if err != nil {
		return nil, "", fmt.Errorf("fileimport: stat imported file %s: %w", dest, err)
	}

	ref := EpisodeFileRef(eps)
	spec := catalogac.MediaFileSpec().
		WithMediaRef(ref).
		WithPath(dest).
		WithSizeBytes(destInfo.Size()).
		WithModTime(metav1.NewTime(destInfo.ModTime())).
		WithQuality(parsed.Quality).
		WithRevision(parsed.Revision).
		WithReleaseType(parsed.ReleaseType).
		WithReleaseGroup(parsed.Group).
		WithEdition(parsed.Edition).
		WithFormatScore(int32(score)). //nolint:gosec // a custom-format score is a small bounded sum
		WithProfileHash(plan.profile.Hash).
		WithOriginal(true).
		WithImportedFrom(catalogac.ImportSource().
			WithDownloadRef(dl.Name).
			WithReleaseTitle(dl.Spec.Release.Title).
			WithIndexerName(dl.Spec.Release.IndexerName).
			WithProtocol(dl.Spec.Release.Protocol).
			WithImportedAt(metav1.NewTime(w.now())).
			WithManual(manual))
	if len(matched) > 0 {
		spec = spec.WithMatchedFormats(capMatchedFormats(matched)...)
	}
	if len(parsed.Languages) > 0 {
		spec = spec.WithLanguages(parsed.Languages...)
	}
	mfName := k8s.ChildName(ref.Name, "mediafile", dest)
	if err := w.applyMediaFile(ctx, mfName, spec, plan.namespace); err != nil {
		return nil, "", err
	}

	// The covered episodes' previous files are replaced, as a movie's is.
	// A previous multi-episode file goes whole, as Sonarr's upgrade does:
	// an episode it held that this file does not is missing again, and its
	// own search takes it from there.
	bin := fsops.RecycleBinPath(plan.rootFolder.Spec.RecycleBin.Path)
	for i := range existing {
		old := &existing[i]
		if old.Spec.Path == dest || old.Name == mfName || replaced[old.Name] {
			continue
		}
		replaced[old.Name] = true
		if _, err := fsops.Recycle(bin, old.Spec.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warn("fileimport: could not recycle the replaced file; leaving it in place", "path", old.Spec.Path, "error", err)
			continue
		}
		if err := w.Client.Delete(ctx, old); client.IgnoreNotFound(err) != nil {
			log.Warn("fileimport: could not delete the replaced media file object", "mediaFile", old.Name, "error", err)
		}
	}

	for _, e := range eps {
		filled[e.Name] = rel
	}
	metrics.ImportFilesTotal.WithLabelValues(metricKindEpisode, mode.String(), "imported").Inc()
	log.Info("fileimport: imported an episode file", "source", rel, "dest", dest, "mediaFile", mfName,
		"episodes", strings.Join(names(eps), ","), "formatScore", score)
	return downloadac.ImportedFile().WithSourcePath(rel).WithDestPath(dest).WithMediaFileRef(mfName), "", nil
}

// existingForEpisodes is every MediaFile backing any of eps, each once --
// the target index covers a multi-episode file's keys too.
func (w *Worker) existingForEpisodes(ctx context.Context, ns string, eps []EpisodeCandidate) ([]catalogv1alpha1.MediaFile, error) {
	seen := map[string]bool{}
	var out []catalogv1alpha1.MediaFile
	for _, e := range eps {
		files, err := w.existingMediaFiles(ctx, ns, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: e.Name})
		if err != nil {
			return nil, err
		}
		for _, mf := range files {
			if !seen[mf.Name] {
				seen[mf.Name] = true
				out = append(out, mf)
			}
		}
	}
	return out, nil
}

// episodeDestination renders an episode file's library path: the series
// folder, the season folder when the series keeps one, and pkg/naming's
// standard, anime or daily episode file name, with the source's extension.
func episodeDestination(plan episodePlan, eps []EpisodeCandidate, parsed *release.ParsedRelease, formats []string, srcPath string) (string, error) {
	md := plan.series.Status.Metadata
	nctx := naming.Context{
		Kind:          commonv1.MediaKindEpisode,
		SeriesTitle:   md.Title,
		SeriesYear:    int(md.Year),
		TvdbID:        strconv.FormatInt(plan.series.Spec.TvdbID, 10),
		Season:        eps[0].Season,
		EpisodeTitle:  eps[0].Title,
		Special:       eps[0].Season == 0,
		Quality:       parsed.Quality,
		Revision:      parsed.Revision,
		ReleaseGroup:  parsed.Group,
		Edition:       parsed.Edition,
		CustomFormats: formats,
	}
	absolute := plan.series.Spec.SeriesType == catalogv1alpha1.SeriesTypeAnime
	for _, e := range eps {
		nctx.Episodes = append(nctx.Episodes, e.Number)
		if e.Absolute == 0 {
			absolute = false
		}
	}
	if absolute {
		for _, e := range eps {
			nctx.Absolute = append(nctx.Absolute, e.Absolute)
		}
	}
	if plan.series.Spec.SeriesType == catalogv1alpha1.SeriesTypeDaily && eps[0].AirDate != "" {
		if t, err := time.Parse("2006-01-02", eps[0].AirDate); err == nil {
			nctx.AirDate = &t
		}
	}
	file, err := plan.engine.EpisodeFile(nctx)
	if err != nil {
		return "", err
	}
	folder := plan.folder
	if plan.seasonFolder {
		season, err := plan.engine.SeasonFolder(nctx)
		if err != nil {
			return "", err
		}
		folder = filepath.Join(folder, season)
	}
	full := filepath.Join(folder, file+filepath.Ext(srcPath))
	return naming.SanitizePath(full, naming.DefaultSanitizeOptions()), nil
}
