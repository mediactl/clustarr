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
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// heartbeatInterval is how often the file loop sends an in-progress ack,
// following importarr/worker/rescan's identical reasoning: ConsumerImportFile's
// AckWait is 60s (topology.go, R6) and a multi-file hardlink-or-copy import
// can outlast it, so this worker heartbeats rather than ask for a longer
// AckWait than the worker Deployment's terminationGracePeriodSeconds allows.
const heartbeatInterval = 20 * time.Second

// FieldManager is the server-side-apply field manager this worker uses for
// the resource it creates: MediaFile. It is k8s.ManagerImportarrWorker, the
// same constant importarr/worker/rescan uses and for the same reason -- see
// that package's FieldManager doc comment for the full rationale (two
// importarr writers must never share one manager name on one object type).
//
// Download.status.import is a SEPARATE write, under the bare
// k8s.ManagerImportarr, not this constant -- see k8s.ManagerImportarr's own
// doc comment for why the two writes deliberately use different manager
// names even though both are made by this same worker.
const FieldManager = k8s.ManagerImportarrWorker

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=series;episodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=artists;albums;authors;books;audiobooks;comics;issues,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads/status,verbs=get;patch

// Worker handles clustarr.work.importarr.fileimport.<download> messages. See
// this package's doc comment for how task D2-8 registers it.
type Worker struct {
	// Client is the cache-backed client this worker reads Downloads, Movies,
	// RootFolders and QualityProfiles through, and applies MediaFiles with.
	Client client.Client

	// Bus carries the dedup fingerprint KV bucket.
	Bus events.Bus

	// Catalogue is the loaded TRaSH custom-format corpus scoring is done
	// against. Defaulted to catalogue.LoadedCatalogue() by NewWorker; a test
	// may override it.
	Catalogue *catalogue.Catalogue

	// Clock is the time source, injected so tests are deterministic.
	Clock func() time.Time

	// ProbeAudio reads a music file's codec and bitrate, the only way to
	// freeze a lossy track's quality (FrozenFileQuality). NewWorker sets
	// mediainfo.ProbeAudio; nil freezes by extension alone, as before.
	ProbeAudio AudioProber

	// SampleMaxBytes is the video size floor (fsops.IsSuspectedSample): a
	// video file smaller than this whose name does not mark it a sample is
	// a SUSPECTED sample, recorded as a rejection on status.import rather
	// than imported -- a size alone cannot tell a promo clip from a short
	// film -- unless the import is manual (DownloadSpec.Manual, or
	// AnnotationImportOverride=true), which imports it. A file whose NAME
	// marks it a sample is never imported and never reported, the
	// convention every scene release follows. Zero disables the size rule.
	// NewWorker sets fsops.DefaultSampleMaxBytes; a Worker built as a
	// literal without it has the rule off.
	SampleMaxBytes int64
}

// NewWorker builds a Worker with the production catalogue, clock and sample
// threshold.
func NewWorker(c client.Client, bus events.Bus) *Worker {
	return &Worker{
		Client: c, Bus: bus, Catalogue: catalogue.LoadedCatalogue(), Clock: time.Now,
		ProbeAudio: mediainfo.ProbeAudio, SampleMaxBytes: fsops.DefaultSampleMaxBytes,
	}
}

func (w *Worker) now() time.Time {
	if w.Clock != nil {
		return w.Clock()
	}
	return time.Now()
}

