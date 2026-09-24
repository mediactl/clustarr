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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

const (
	// heartbeatInterval is how often the walk sends an in-progress ack.
	// ConsumerImportScan's AckWait is 60s (topology.go pins it to the
	// worker Deployment's terminationGracePeriodSeconds), and topology.go's
	// own comment requires any unit of work that can outlast it to
	// heartbeat rather than have AckWait raised. 20s leaves two missed
	// beats of headroom.
	heartbeatInterval = 20 * time.Second

	// checkpointInterval is how often the running tally is written to the
	// clustarr-progress bucket. The LibraryScan controller polls at 3s, so
	// checkpointing faster only costs KV writes without making the CRD any
	// fresher.
	checkpointInterval = 3 * time.Second

	// defaultMetadataTimeout bounds one imdb-to-tmdb resolve RPC. A walk
	// makes at most one per id-carrying file, and a gateway that is down
	// must leave those files unmatched quickly rather than stall the scan.
	defaultMetadataTimeout = 10 * time.Second

	// maxUnmatched caps the unmatched list the worker accumulates in
	// memory. It is catalogv1alpha1.LibraryScanStatus.Unmatched's own
	// MaxItems: the controller sorts newest-first and truncates to the same
	// number, so carrying more would only inflate the KV value. The worker
	// keeps the newest by dropping the oldest as it goes.
	maxUnmatched = 200
)

// Worker handles clustarr.work.importarr.scan.* messages: one message is one
// LibraryScan's walk. See this package's doc comment for how Task C12
// registers it.
type Worker struct {
	// Client is the cache-backed client the walk reads Movies and
	// MediaFiles through and applies them with.
	Client client.Client

	// Bus carries the progress checkpoints and the metadata-resolve RPC.
	Bus events.Bus

	// Clock is the time source, injected so tests are deterministic.
	Clock func() time.Time

	// MetadataTimeout bounds one resolve RPC. Zero means
	// defaultMetadataTimeout.
	MetadataTimeout time.Duration

	// ProbeAudio reads a music file's codec and bitrate, so a lossy track
	// freezes the tier its bitrate puts it on
	// (fileimport.FrozenFileQuality). NewWorker sets mediainfo.ProbeAudio;
	// nil freezes by extension alone.
	ProbeAudio fileimport.AudioProber

	// Catalogue is the TRaSH custom-format corpus a scanned movie file is
	// scored against, as the file-import worker scores an imported one.
	// NewWorker sets catalogue.LoadedCatalogue(); nil means the same.
	Catalogue *catalogue.Catalogue

	// ProbeTranscodeProfile reads a file's CLUSTARR_PROFILE tag, the last
	// test of whether a file named like a kept source's transcode output is
	// one (keptOutput). NewWorker sets the pkg/mediainfo probe; nil skips
	// that test, so only the kept source's own record can confirm one.
	ProbeTranscodeProfile ProbeTranscodeProfile

	// SampleMaxBytes is the video size floor (fsops.IsSuspectedSample): a
	// video file smaller than this whose name does not mark it a sample is
	// a SUSPECTED sample. The walk records it in LibraryScan.status.unmatched
	// with the reason [CodeSuspectedSample] instead of attributing it --
	// never skips it, since a size cannot tell a promo clip from a short
	// film -- unless a MediaFile already records the path, or the scan is a
	// manual assignment; see walk. Zero disables the size rule. NewWorker
	// sets fsops.DefaultSampleMaxBytes; a Worker built as a literal without
	// it has the rule off.
	SampleMaxBytes int64
}

// The rescan worker's RBAC. It is the sole writer of MediaFileSpec (spec §8.4)
// and creates the Movie a scanned file is attributed to, and the Series a
// series folder's TheTVDB id names; it never writes
// MediaFileStatus or LibraryScan.status, both of which have their own single
// writer, so neither /status subresource appears here. It reads
// QualityProfiles to score a scanned movie file, and patches one annotation
// on a post-transcode MediaFile (AnnotationObservedFingerprint), which the
// mediafiles patch verb already covers.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=artists;albums;authors;books;audiobooks;comics;issues,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=series,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=libraryscans,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch

