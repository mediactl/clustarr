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
	"path/filepath"
	"strings"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importtarget"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/release"
)

// handleInspect answers one ImportInspectTask (ADR-0019 §6.9, step 1): it
// reads the payload, says what each file is -- parsed, probed, classed,
// mapped to its target, its import fields frozen and its library path
// rendered -- with every rejection and its class, and writes the record at
// RecordSubKey(entry, "inspect"). It decides nothing and writes no
// Kubernetes object: the manager's importplan decides.
func (w *Worker) handleInspect(ctx context.Context, m events.Message, env *events.Envelope) error {
	var task schema.ImportInspectTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("fileimport: malformed ImportInspectTask", err)
	}
	if task.Entry.UID == "" || task.Owner.Namespace == "" {
		return events.Discard("fileimport: an inspect task names no entry or owner", nil)
	}
	key := agentrecords.ImportKey(task.Entry.UID, schema.ImportSubInspect)
	if cur, _, ok, err := w.reader().Get(ctx, key); err != nil {
		return fmt.Errorf("fileimport: read the inspect record: %w", err)
	} else if ok && cur.Seq >= task.Seq {
		return nil // superseded, or already answered
	}
	log := logging.FromContext(ctx).With("namespace", task.Owner.Namespace, "owner", task.Owner.Name, "entry", task.Entry.ID)
	ctx = logging.NewContext(ctx, log)

	insp, err := w.inspect(ctx, m, &task)
	if err != nil {
		return err
	}
	now := w.now().UTC()
	rec := &schema.ImportRecord{
		RecordHeader: schema.RecordHeader{
			Item: &task.Owner, Sub: schema.ImportSubInspect, Seq: task.Seq, State: records.StateAnswered, AnsweredAt: &now,
		},
		Entry: task.Entry, Inspect: insp,
	}
	_, err = w.writer().Write(ctx, key, rec)
	if errors.Is(err, records.ErrTooLarge) {
		rec.Inspect = &schema.ImportInspection{Rejections: []schema.InspectRejection{blockedRejection(
			"the payload's inspection is larger than the import record holds (%d files); import its files by hand", len(insp.Files))}}
		_, err = w.writer().Write(ctx, key, rec)
	}
	if err != nil {
		return fmt.Errorf("fileimport: write the inspect record: %w", err)
	}
	log.Info("fileimport: inspected a completed download", "files", len(insp.Files), "rejections", len(insp.Rejections))
	return nil
}

// inspectRejection is one inspect rejection.
func inspectRejection(class commonv1.ImportRejectionClass, kind, format string, args ...any) schema.InspectRejection {
	return schema.InspectRejection{Class: string(class), Kind: kind, Text: fmt.Sprintf(format, args...)}
}

func transientRejection(format string, args ...any) schema.InspectRejection {
	return inspectRejection(commonv1.ImportClassTransient, "", format, args...)
}

func needsPersonRejection(format string, args ...any) schema.InspectRejection {
	return inspectRejection(commonv1.ImportClassNeedsPerson, "", format, args...)
}

func blockedRejection(format string, args ...any) schema.InspectRejection {
	return inspectRejection(commonv1.ImportClassNeedsPerson, schema.InspectKindBlocked, format, args...)
}

func incidentalRejection(format string, args ...any) schema.InspectRejection {
	return inspectRejection("", schema.InspectKindIncidental, format, args...)
}

// resolution is the rejection a target resolution failure stands for: a
// target that does not exist or names no root folder (errBlocked) needs a
// person; anything else -- the cache, metadata not fetched yet -- is
// transient.
func resolution(err error) schema.InspectRejection {
	if errors.Is(err, errBlocked) {
		return blockedRejection("%s", blockedMessage(err))
	}
	return transientRejection("%v", err)
}