// Handle implements events.Handler.
func (w *Worker) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	// Extract before Start, so this span continues the trace of whatever
	// published the ImportTask rather than beginning a new one, matching
	// catalogarr/worker/grab's pattern.
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "fileimport.Worker.Handle")
	defer span.End()

	if env == nil {
		return events.Discard("import task has no envelope", errors.New("fileimport: nil envelope"))
	}

	var task schema.ImportTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("fileimport: undecodable ImportTask", err)
	}

	ns, keyName, ok := strings.Cut(env.Key, "/")
	if !ok || ns == "" {
		return events.Discard("fileimport: envelope key is not <namespace>/<name>",
			fmt.Errorf("key=%q", env.Key))
	}
	name := keyName
	if name == "" {
		name = task.DownloadRef.Name
	}
	if name == "" {
		return events.Discard("fileimport: no download name in envelope key or task",
			fmt.Errorf("key=%q downloadRef=%+v", env.Key, task.DownloadRef))
	}

	log := logging.FromContext(ctx).With("namespace", ns, "download", name)
	ctx = logging.NewContext(ctx, log)

	// Dedup fast path: a Download already imported, and possibly already
	// deleted by grabarr's RemoveOnImport, must not be reprocessed just
	// because this delivery is a redelivery or a duplicate publish. See
	// dedup.go's doc comment.
	if task.DownloadRef.UID != "" {
		done, err := alreadyImported(ctx, w.Bus.KV(events.BucketDedup), task.DownloadRef.UID)
		if err != nil {
			return fmt.Errorf("fileimport: check dedup fingerprint: %w", err)
		}
		if done {
			log.Debug("fileimport: download already imported; redelivery is a no-op")
			return nil
		}
	}

	var dl downloadv1alpha1.Download
	if err := w.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &dl); err != nil {
		if apierrors.IsNotFound(err) {
			return events.Discard("fileimport: download no longer exists", err)
		}
		return fmt.Errorf("fileimport: get download %s/%s: %w", ns, name, err)
	}
	if task.DownloadRef.UID != "" && string(dl.UID) != task.DownloadRef.UID {
		return events.Discard("fileimport: download was replaced", fmt.Errorf(
			"fileimport: download %s/%s has uid %s, task was published for %s",
			ns, name, dl.UID, task.DownloadRef.UID))
	}

	if dl.Status.Import != nil && dl.Status.Import.State == downloadv1alpha1.ImportPhaseImported {
		log.Debug("fileimport: status.import already reports imported; redelivery is a no-op")
		return nil
	}

	switch dl.Status.Phase {
	case downloadv1alpha1.DownloadPhaseCompleted, downloadv1alpha1.DownloadPhaseSeeding:
		// ready to import
	default:
		log.Info("fileimport: download is not in an importable phase; a fresh task will follow when it is",
			"phase", dl.Status.Phase)
		return nil
	}

	// The import annotations are read before anything else is decided: a
	// malformed one is a user instruction this worker cannot follow, and
	// importing to spec.target instead would be guessing what they meant.
	// It is reported on status.import, which this worker owns, and the
	// Download stays importable once the annotation is fixed (Retrigger).
	dirs, derr := readDirectives(dl.Annotations)
	if derr != nil {
		return w.finishBlocked(ctx, &dl, nil, nil, "invalid annotation: "+derr.Error())
	}
	manual := dl.Spec.Manual || dirs.override
	target := targetFromSpec(dl.Spec.Target)
	if dirs.target != nil {
		target = *dirs.target
	}
	ref := target.FileRef()

	switch {
	case ref.Kind == commonv1.MediaKindMovie:
		// handled below
	case IsNonVideoFileKind(ref.Kind):
		return w.importNonVideo(ctx, m, &dl, target, manual)
	case ref.Kind == commonv1.MediaKindSeries || ref.Kind == commonv1.MediaKindEpisode:
		return w.importEpisodes(ctx, m, &dl, target, manual)
	default:
		// An artist, author, or comic without an issue key: a container
		// whose files belong to one of its children, and choosing which
		// is the guess this worker does not make.
		return w.finishBlocked(ctx, &dl, nil, nil, fmt.Sprintf(
			"target %s is a %s, which holds no files itself; set %s to the album, book or issue "+
				"(comic/<comic>/<issue>) the files belong to", target, ref.Kind, AnnotationImportTarget))
	}

	var movie catalogv1alpha1.Movie
	if err := w.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &movie); err != nil {
		if apierrors.IsNotFound(err) {
			return w.finishBlocked(ctx, &dl, nil, nil, fmt.Sprintf("movie %q does not exist", ref.Name))
		}
		return fmt.Errorf("fileimport: get movie %s/%s: %w", ns, ref.Name, err)
	}
	if movie.Status.Metadata == nil {
		return fmt.Errorf("fileimport: movie %s/%s has no metadata yet", ns, movie.Name)
	}

	var rootFolder catalogv1alpha1.RootFolder
	if err := w.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: movie.Spec.RootFolderRef}, &rootFolder); err != nil {
		return fmt.Errorf("fileimport: get root folder %s/%s: %w", ns, movie.Spec.RootFolderRef, err)
	}

	profile, err := w.resolveProfile(ctx, dl.Spec.QualityProfileRef, movie.Spec.QualityProfileRef)
	if err != nil {
		return err
	}

	if dl.Status.ContentRoot == "" {
		return fmt.Errorf("fileimport: download %s/%s has no status.contentRoot yet", ns, name)
	}
	if _, err := statDir(dl.Status.ContentRoot); err != nil {
		if w.finalAttempt(m) {
			return w.finishBlocked(ctx, &dl, nil, nil,
				fmt.Sprintf("content root %q is not accessible: %v", dl.Status.ContentRoot, err))
		}
		return fmt.Errorf("fileimport: stat content root %s: %w", dl.Status.ContentRoot, err)
	}

	existing, err := w.existingMediaFile(ctx, ns, ref)
	if err != nil {
		return err
	}

	eng := naming.NewEngine(naming.Config{
		Dialect:           naming.Dialect(rootFolder.Spec.Naming.Dialect),
		ColonReplacement:  naming.ColonReplacement(rootFolder.Spec.Naming.ColonReplacement),
		MultiEpisodeStyle: naming.MultiEpisodeStyle(rootFolder.Spec.Naming.MultiEpisodeStyle),
		Overrides:         rootFolder.Spec.Naming.Overrides,
	})
	base := naming.Context{
		Kind:          commonv1.MediaKindMovie,
		Title:         movie.Status.Metadata.Title,
		OriginalTitle: movie.Status.Metadata.Title,
		Year:          int(movie.Status.Metadata.Year),
		TmdbID:        strconv.FormatInt(movie.Spec.TmdbID, 10),
		ImdbID:        movie.Status.Metadata.ExternalIDs["imdb"],
	}
	originalLanguageName := ""
	if movie.Status.Metadata.OriginalLanguage != "" {
		if n, ok := catalogue.LanguageName(movie.Status.Metadata.OriginalLanguage); ok {
			originalLanguageName = n
		}
	}

	pc := processConfig{
		worker:               w,
		message:              m,
		download:             &dl,
		target:               ref,
		manual:               manual,
		movie:                &movie,
		rootFolder:           &rootFolder,
		profile:              profile,
		existing:             existing,
		engine:               eng,
		baseContext:          base,
		originalLanguageName: originalLanguageName,
	}

	outcome, walkErr := pc.run(ctx)
	if walkErr != nil {
		if w.finalAttempt(m) {
			return w.finishBlocked(ctx, &dl, outcome.imported, outcome.rejections, walkErr.Error())
		}
		return fmt.Errorf("fileimport: import %s/%s: %w", ns, name, walkErr)
	}

	if len(outcome.imported) == 0 {
		msg := "no importable files found"
		if len(outcome.rejections) > 0 {
			msg = downloadv1alpha1.ImportMessageEveryFileRejected
		}
		return w.finishBlocked(ctx, &dl, outcome.imported, outcome.rejections, msg)
	}
	return w.finishImported(ctx, &dl, outcome.imported, outcome.rejections)
}