// NewWorker builds a Worker with the production clock, timeout and sample
// threshold.
func NewWorker(c client.Client, bus events.Bus) *Worker {
	return &Worker{
		Client: c, Bus: bus, Clock: time.Now, MetadataTimeout: defaultMetadataTimeout,
		Catalogue: catalogue.LoadedCatalogue(), ProbeAudio: mediainfo.ProbeAudio,
		ProbeTranscodeProfile: probeTranscodeProfile, SampleMaxBytes: fsops.DefaultSampleMaxBytes,
	}
}

func (w *Worker) now() time.Time {
	if w.Clock != nil {
		return w.Clock()
	}
	return time.Now()
}

func (w *Worker) metadataTimeout() time.Duration {
	if w.MetadataTimeout > 0 {
		return w.MetadataTimeout
	}
	return defaultMetadataTimeout
}

// scanState is everything one walk carries. It exists so the per-file
// handler in mediafile.go takes one parameter instead of eight.
type scanState struct {
	task     schema.ScanTask
	scan     *catalogv1alpha1.LibraryScan
	root     *catalogv1alpha1.RootFolder
	movies   []MovieCandidate
	progress Progress

	// nonVideo holds the candidates of a music, book, audiobook or comic
	// root folder; nil for a movie root.
	nonVideo *nonVideoIndex

	// series holds the series of a series root folder, with their
	// episodes.
	series []SeriesCandidate

	// manual is the scan's import-target annotation, resolved; nil unless
	// the scan is a manual assignment.
	manual *manualAssign

	// manualHasMedia is true when a manual assignment's walk holds at least
	// one plain media file, which makes any suspected sample beside it a
	// promo clip to leave behind (walkHoldsMedia).
	manualHasMedia bool

	// resumeAfter is a resumed tally's Resume path: the walk passes over
	// every file up to and including it, whose outcome is already counted.
	resumeAfter string

	// transcodeOutputs maps each status.result.outputPath of a Succeeded
	// TranscodeJob in the scan's namespace (cleaned) to that job's name:
	// files the walk must not adopt unless a MediaFile already records
	// them. Listed once per scan (transcodeOutputs).
	transcodeOutputs map[string]string

	// profiles caches the QualityProfiles a movie walk scores files with,
	// by name; a nil entry is a profile that could not be used.
	profiles map[string]*quality.Profile

	// lastCheckpoint is when the running tally last reached the KV bucket.
	lastCheckpoint time.Time

	// lastHeartbeat is when the delivery's ack deadline was last extended.
	lastHeartbeat time.Time
}

// incremental reports whether unchanged fingerprints may be skipped. The CRD
// defaults spec.mode to incremental, so an empty mode is incremental too.
func (s *scanState) incremental() bool {
	return s.task.Mode != string(catalogv1alpha1.ScanModeFull)
}

// unmatched records one file the walk could not attribute, keeping only the
// newest maxUnmatched entries. The scanner never guesses: this is the only
// thing that happens to a file MatchMovie declined.
func (s *scanState) unmatched(path, code, reason string, candidates []string, at time.Time) {
	s.progress.Unmatched = append(s.progress.Unmatched, UnmatchedFile{
		Path:       path,
		Reason:     reason,
		Candidates: candidates,
		SeenAt:     at,
	})
	if len(s.progress.Unmatched) > maxUnmatched {
		s.progress.Unmatched = s.progress.Unmatched[len(s.progress.Unmatched)-maxUnmatched:]
	}
	kind := ""
	if s.root != nil {
		kind = string(s.root.Spec.Kind) // a closed enum, so a bounded label
	}
	metrics.ImportUnmatchedTotal.WithLabelValues(kind, code).Inc()
}

