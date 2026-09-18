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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
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

	// metricKindMovie is the bounded `kind` label on the import metrics.
	metricKindMovie = "movie"
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
}

// NewWorker builds a Worker with the production clock and timeout.
func NewWorker(c client.Client, bus events.Bus) *Worker {
	return &Worker{Client: c, Bus: bus, Clock: time.Now, MetadataTimeout: defaultMetadataTimeout}
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
	metrics.ImportUnmatchedTotal.WithLabelValues(metricKindMovie, code).Inc()
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

	var root catalogv1alpha1.RootFolder
	rootKey := types.NamespacedName{Namespace: task.RootFolderRef.Namespace, Name: task.RootFolderRef.Name}
	if err := w.Client.Get(ctx, rootKey, &root); err != nil {
		return w.abort(ctx, m, st, fmt.Errorf("rescan: get root folder %s: %w", rootKey, err))
	}
	st.root = &root

	if err := w.loadMovies(ctx, &st.movies, scan.Namespace); err != nil {
		return w.abort(ctx, m, st, err)
	}

	if err := w.walk(ctx, m, st); err != nil {
		return w.abort(ctx, m, st, err)
	}

	st.progress.Done = true
	if err := w.checkpoint(ctx, st, true); err != nil {
		// The walk itself succeeded; only the final report failed. Retry
		// so the controller is not left polling a Running scan forever.
		return events.Retry(checkpointInterval, err)
	}
	log.Info("library scan finished",
		"filesSeen", st.progress.FilesSeen,
		"filesMatched", st.progress.FilesMatched,
		"filesSkipped", st.progress.FilesSkipped,
		"itemsCreated", st.progress.ItemsCreated,
		"itemsUpdated", st.progress.ItemsUpdated,
		"unmatched", len(st.progress.Unmatched))
	return nil
}

// walk is the classify-and-dispatch loop. Every file fsops classifies as
// anything but media is skipped before it ever reaches matching, which is
// what keeps a sample, an extra or a half-downloaded .part out of the
// catalog.
func (w *Worker) walk(ctx context.Context, m events.Message, st *scanState) error {
	return fsops.Walk(ctx, st.task.Path, func(path string, info os.FileInfo, class fsops.FileClass) error {
		if err := w.beat(ctx, m, st); err != nil {
			return err
		}

		if class != fsops.ClassMedia {
			st.progress.FilesSkipped++
			return w.checkpoint(ctx, st, false)
		}
		st.progress.FilesSeen++

		if err := w.handleMediaFile(ctx, st, path, info); err != nil {
			return err
		}
		return w.checkpoint(ctx, st, false)
	})
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
	c := MovieCandidate{Name: m.Name, TmdbID: m.Spec.TmdbID}
	if m.Status.Metadata != nil {
		c.Title = m.Status.Metadata.Title
		c.Year = int(m.Status.Metadata.Year)
	}
	return c
}

// relPath renders a walked path the way catalogv1alpha1.UnmatchedFile
// documents it: relative to the root folder being walked. It falls back to
// the absolute path when the two are unrelated, which cannot happen for a
// path fsops.Walk produced but keeps the field non-empty if it ever does.
func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "" {
		return path
	}
	return rel
}