// resolveProfile loads the QualityProfile the import re-checks finished
// files against: the Download's own qualityProfileRef when set ("the
// QualityProfile the grab decision used; the importer re-checks the
// finished files against it", DownloadSpec.QualityProfileRef's doc comment),
// falling back to the movie's when the Download carries none.
func (w *Worker) resolveProfile(ctx context.Context, downloadRef, movieRef string) (quality.Profile, error) {
	ref := downloadRef
	if ref == "" {
		ref = movieRef
	}
	if ref == "" {
		return quality.Profile{}, events.Discard("fileimport: no qualityProfileRef on the download or its target",
			errors.New("cannot score an import without a profile"))
	}
	var qp catalogv1alpha1.QualityProfile
	if err := w.Client.Get(ctx, client.ObjectKey{Name: ref}, &qp); err != nil {
		return quality.Profile{}, fmt.Errorf("fileimport: get quality profile %s: %w", ref, err)
	}
	profile, ferrs := quality.FromCRD(&qp, w.Catalogue)
	if len(ferrs) > 0 {
		return quality.Profile{}, events.Discard("fileimport: invalid quality profile", errors.Join(ferrs...))
	}
	return profile, nil
}

// existingMediaFile looks the Download's target up through the field index.
func (w *Worker) existingMediaFile(ctx context.Context, namespace string, target commonv1.MediaRef) (*catalogv1alpha1.MediaFile, error) {
	var list catalogv1alpha1.MediaFileList
	if err := w.Client.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingFields{MediaFileByTargetIndexKey: targetKey(string(target.Kind), target.Name)},
	); err != nil {
		return nil, fmt.Errorf("fileimport: look up existing media file for %s/%s: %w", target.Kind, target.Name, err)
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	return &list.Items[0], nil
}