// Handle implements events.Handler. It walks the ScanTask's path, attributes
// what it finds, and reports progress through the clustarr-progress bucket --
// never through LibraryScan.status, whose single writer is the LibraryScan
// controller.
func (w *Worker) Handle(ctx context.Context, m events.Message) error {
	ctx, span := tracing.Start(ctx, "rescan.Handle")
	defer span.End()

	env := m.Envelope()
	if env == nil {
		return events.Discard("scan task has no envelope", errors.New("rescan: nil envelope"))
	}

	var task schema.ScanTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable scan task", err)
	}
	if task.Path == "" || task.LibraryScanRef.Name == "" || task.RootFolderRef.Name == "" {
		return events.Discard("incomplete scan task",
			fmt.Errorf("rescan: scan task is missing path, libraryScanRef or rootFolderRef: %+v", task))
	}

	log := logging.FromContext(ctx).With(
		"libraryScan", task.LibraryScanRef.String(),
		"rootFolder", task.RootFolderRef.String(),
		"mode", task.Mode,
	)
	ctx = logging.NewContext(ctx, log)

	st := &scanState{task: task}

	var scan catalogv1alpha1.LibraryScan
	scanKey := types.NamespacedName{Namespace: task.LibraryScanRef.Namespace, Name: task.LibraryScanRef.Name}
	if err := w.Client.Get(ctx, scanKey, &scan); err != nil {
		if apierrors.IsNotFound(err) {
			// The scan was deleted (its TTL elapsed, or a user removed
			// it). There is nobody left to report to, so the message is
			// finished rather than retried.
			return events.Discard("library scan no longer exists", err)
		}
		return w.abort(ctx, m, st, fmt.Errorf("rescan: get library scan %s: %w", scanKey, err))
	}
	if task.LibraryScanRef.UID != "" && string(scan.UID) != task.LibraryScanRef.UID {
		// A scan deleted and recreated under the same name is a different
		// scan; finishing the old task against it would report the wrong
		// tally under the wrong progress key.
		return events.Discard("library scan was replaced", fmt.Errorf(
			"rescan: library scan %s has uid %s, task was published for %s",
			scanKey, scan.UID, task.LibraryScanRef.UID))
	}
	st.scan = &scan

	switch scan.Status.Phase {
	case catalogv1alpha1.ScanPhaseCompleted, catalogv1alpha1.ScanPhaseFailed:
		// The controller has settled the scan -- finished, or failed for
		// want of progress or because this very task was dead-lettered. A
		// late delivery walking now would change the library with nobody
		// left to report it to.
		return events.Discard("library scan already settled", fmt.Errorf(
			"rescan: library scan %s is %s", scanKey, scan.Status.Phase))
	}

	done, err := w.resume(ctx, st)
	if err != nil {
		return w.abort(ctx, m, st, err)
	}
	if done {
		// A redelivery of a walk that already reported its final tally
		// (the ack was lost): walking again would overwrite that tally
		// with a partial one, driving the counters backwards.
		log.Debug("library scan already reported done; redelivery is a no-op")
		return nil
	}

	var root catalogv1alpha1.RootFolder
	rootKey := types.NamespacedName{Namespace: task.RootFolderRef.Namespace, Name: task.RootFolderRef.Name}
	if err := w.Client.Get(ctx, rootKey, &root); err != nil {
		return w.abort(ctx, m, st, fmt.Errorf("rescan: get root folder %s: %w", rootKey, err))
	}
	st.root = &root

	// A subpath that climbs out of the root folder ("../elsewhere") would
	// otherwise be walked, and every file found recorded against items of
	// this root. The LibraryScan controller joins spec.subpath without
	// checking it, and the UI creates LibraryScans, so the worker refuses.
	if !withinRoot(root.Spec.Path, task.Path) {
		return w.refuseScan(ctx, st, refuse("scan path %q is outside root folder %q (%s)",
			task.Path, root.Name, root.Spec.Path))
	}

	manual, err := w.resolveManualAssign(ctx, &scan, &root)
	switch {
	case errors.Is(err, errScanRefused):
		return w.refuseScan(ctx, st, err)
	case err != nil:
		return w.abort(ctx, m, st, err)
	}
	st.manual = manual
	if manual != nil {
		hasMedia, err := w.walkHoldsMedia(ctx, st)
		if err != nil {
			return w.abort(ctx, m, st, err)
		}
		st.manualHasMedia = hasMedia
	}

	outputs, err := w.transcodeOutputs(ctx, scan.Namespace)
	if err != nil {
		return w.abort(ctx, m, st, err)
	}
	st.transcodeOutputs = outputs

	switch {
	case st.manual != nil:
		// A manual assignment matches nothing, so it loads no candidates.
	case root.Spec.Kind == catalogv1alpha1.RootFolderKindMovie:
		if err := w.loadMovies(ctx, &st.movies, scan.Namespace); err != nil {
			return w.abort(ctx, m, st, err)
		}
	case root.Spec.Kind == catalogv1alpha1.RootFolderKindSeries:
		series, err := w.loadSeries(ctx, scan.Namespace, &root)
		if err != nil {
			return w.abort(ctx, m, st, err)
		}
		st.series = series
	case fileKindForRoot(root.Spec.Kind) != "":
		idx, err := w.loadNonVideo(ctx, scan.Namespace, &root)
		if err != nil {
			return w.abort(ctx, m, st, err)
		}
		st.nonVideo = idx
	}

	if err := w.walk(ctx, m, st); err != nil {
		return w.abort(ctx, m, st, err)
	}

	st.progress.Done = true
	st.progress.Resume = ""
	if err := w.checkpoint(ctx, st, true); err != nil {
		// The walk itself succeeded; only the final report failed. Retry
		// so the controller is not left polling a Running scan forever.
		// The retry resumes from the last checkpoint, past every file.
		return events.Retry(checkpointInterval, err)
	}
	log.Info("library scan finished",
		"filesSeen", st.progress.FilesSeen,
		"filesMatched", st.progress.FilesMatched,
		"filesSkipped", st.progress.FilesSkipped,
		"itemsCreated", st.progress.ItemsCreated,
		"itemsUpdated", st.progress.ItemsUpdated,
		"unmatched", len(st.progress.Unmatched),
		"summary", st.progress.Summary())
	return nil
}