// targetOf is what the task imports to: the intent's target override, else
// the owner, keyed to the one Episode or Issue a Series or Comic entry
// covers.
func targetOf(task *schema.ImportInspectTask) importtarget.ImportTarget {
	if o := task.TargetOverride; o != nil {
		return importtarget.ImportTarget{Kind: commonv1.MediaKind(strings.ToLower(o.Kind)), Name: o.Name}
	}
	t := importtarget.ImportTarget{Kind: commonv1.MediaKind(strings.ToLower(task.Owner.Kind)), Name: task.Owner.Name}
	if (t.Kind == commonv1.MediaKindSeries || t.Kind == commonv1.MediaKindComic) && len(task.Targets) == 1 {
		t.Key = task.Targets[0].Name
	}
	return t
}

// objectKind is a MediaKind as an object Kind.
func objectKind(k commonv1.MediaKind) string {
	s := string(k)
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// inspect reads the task's payload. Its error is only one the delivery
// must retry (a failed heartbeat, a cancelled context); every finding is
// in the inspection.
func (w *Worker) inspect(ctx context.Context, m events.Message, task *schema.ImportInspectTask) (*schema.ImportInspection, error) {
	insp := &schema.ImportInspection{}
	root := task.ContentRoot
	if root == "" {
		root = task.OutputPath
	}
	if root == "" {
		insp.Rejections = append(insp.Rejections, transientRejection("the transfer reports no content root yet"))
		return insp, nil
	}
	if _, err := statDir(root); err != nil {
		insp.Rejections = append(insp.Rejections, transientRejection("content root %q is not accessible: %v", root, err))
		return insp, nil
	}
	target := targetOf(task)
	if task.Purpose == commonv1.DownloadPurposeAudioDonor {
		return insp, w.inspectDonor(ctx, m, task, root, target, insp)
	}
	ref := target.FileRef()
	switch {
	case ref.Kind == commonv1.MediaKindMovie:
		return insp, w.inspectMovie(ctx, m, task, root, ref.Name, insp)
	case IsNonVideoFileKind(ref.Kind):
		return insp, w.inspectNonVideo(ctx, m, task, root, target, insp)
	case ref.Kind == commonv1.MediaKindSeries || ref.Kind == commonv1.MediaKindEpisode:
		return insp, w.inspectEpisodes(ctx, m, task, root, target, insp)
	}
	// An artist, author or comic without an issue key holds no files
	// itself: choosing which child they belong to is a guess (P53).
	insp.Rejections = append(insp.Rejections, blockedRejection(
		"target %s is a %s, which holds no files itself; import it naming the album, book or issue the files belong to",
		target, ref.Kind))
	return insp, nil
}

// walk classifies every file under root for kind, beating on m, and hands
// each media file (and, under a manual import with no real media beside it,
// each suspected sample) to visit; a suspected sample otherwise and an
// unreadable entry are recorded as rejected files.
func (w *Worker) walk(
	ctx context.Context, m events.Message, kind commonv1.MediaKind, root string, manual bool, insp *schema.ImportInspection,
	visit func(path string, info os.FileInfo) error,
) error {
	classifier := ClassifierFor(kind, root, w.SampleMaxBytes)
	besideMedia := false
	if manual {
		n, err := countMedia(ctx, classifier, root, 1)
		if err != nil {
			insp.Rejections = append(insp.Rejections, transientRejection("%v", err))
			return nil
		}
		besideMedia = n > 0
	}
	var (
		last     time.Time
		visitErr error
	)
	err := classifier.Walk(ctx, root, func(path string, info os.FileInfo, class fsops.FileClass) error {
		if err := w.beat(ctx, m, &last); err != nil {
			visitErr = err
			return err
		}
		rel := relPath(root, path)
		switch class {
		case fsops.ClassMedia:
			if err := visit(path, info); err != nil {
				visitErr = err
				return err
			}
		case fsops.ClassSuspectedSample:
			reason := SuspectedSampleReason(info.Size(), w.SampleMaxBytes)
			switch {
			case manual && !besideMedia:
				if err := visit(path, info); err != nil {
					visitErr = err
					return err
				}
			case manual:
				insp.Files = append(insp.Files, schema.InspectedFile{
					Path: path, SizeBytes: info.Size(), Sample: true,
					Rejections: []schema.InspectRejection{incidentalRejection("%s: %s; left behind by this manual import because "+
						"the download also holds real media, and this is the promo clip beside it", rel, reason)},
				})
			default:
				insp.Files = append(insp.Files, schema.InspectedFile{
					Path: path, SizeBytes: info.Size(), Sample: true,
					Rejections: []schema.InspectRejection{inspectRejection(commonv1.ImportClassNeedsPerson, schema.InspectKindSample,
						"%s: %s; only a manual import imports it", rel, reason)},
				})
			}
		}
		return nil
	}, func(path string, err error) error {
		insp.Files = append(insp.Files, schema.InspectedFile{Path: path, Rejections: []schema.InspectRejection{
			transientRejection("%s: could not be read, so nothing in it was imported: %v", relPath(root, path), err),
		}})
		return nil
	})
	switch {
	case visitErr != nil:
		return visitErr
	case err != nil && ctx.Err() == nil:
		insp.Rejections = append(insp.Rejections, transientRejection("the payload could not be walked: %v", err))
		return nil
	}
	return err
}

// videoFacts is what one video file's read settles before its target's
// naming.
type videoFacts struct {
	parsed  *release.ParsedRelease
	mi      *commonv1.MediaInfo
	score   int
	matched []string
}

// readVideo probes a parsed video file, corrects its quality from the probe
// and scores it against prof: ReleaseTitle custom formats read sceneName
// (the release's name for this file) else the file name.
func (w *Worker) readVideo(
	ctx context.Context, m events.Message, path, rel, sceneName string, parsed *release.ParsedRelease,
	prof quality.Profile, originalLanguageName string,
) (videoFacts, error) {
	parsed.Languages = parsed.LanguagesFor(originalLanguageName)
	mi, err := w.probeVideo(ctx, m, path, rel)
	if err != nil {
		return videoFacts{}, err
	}
	parsed.Quality, _ = quality.AugmentFromMediaInfo(parsed.Quality, mi)
	title := sceneName
	if title == "" {
		title = filepath.Base(path)
	}
	ic := catalogue.ItemContext{
		OriginalLanguageName: originalLanguageName, ReleaseType: parsed.ReleaseType,
		ReleaseTitle: title, Filename: filepath.Base(path),
	}
	score, matched := prof.Score(ctx, w.Catalogue, parsed, ic)
	return videoFacts{parsed: parsed, mi: mi, score: score, matched: matched}, nil
}

// frozenFields is the import fields a video file freezes (P49).
func frozenFields(v videoFacts, prof quality.Profile) schema.FrozenFields {
	q, rev := v.parsed.Quality, v.parsed.Revision
	score := int32(v.score) //nolint:gosec // a custom-format score is a small bounded sum
	original := true
	f := schema.FrozenFields{
		Quality: &q, Revision: &rev, ReleaseType: v.parsed.ReleaseType,
		ReleaseGroup: importText(v.parsed.Group, catalogv1alpha1.MaxReleaseGroupLength),
		Languages:    v.parsed.Languages, FormatScore: &score,
		MatchedFormats: capMatchedFormats(v.matched), ProfileHash: prof.Hash, Original: &original,
	}
	if ed := importText(v.parsed.Edition, catalogv1alpha1.MaxEditionLength); ed != "" {
		f.Edition = &ed
	}
	return f
}

// summaryOf is a probe as an inspection reports it.
func summaryOf(mi *commonv1.MediaInfo) *schema.ProbeSummary {
	if mi == nil {
		return nil
	}
	return &schema.ProbeSummary{
		VideoCodec: mi.VideoCodec, Width: mi.Width, Height: mi.Height, HDR: string(mi.Hdr),
		AudioLanguages:  decision.AudioLanguages(mi),
		DurationSeconds: mi.RuntimeMillis / 1000,
		Transcoded:      mi.TranscodeProfile != "" || mi.TranscodedElsewhere(),
		ProfileTag:      mi.TranscodeProfile,
	}
}

// factsOf is a parse as an inspection reports it.
func factsOf(p *release.ParsedRelease) schema.ParsedFacts {
	var f schema.ParsedFacts
	for _, n := range p.Seasons {
		f.Seasons = append(f.Seasons, int32(n)) //nolint:gosec // a season number
	}
	for _, n := range p.Episodes {
		f.Episodes = append(f.Episodes, int32(n)) //nolint:gosec // an episode number
	}
	for _, n := range p.Absolute {
		f.Absolute = append(f.Absolute, int32(n)) //nolint:gosec // an absolute number
	}
	if p.AirDate != nil {
		f.AirDate = p.AirDate.UTC().Format("2006-01-02")
	}
	f.FullSeason, f.MultiSeason = p.FullSeason, p.MultiSeason
	return f
}

// frozenSpec is the MediaFileSpec catalogctx.File renders a file's name
// from: the fields the import freezes.
func frozenSpec(f schema.FrozenFields, releaseTitle string) *catalogv1alpha1.MediaFileSpec {
	s := &catalogv1alpha1.MediaFileSpec{
		ReleaseGroup: f.ReleaseGroup, MatchedFormats: f.MatchedFormats,
		ImportedFrom: &catalogv1alpha1.ImportSource{ReleaseTitle: importText(releaseTitle, catalogv1alpha1.MaxReleaseTitleLength)},
	}
	if f.Quality != nil {
		s.Quality = *f.Quality
	}
	if f.Revision != nil {
		s.Revision = *f.Revision
	}
	if f.Edition != nil {
		s.Edition = *f.Edition
	}
	return s
}

// originalName is a language tag's name in the catalogue's vocabulary.
func originalName(tag string) string {
	if tag == "" {
		return ""
	}
	n, _ := catalogue.LanguageName(tag)
	return n
}

// inspectMovie reads a movie's payload.
func (w *Worker) inspectMovie(
	ctx context.Context, m events.Message, task *schema.ImportInspectTask, root, name string, insp *schema.ImportInspection,
) error {
	ns := task.Owner.Namespace
	var movie catalogv1alpha1.Movie
	if err := w.getItem(ctx, ns, "movie", name, &movie); err != nil {
		insp.Rejections = append(insp.Rejections, resolution(err))
		return nil
	}
	if movie.Status.Metadata == nil {
		insp.Rejections = append(insp.Rejections, transientRejection("movie %s has no metadata yet", name))
		return nil
	}
	rf, err := w.getRoot(ctx, ns, movie.Spec.RootFolderRef)
	if err != nil {
		insp.Rejections = append(insp.Rejections, resolution(err))
		return nil
	}
	prof, err := w.resolveProfile(ctx, task.QualityProfile, movie.Spec.QualityProfileRef)
	if err != nil {
		insp.Rejections = append(insp.Rejections, resolution(err))
		return nil
	}
	setRoot(insp, rf)
	base, _ := catalogctx.Movie(&movie)
	lang := originalName(movie.Status.Metadata.OriginalLanguage)
	target := &schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: ns, Name: movie.Name, UID: string(movie.UID)}}
	return w.walk(ctx, m, commonv1.MediaKindMovie, root, task.Manual || task.Override, insp, func(path string, info os.FileInfo) error {
		rel := relPath(root, path)
		f := schema.InspectedFile{Path: path, SizeBytes: info.Size()}
		parsed, perr := parseMediaFile(path, task.Release.Title, commonv1.MediaKindMovie)
		if perr != nil {
			f.Rejections = append(f.Rejections, needsPersonRejection("%s: could not parse the filename: %v", rel, perr))
			insp.Files = append(insp.Files, f)
			return nil
		}
		v, err := w.readVideo(ctx, m, path, rel, task.Release.Title, parsed, prof, lang)
		if err != nil {
			return err
		}
		f.Parsed, f.Probe = factsOf(v.parsed), summaryOf(v.mi)
		f.Frozen = frozenFields(v, prof)
		nctx := catalogctx.File(ctx, base, frozenSpec(f.Frozen, task.Release.Title), v.mi)
		dest, derr := catalogctx.MovieFilePath(rf, &movie, nctx, catalogctx.ContainerExt(v.mi, path))
		if derr != nil {
			f.Rejections = append(f.Rejections, blockedRejection("%s: could not render a destination path: %v", rel, derr))
			insp.Files = append(insp.Files, f)
			return nil
		}
		f.Proposed, f.Keys, f.Dest = target, []string{movie.Name}, dest
		insp.Files = append(insp.Files, f)
		return nil
	})
}