// applyMediaFile creates or re-asserts the MediaFile for one imported file,
// under [FieldManager]. It writes MediaFileSpec only, per CLAUDE.md's
// invariant: nothing here touches MediaFileStatus.
func (w *Worker) applyMediaFile(ctx context.Context, name string, spec *catalogac.MediaFileSpecApplyConfiguration, namespace string) error {
	if _, err := k8s.Apply(ctx, w.Client, FieldManager,
		catalogac.MediaFile(name, namespace).WithSpec(spec)); err != nil {
		return fmt.Errorf("fileimport: apply media file %s: %w", name, err)
	}
	return nil
}

// finalAttempt reports whether this delivery is the last one
// ConsumerImportFile's topology allows.
func (w *Worker) finalAttempt(m events.Message) bool {
	spec, ok := events.Default().Consumer(events.ConsumerImportFile)
	if !ok || spec.MaxDeliver <= 0 {
		return true
	}
	return m.Attempt() >= uint64(spec.MaxDeliver) //nolint:gosec // MaxDeliver is a small positive constant
}

// finishBlocked patches status.import to Blocked: the import could not
// complete, but the Download and any target it names still exist and a
// future attempt (a manual retry, or the underlying cause being fixed)
// could succeed.
func (w *Worker) finishBlocked(
	ctx context.Context, dl *downloadv1alpha1.Download, imported []*downloadac.ImportedFileApplyConfiguration,
	rejections []string, message string,
) error {
	listed, _ := capImported(imported)
	ac := downloadac.ImportState().
		WithState(downloadv1alpha1.ImportPhaseBlocked).
		WithMessage(truncateChars(message, maxImportMessage))
	if len(listed) > 0 {
		ac = ac.WithImported(listed...)
	}
	if len(rejections) > 0 {
		ac = ac.WithRejections(capRejections(rejections)...)
	}
	return w.patchImport(ctx, dl, ac, nil)
}