// resume seeds st.progress from this scan's own checkpoint, when one
// survives, so a redelivered task carries on from where the last delivery
// got to rather than starting the tally over. done is true when that
// checkpoint is the final one: the walk finished and only its ack was lost.
//
// A redelivery without a checkpoint -- the clustarr-progress bucket's TTL
// outlived the gap -- walks from the top; the LibraryScan controller never
// lets that restarted tally lower a counter it has already reported.
func (w *Worker) resume(ctx context.Context, st *scanState) (done bool, err error) {
	entry, err := w.Bus.KV(events.BucketProgress).Get(ctx, ProgressKey(w.scanUID(st)))
	switch {
	case errors.Is(err, events.ErrKeyNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("rescan: read progress checkpoint: %w", err)
	}
	prev, err := DecodeProgress(entry.Value)
	if err != nil {
		// An undecodable checkpoint cannot be resumed from; a fresh walk
		// overwrites it.
		logging.FromContext(ctx).Warn("ignoring an undecodable progress checkpoint", "error", err)
		return false, nil
	}
	if prev.Done {
		return true, nil
	}
	st.progress = prev
	st.resumeAfter = prev.Resume
	if prev.Resume != "" {
		logging.FromContext(ctx).Info("resuming a library scan after a redelivery", "after", prev.Resume,
			"filesSeen", prev.FilesSeen)
	}
	return false, nil
}

// resuming reports whether path's outcome is already in a resumed tally:
// it is at or before the checkpoint's Resume path in walk order. Once the
// walk passes that path it stops asking.
func (st *scanState) resuming(path string) bool {
	if st.resumeAfter == "" {
		return false
	}
	if walkOrderLess(st.resumeAfter, path) {
		st.resumeAfter = ""
		return false
	}
	return true
}

// walkOrderLess reports whether a comes before b in the order
// filepath.WalkDir visits them: directory entries sorted by name, each
// directory before everything beneath it -- which is a component-wise
// comparison, not a plain string one ("a/b" is visited before "a-c",
// although "a-c" < "a/b" as strings).
func walkOrderLess(a, b string) bool {
	as := strings.Split(filepath.Clean(a), string(filepath.Separator))
	bs := strings.Split(filepath.Clean(b), string(filepath.Separator))
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
}

// walk is the classify-and-dispatch loop. Every file is classified as its
// root folder's kind (fileimport.ClassifierFor), bounded by the root
// folder's path -- not the walked path, so a scan narrowed by spec.subpath
// classifies a file exactly as a full scan would.
//
// A part, an extras-folder file, a file whose NAME marks it a sample, and a
// non-media file are not considered: each is counted by what it is
// (Progress.NotMedia and friends) and matched against nothing. The first
// three are a release's own packaging, and a "-sample" file accompanies
// nearly every scene release, so listing each in the capped unmatched list
// would evict the genuinely unattributable files the list exists for. A
// SUSPECTED sample -- a video file only the size floor flags -- is never
// passed over; see suspectedSample.
//
// An entry the walk cannot read is listed as unmatched ([CodeUnreadable])
// and the walk carries on. After each file the tally's Resume marker moves
// to it, so a redelivery picks up after the last file checkpointed.
func (w *Worker) walk(ctx context.Context, m events.Message, st *scanState) error {
	classifier := fileimport.ClassifierFor(fileKindForRoot(st.root.Spec.Kind), st.root.Spec.Path, w.SampleMaxBytes)
	return classifier.Walk(ctx, st.task.Path, func(path string, info os.FileInfo, class fsops.FileClass) error {
		if err := w.beat(ctx, m, st); err != nil {
			return err
		}
		if st.resuming(path) {
			return nil
		}
		if err := w.visit(ctx, st, path, info, class); err != nil {
			return err
		}
		st.progress.Resume = path
		return w.checkpoint(ctx, st, false)
	}, func(path string, err error) error {
		if berr := w.beat(ctx, m, st); berr != nil {
			return berr
		}
		if st.resuming(path) {
			return nil
		}
		st.progress.Unreadable++
		st.unmatched(relPath(st.root.Spec.Path, path), CodeUnreadable, fmt.Sprintf(
			"could not be read, so the scan went on without it (and anything beneath it): %v", err), nil, w.now())
		st.progress.Resume = path
		return w.checkpoint(ctx, st, false)
	})
}

// visit is one walked file's outcome, by its class.
func (w *Worker) visit(ctx context.Context, st *scanState, path string, info os.FileInfo, class fsops.FileClass) error {
	if class == fsops.ClassMedia || class == fsops.ClassSuspectedSample {
		if skip, err := w.transcodeOutput(ctx, st, path); err != nil || skip {
			return err
		}
	}
	switch class {
	case fsops.ClassMedia:
	case fsops.ClassSuspectedSample:
		surfaced, err := w.suspectedSample(ctx, st, path, info)
		if err != nil || surfaced {
			return err
		}
	default:
		switch class {
		case fsops.ClassPart:
			st.progress.Parts++
		case fsops.ClassExtra:
			st.progress.Extras++
		case fsops.ClassSample:
			st.progress.Samples++
		default:
			st.progress.NotMedia++
		}
		logging.FromContext(ctx).Debug("did not consider a walked file",
			"path", relPath(st.root.Spec.Path, path), "class", class.String())
		return nil
	}
	st.progress.FilesSeen++
	return w.handleMediaFile(ctx, st, path, info)
}

// transcodeOutputs lists the scan namespace's TranscodeJobs once and
// returns the output path of every Succeeded one that has a result, mapped
// to the job's name. One List per scan, from the cache, not one per file.
func (w *Worker) transcodeOutputs(ctx context.Context, ns string) (map[string]string, error) {
	var jobs transcodev1alpha1.TranscodeJobList
	if err := w.Client.List(ctx, &jobs, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("rescan: list transcode jobs in %s: %w", ns, err)
	}
	out := map[string]string{}
	for i := range jobs.Items {
		j := &jobs.Items[i]
		if j.Status.Phase != transcodev1alpha1.TranscodeJobPhaseSucceeded || j.Status.Result == nil ||
			j.Status.Result.OutputPath == "" {
			continue
		}
		out[filepath.Clean(j.Status.Result.OutputPath)] = j.Name
	}
	return out, nil
}

// transcodeOutput decides a media file squasharr wrote. Once a MediaFile
// records the path -- catalogarr moved spec.path to a container change's new
// file, or the job transcoded in place -- the file is the catalog's like any
// other, and skip is false. Until then a file a Succeeded TranscodeJob names
// is skipped and counted (Progress.TranscodeOutputs): a container change
// writes <stem>.<container> and retires the source, and replaceSource=false
// keeps the source and writes "<stem> - <profile>.<container>" beside it,
// and in the window before catalogarr's spec.path takeover -- and for good,
// for a kept source's derived file -- the walk would otherwise adopt the
// output as an unrecorded file: a second MediaFile for the item, or an
// unmatched entry, or a new Movie. A kept source's derived file stays
// protected once its job is gone too, recognised by its name beside the
// recorded source and squasharr's tag (keptOutput).
func (w *Worker) transcodeOutput(ctx context.Context, st *scanState, path string) (skip bool, err error) {
	job, byJob := st.transcodeOutputs[filepath.Clean(path)]
	if !byJob {
		if _, _, named := keptOutputName(filepath.Base(path)); !named {
			return false, nil
		}
	}
	existing, err := w.existingMediaFile(ctx, st.scan.Namespace, path)
	if err != nil || existing != nil {
		return false, err
	}
	if !byJob {
		return w.keptOutput(ctx, st, path)
	}
	st.progress.FilesSeen++
	st.progress.TranscodeOutputs++
	st.progress.FilesSkipped++
	logging.FromContext(ctx).Debug("left a transcode output catalogarr has not recorded yet",
		"path", relPath(st.root.Spec.Path, path), "transcodeJob", job)
	return true, nil
}

// walkHoldsMedia reports whether the walk of a manual assignment will visit
// at least one plain media file (fsops.ClassMedia). A person who assigns a
// FOLDER assigns the release in it, and a suspected sample beside real media
// there is the promo clip that ships with it: it is left behind, reported as
// unmatched, rather than swept into the item. A suspected sample that is the
// folder's only video -- or a file subpath naming it -- is what the person
// assigned, and is taken (see suspectedSample). It is a classification-only
// pass, with no side effect, so a redelivery repeating it is harmless.
func (w *Worker) walkHoldsMedia(ctx context.Context, st *scanState) (bool, error) {
	classifier := fileimport.ClassifierFor(fileKindForRoot(st.root.Spec.Kind), st.root.Spec.Path, w.SampleMaxBytes)
	found := false
	err := classifier.Walk(ctx, st.task.Path, func(_ string, _ os.FileInfo, class fsops.FileClass) error {
		if class == fsops.ClassMedia {
			found = true
			return filepath.SkipAll
		}
		return nil
	}, func(string, error) error { return nil })
	if err != nil {
		return false, fmt.Errorf("rescan: look for media under %s: %w", st.task.Path, err)
	}
	return found, nil
}

// suspectedSample decides a file only the size floor flags as a sample
// (fsops.ClassSuspectedSample). The scanner never guesses, and a size is a
// guess -- it cannot tell a promo clip from a 45 MiB short film or an old
// low-resolution episode -- so the file is recorded as unmatched with
// [CodeSuspectedSample], naming its size and the threshold, and counted as
// seen: surfaced is true and the walk moves on.
//
// Two cases are not a guess, and surfaced is false so the walk attributes
// the file like any other media file:
//
//   - a path a MediaFile already records: the MediaFile is its
//     attribution (see handleMediaFile), however it was made -- a manual
//     assignment, or an import under a lower threshold -- and reporting it
//     unmatched on every later scan would list a file that is in the
//     catalog.
//   - a manual assignment whose walk holds no other media file: a person
//     named this file's item -- by a subpath naming the file, or a folder
//     whose only video it is -- and a size does not overrule them. Without
//     this, the unmatched page's assign action could never assign the very
//     file this reports.
//
// A manual assignment of a folder that ALSO holds real media is the other
// way round: the small file beside the release is its promo clip, and is
// left behind -- surfaced as unmatched with the remedy -- rather than swept
// into the item with the real file (walkHoldsMedia).
func (w *Worker) suspectedSample(ctx context.Context, st *scanState, path string, info os.FileInfo) (surfaced bool, err error) {
	if fileKindForRoot(st.root.Spec.Kind) == "" {
		return false, nil
	}
	existing, err := w.existingMediaFile(ctx, st.scan.Namespace, path)
	if err != nil || existing != nil {
		return false, err
	}
	remedy := "; if it is real media, assign it by hand (the unmatched page's assign action, or a LibraryScan " +
		"annotated " + fileimport.AnnotationImportTarget + " whose subpath names this file)"
	if st.manual != nil {
		if !st.manualHasMedia {
			return false, nil
		}
		remedy = "; left behind by this manual assignment, because the folder it assigns also holds real media " +
			"and this is the promo clip beside it -- if it is real media, assign it with a subpath naming this file"
	}
	st.progress.FilesSeen++
	st.unmatched(relPath(st.root.Spec.Path, path), CodeSuspectedSample,
		fileimport.SuspectedSampleReason(info.Size(), w.SampleMaxBytes)+remedy, nil, w.now())
	return true, nil
}

// beat extends the delivery's ack deadline when heartbeatInterval has
// elapsed. A library walk is the canonical long task: without this the broker
// redelivers it after AckWait and two workers walk the same tree.
func (w *Worker) beat(ctx context.Context, m events.Message, st *scanState) error {
	now := w.now()
	if !st.lastHeartbeat.IsZero() && now.Sub(st.lastHeartbeat) < heartbeatInterval {
		return nil
	}
	st.lastHeartbeat = now
	if err := m.InProgress(ctx); err != nil {
		return fmt.Errorf("rescan: heartbeat: %w", err)
	}
	return nil
}

// checkpoint writes the running tally to the clustarr-progress bucket, at
// most once per checkpointInterval unless force is set. A checkpoint failure
// mid-walk is logged and swallowed: losing one intermediate tally is not
// worth abandoning a walk that is otherwise succeeding, and the next
// checkpoint supersedes it. The final one (force) is not swallowed.
func (w *Worker) checkpoint(ctx context.Context, st *scanState, force bool) error {
	now := w.now()
	if !force && !st.lastCheckpoint.IsZero() && now.Sub(st.lastCheckpoint) < checkpointInterval {
		return nil
	}
	st.lastCheckpoint = now

	// The controller renders unmatched newest-first and truncates; sorting
	// here means a mid-walk checkpoint and the final one agree on which 200
	// entries survive.
	sorted := make([]UnmatchedFile, len(st.progress.Unmatched))
	copy(sorted, st.progress.Unmatched)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].SeenAt.After(sorted[j].SeenAt) })
	snapshot := st.progress
	snapshot.Unmatched = sorted

	data, err := snapshot.Encode()
	if err != nil {
		return err
	}
	if _, err := w.Bus.KV(events.BucketProgress).Put(ctx, ProgressKey(w.scanUID(st)), data); err != nil {
		err = fmt.Errorf("rescan: checkpoint progress: %w", err)
		if force {
			return err
		}
		logging.FromContext(ctx).Warn("checkpoint failed; continuing the walk", "error", err)
	}
	return nil
}