// inspectEpisodes reads an episode's or a season pack's payload: each file
// is attributed by the numbering its name carries (MatchEpisodes) to
// episodes of the target's series (P63), or under a manual import to one
// named episode a file whose name carries no numbering.
func (w *Worker) inspectEpisodes(
	ctx context.Context, m events.Message, task *schema.ImportInspectTask, root string, target importtarget.ImportTarget,
	insp *schema.ImportInspection,
) error {
	ns := task.Owner.Namespace
	plan, err := w.resolveSeries(ctx, ns, target, task.QualityProfile)
	if err != nil {
		insp.Rejections = append(insp.Rejections, resolution(err))
		return nil
	}
	setRoot(insp, plan.rootFolder)
	manual := task.Manual || task.Override
	classifier := ClassifierFor(commonv1.MediaKindEpisode, root, w.SampleMaxBytes)
	videos, err := countMedia(ctx, classifier, root, 2)
	if err != nil {
		insp.Rejections = append(insp.Rejections, transientRejection("%v", err))
		return nil
	}
	// The release title is the scene name of an episode file only when it
	// is not a full season's and the download holds no other video file
	// (Sonarr's SceneNameCalculator).
	if videos == 1 {
		if p, perr := release.Parse(task.Release.Title, release.Options{Kind: commonv1.MediaKindEpisode}); perr == nil && !p.FullSeason {
			plan.sceneName = task.Release.Title
		}
	}
	series := plan.series
	return w.walk(ctx, m, commonv1.MediaKindEpisode, root, manual, insp, func(path string, info os.FileInfo) error {
		rel := relPath(root, path)
		f := schema.InspectedFile{Path: path, SizeBytes: info.Size()}
		parsed, perr := parseMediaFile(path, plan.sceneName, commonv1.MediaKindEpisode)
		if perr != nil {
			f.Rejections = append(f.Rejections, needsPersonRejection("%s: could not parse the filename: %v", rel, perr))
			insp.Files = append(insp.Files, f)
			return nil
		}
		f.Parsed = factsOf(parsed)
		eps, reason := MatchEpisodes(parsed, series.Spec.SeriesType, plan.episodes)
		if reason != "" {
			if !manual || plan.named == nil || len(parsed.Episodes)+len(parsed.Absolute) > 0 || parsed.AirDate != nil {
				f.Rejections = append(f.Rejections, needsPersonRejection("%s: not attributable to an episode of series %s: %s",
					rel, series.Name, reason))
				insp.Files = append(insp.Files, f)
				return nil
			}
			eps = []EpisodeCandidate{*plan.named}
		}
		v, err := w.readVideo(ctx, m, path, rel, plan.sceneName, parsed, plan.profile, plan.originalLanguageName)
		if err != nil {
			return err
		}
		f.Probe = summaryOf(v.mi)
		f.Frozen = frozenFields(v, plan.profile)
		nctx, _ := catalogctx.Episode(series, episodesFor(eps))
		nctx = catalogctx.File(ctx, nctx, frozenSpec(f.Frozen, task.Release.Title), v.mi)
		dest, derr := catalogctx.EpisodeFilePath(plan.rootFolder, series, nctx, catalogctx.ContainerExt(v.mi, path))
		if derr != nil {
			f.Rejections = append(f.Rejections, blockedRejection("%s: could not render a destination path: %v", rel, derr))
			insp.Files = append(insp.Files, f)
			return nil
		}
		f.Proposed = &schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: ns, Name: eps[0].Name}}
		f.Keys, f.Dest = names(eps), dest
		insp.Files = append(insp.Files, f)
		return nil
	})
}