// finishImported patches status.import to Imported and records the dedup
// fingerprint.
func (w *Worker) finishImported(
	ctx context.Context, dl *downloadv1alpha1.Download, imported []*downloadac.ImportedFileApplyConfiguration,
	rejections []string,
) error {
	listed, unlisted := capImported(imported)
	ac := downloadac.ImportState().
		WithState(downloadv1alpha1.ImportPhaseImported).
		WithImportedAt(metav1.NewTime(w.now())).
		WithImported(listed...)
	if unlisted > 0 {
		ac = ac.WithMessage(fmt.Sprintf("imported %d files; status lists the first %d", len(imported), len(listed)))
	}
	if len(rejections) > 0 {
		ac = ac.WithRejections(capRejections(rejections)...)
	}
	// The fingerprint covers every imported file, not only the listed ones.
	refs := make([]string, 0, len(imported))
	for _, i := range imported {
		if i.MediaFileRef != nil {
			refs = append(refs, *i.MediaFileRef)
		}
	}
	return w.patchImport(ctx, dl, ac, refs)
}

// patchImport applies ac as the complete declaration of status.import under
// k8s.ManagerImportarr -- this worker's only owned field on Download.status
// (see k8s.ManagerImportarr's doc comment), so every call is naturally a
// complete declaration; there is no sibling field on this manager's set that
// an earlier call could have sent and this one must remember to repeat.
//
// dl is re-Read immediately before the apply, per the lost-update hazard:
// this Handle call may have spent real time walking and copying files since
// dl was first read, and a status.Apply seeded from that stale read would
// not itself roll back another writer's field (this manager owns only
// status.import), but a Download deleted or replaced in the meantime must
// not receive a phantom write.
func (w *Worker) patchImport(
	ctx context.Context, dl *downloadv1alpha1.Download, ac *downloadac.ImportStateApplyConfiguration, mediaFileRefs []string,
) error {
	var fresh downloadv1alpha1.Download
	if err := w.Client.Get(ctx, types.NamespacedName{Namespace: dl.Namespace, Name: dl.Name}, &fresh); err != nil {
		if apierrors.IsNotFound(err) {
			// The work already happened (any MediaFiles this call created
			// are already applied); there is simply nobody left to report
			// the outcome to. Not an error -- record the dedup fingerprint
			// so a stray redelivery still no-ops, and move on.
			w.recordDedup(ctx, dl, mediaFileRefs)
			return nil
		}
		return fmt.Errorf("fileimport: re-get download %s/%s before status patch: %w", dl.Namespace, dl.Name, err)
	}

	if _, err := k8s.PatchStatus(ctx, w.Client, k8s.ManagerImportarr,
		downloadac.Download(fresh.Name, fresh.Namespace).WithStatus(downloadac.DownloadStatus().WithImport(ac))); err != nil {
		return fmt.Errorf("fileimport: patch download status.import: %w", err)
	}

	if ac.State != nil && *ac.State == downloadv1alpha1.ImportPhaseImported {
		w.recordDedup(ctx, &fresh, mediaFileRefs)
	}
	return nil
}

// recordDedup best-effort records the dedup fingerprint; see dedup.go.
func (w *Worker) recordDedup(ctx context.Context, dl *downloadv1alpha1.Download, mediaFileRefs []string) {
	if dl.UID == "" {
		return
	}
	if err := recordImport(ctx, w.Bus.KV(events.BucketDedup), string(dl.UID), mediaFileRefs); err != nil {
		logging.FromContext(ctx).Warn("fileimport: could not record dedup fingerprint", "error", err)
	}
}

// beat extends the delivery's ack deadline when heartbeatInterval has
// elapsed, mirroring importarr/worker/rescan.Worker.beat.
func (w *Worker) beat(ctx context.Context, m events.Message, last *time.Time) error {
	now := w.now()
	if !last.IsZero() && now.Sub(*last) < heartbeatInterval {
		return nil
	}
	*last = now
	if err := m.InProgress(ctx); err != nil {
		return fmt.Errorf("fileimport: heartbeat: %w", err)
	}
	return nil
}

// statDir reports whether dir exists and is a directory.
func statDir(dir string) (bool, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return false, err
	}
	if !fi.IsDir() {
		return false, fmt.Errorf("fileimport: %s is not a directory", dir)
	}
	return true, nil
}