// scanUID is the key the progress checkpoint lives under: the object's own
// UID when the worker managed to read it, and otherwise the UID the task
// carried, so even an aborted task reports under the key the controller
// polls.
func (w *Worker) scanUID(st *scanState) string {
	if st.scan != nil && st.scan.UID != "" {
		return string(st.scan.UID)
	}
	return st.task.LibraryScanRef.UID
}

// abort ends a walk that could not finish. On anything but the final delivery
// it asks for a redelivery and leaves the scan Running, so a transient
// apiserver or filesystem blip is simply retried. On the final delivery it
// writes a Done checkpoint carrying the error, so the controller reports
// Failed instead of polling a scan that will never finish.
func (w *Worker) abort(ctx context.Context, m events.Message, st *scanState, cause error) error {
	if !w.finalAttempt(m) {
		return cause
	}
	st.progress.Done = true
	st.progress.Error = cause.Error()
	if err := w.checkpoint(ctx, st, true); err != nil {
		logging.FromContext(ctx).Error("could not report the failed scan", "error", err)
	}
	return cause
}

// finalAttempt reports whether this delivery is the last one the topology
// allows, so the failure is the scan's outcome rather than a hiccup.
func (w *Worker) finalAttempt(m events.Message) bool {
	spec, ok := events.Default().Consumer(events.ConsumerImportScan)
	if !ok || spec.MaxDeliver <= 0 {
		return true
	}
	return m.Attempt() >= uint64(spec.MaxDeliver) //nolint:gosec // MaxDeliver is a small positive constant
}