// inspectNonVideo reads an album's, book's, audiobook's or issue's
// payload: its quality frozen by extension or an audio probe, its path
// under the item's folder.
func (w *Worker) inspectNonVideo(
	ctx context.Context, m events.Message, task *schema.ImportInspectTask, root string, target importtarget.ImportTarget,
	insp *schema.ImportInspection,
) error {
	ns := task.Owner.Namespace
	plan, err := w.resolveNonVideo(ctx, ns, target, task.QualityProfile)
	if err != nil {
		insp.Rejections = append(insp.Rejections, resolution(err))
		return nil
	}
	setRoot(insp, plan.rootFolder)
	kind := plan.ref.Kind
	if plan.singleTrack != "" {
		// A one-track album's lone file is that track; two files for it
		// are not both that track.
		if n, err := countMedia(ctx, ClassifierFor(kind, root, w.SampleMaxBytes), root, 2); err != nil || n != 1 {
			plan.singleTrack = ""
		}
	}
	proposed := &schema.ItemRef{Kind: objectKind(kind), Ref: schema.Ref{Namespace: ns, Name: plan.ref.Name}}
	return w.walk(ctx, m, kind, root, task.Manual || task.Override, insp, func(path string, info os.FileInfo) error {
		rel := relPath(root, path)
		f := schema.InspectedFile{Path: path, SizeBytes: info.Size()}
		var hbErr error
		q, known := FrozenFileQuality(ctx, w.audioProbe(m, &hbErr), kind, path, task.Release.Title, rel)
		if hbErr != nil {
			return hbErr
		}
		original := true
		f.Frozen = schema.FrozenFields{ReleaseType: ReleaseTypeFor(kind), Original: &original, Track: plan.singleTrack}
		if known {
			f.Frozen.Quality = &q
		}
		f.Proposed, f.Keys = proposed, []string{plan.ref.Name}
		f.Dest = naming.SanitizePath(filepath.Join(plan.folder, plan.fileName(rel)), naming.DefaultSanitizeOptions())
		insp.Files = append(insp.Files, f)
		return nil
	})
}