// loadMovies reduces every Movie in the namespace to what MatchMovie needs.
// It is read once per walk rather than per file: the cache-backed client
// serves the List locally, and one snapshot keeps the tally's
// created-versus-updated split coherent across the walk.
func (w *Worker) loadMovies(ctx context.Context, out *[]MovieCandidate, namespace string) error {
	var movies catalogv1alpha1.MovieList
	if err := w.Client.List(ctx, &movies, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("rescan: list movies in %s: %w", namespace, err)
	}
	*out = make([]MovieCandidate, 0, len(movies.Items))
	for i := range movies.Items {
		*out = append(*out, candidateFor(&movies.Items[i]))
	}
	return nil
}

// candidateFor projects a Movie onto a MovieCandidate. Title and year come
// from status.metadata, which the metadata gateway fills in; a movie the
// gateway has not reached yet still matches by tmdb id, just not by title.
func candidateFor(m *catalogv1alpha1.Movie) MovieCandidate {
	c := MovieCandidate{Name: m.Name, TmdbID: m.Spec.TmdbID, QualityProfileRef: m.Spec.QualityProfileRef}
	if m.Status.Metadata != nil {
		c.Title = m.Status.Metadata.Title
		c.Year = int(m.Status.Metadata.Year)
		c.OriginalLanguage = m.Status.Metadata.OriginalLanguage
	}
	return c
}

// relPath renders a walked path the way catalogv1alpha1.UnmatchedFile
// documents it: relative to the ROOT FOLDER, which is not the same as the
// walked directory whenever LibraryScan.spec.subpath narrows the scan.
//
// It falls back to the absolute path when the two are unrelated, or when the
// result would climb out of the root with "..", which would be a root folder
// and a task path that do not belong together -- better an absolute path in
// status than a misleading relative one.
func relPath(root, path string) string {
	if root == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "" || rel == "." || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}