// inspectDonor reads an audio donor's payload: its largest media or
// Matroska audio file, probed for its audio languages.
func (w *Worker) inspectDonor(
	ctx context.Context, m events.Message, task *schema.ImportInspectTask, root string, target importtarget.ImportTarget,
	insp *schema.ImportInspection,
) error {
	ns := task.Owner.Namespace
	item, err := w.donorItem(ctx, ns, target.FileRef())
	if err != nil {
		insp.Rejections = append(insp.Rejections, transientRejection("%v", err))
		return nil
	}
	if item == nil {
		insp.Rejections = append(insp.Rejections, inspectRejection(commonv1.ImportClassItemState, "",
			"a donor is for an episode or a movie; %s %q is gone or neither", target.Kind, target.Name))
		return nil
	}
	rf, err := w.getRoot(ctx, ns, item.rootFolder)
	if err != nil {
		insp.Rejections = append(insp.Rejections, resolution(err))
		return nil
	}
	setRoot(insp, rf)
	src, info, err := donorFile(root)
	if err != nil {
		insp.Rejections = append(insp.Rejections, transientRejection("content root %q: %v", root, err))
		return nil
	}
	if src == "" {
		return nil
	}
	// A donor is judged on its probe alone; a worker with no prober cannot
	// judge one, which is no fault of the release: the delivery is retried.
	if w.Prober == nil {
		return fmt.Errorf("fileimport: an audio donor and this worker has no prober to judge it")
	}
	mi, err := w.probeVideo(ctx, m, src, relPath(root, src))
	if err != nil {
		return err
	}
	insp.Files = append(insp.Files, schema.InspectedFile{
		Path: src, SizeBytes: info.Size(), Probe: summaryOf(mi),
		Proposed: &schema.ItemRef{Kind: objectKind(kindOf(item.obj)), Ref: schema.Ref{
			Namespace: ns, Name: item.obj.GetName(), UID: string(item.obj.GetUID()),
		}},
	})
	return nil
}

// setRoot records the root folder the files go under.
func setRoot(insp *schema.ImportInspection, rf *catalogv1alpha1.RootFolder) {
	if rf == nil {
		return
	}
	insp.RootFolder, insp.RecycleBin, insp.MinFreeBytes = rf.Spec.Path, rf.Spec.RecycleBin.Path, rf.Spec.MinFreeBytes
}
