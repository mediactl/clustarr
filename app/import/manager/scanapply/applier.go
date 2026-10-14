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

package scanapply

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/app/import/mediafilespec/specwrite"
	"github.com/mediactl/clustarr/app/import/scanprogress"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// Unmatched reason codes the applier lists (closed, label-safe). The first
// four are the rescan's own codes, now decided here.
const (
	CodeExcluded          = "excluded"
	CodeNoQualityProfile  = "no_default_quality_profile"
	CodeRecordedElsewhere = "recorded_elsewhere"
	// CodeNameCollision: the item the attribution names exists with
	// another provider id; it is never overwritten (ADR-0019 §7.6).
	CodeNameCollision = "name_collision"
	// CodeNoItem: the attributed item no longer exists.
	CodeNoItem = "no_item"
	// CodeManualTarget: a person's manual target no longer fits (P45).
	CodeManualTarget = "manual_target_invalid"
	// CodeOtherRootFolder: the item the file names is stored under another
	// root folder (smoke finding O4): a scan attributes a file only to an
	// item of the root folder it scans, never guessing which item a file
	// outside its item's root folder belongs to.
	CodeOtherRootFolder = "other_root_folder"
)

// otherRootFolder lists a file whose attributed item is stored under
// itemRoot, another root folder than the scan's.
func (a *Applier) otherRootFolder(t *tally, root *catalogv1alpha1.RootFolder, rel string, ref commonv1.MediaRef, itemRoot string) {
	t.unmatched(root, rel, CodeOtherRootFolder, fmt.Sprintf(
		"the file names %s %s, which is stored under root folder %q, not %q: a scan attributes a file only to "+
			"an item of the root folder it scans; move the file under %q to import it",
		ref.Kind, ref.Name, itemRoot, root.Name, itemRoot), []string{ref.Name}, a.now())
}

// errMalformed marks an observation the applier cannot act on: the intake
// dead-letters it (not transient).
var errMalformed = errors.New("scanapply: malformed observation")

// Applier implements intake.ScanApplier.
type Applier struct {
	// Client writes: MediaFile spec and annotation, items, deletes, the
	// re-observing LibraryScan.
	Client client.Client
	// Reader is the manager's cache: root folders, items, media files.
	Reader client.Reader
	// APIReader reads straight from the apiserver immediately before a
	// write decided on it (CLAUDE.md's lost-update rule): the MediaFile a
	// basis names, an item a create would overwrite, a record to delete.
	APIReader client.Reader
	// Bus carries the exclusion lookups, the applier's tally and the
	// recycle-files task for orphan parts.
	Bus events.Bus
	// Limiter paces creates, updates and deletes (the remediation loop's
	// remediation.WriteLimiter, 25/s); nil writes unpaced.
	Limiter *remediation.WriteLimiter
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

func (a *Applier) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Applier) apiReader() client.Reader {
	if a.APIReader != nil {
		return a.APIReader
	}
	return a.Client
}

func (a *Applier) reader() client.Reader {
	if a.Reader != nil {
		return a.Reader
	}
	return a.Client
}

// Apply decides one observation and writes (intake.ScanApplier). A refusal
// is an outcome, listed as unmatched, not an error; an error is the
// apiserver's or the bus's, which the intake retries when transient.
func (a *Applier) Apply(ctx context.Context, obs schema.ScanObservation) error {
	if obs.Scan.Namespace == "" || obs.Scan.UID == "" || obs.RootFolder.Name == "" || obs.Path == "" {
		return fmt.Errorf("%w: scan %q, root folder %q, path %q", errMalformed, obs.Scan.String(), obs.RootFolder.Name, obs.Path)
	}
	ctx = logging.NewContext(ctx, logging.FromContext(ctx).With(
		"libraryScan", obs.Scan.String(), "rootFolder", obs.RootFolder.Name, "kind", obs.Kind))
	t := &tally{}
	var err error
	switch obs.Kind {
	case schema.ScanObservationPresent:
		err = a.present(ctx, obs, t)
	case schema.ScanObservationMissing:
		err = a.missing(ctx, obs, t)
	case schema.ScanObservationOrphanPart:
		err = a.orphanPart(ctx, obs, t)
	case schema.ScanObservationUnmatched:
		// The walk's tally already lists it (ADR-0019 §7.6).
		return nil
	default:
		return fmt.Errorf("%w: kind %q", errMalformed, obs.Kind)
	}
	if err != nil {
		return err
	}
	a.record(ctx, obs.Scan.UID, t)
	return nil
}

// rootFolder reads the observation's RootFolder from the cache; nil when it
// is gone, in which case nothing under it is applied.
func (a *Applier) rootFolder(ctx context.Context, obs schema.ScanObservation) (*catalogv1alpha1.RootFolder, error) {
	var root catalogv1alpha1.RootFolder
	err := a.reader().Get(ctx, types.NamespacedName{Namespace: obs.Scan.Namespace, Name: obs.RootFolder.Name}, &root)
	switch {
	case apierrors.IsNotFound(err):
		logging.FromContext(ctx).Info("scanapply: the root folder is gone; nothing applied", "path", obs.Path)
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("scanapply: get root folder %s: %w", obs.RootFolder.Name, err)
	}
	return &root, nil
}

// present decides a file the scan found (see the package doc).
func (a *Applier) present(ctx context.Context, obs schema.ScanObservation, t *tally) error {
	if obs.Attribution == nil {
		if obs.TranscodeOutputOf == "" {
			return fmt.Errorf("%w: a present observation of %s with no attribution", errMalformed, obs.Path)
		}
		// P44: a transcode's output is the transcoded item's; adopting it
		// would make a second MediaFile, an unmatched entry or a new Movie.
		t.TranscodeOutputs++
		t.FilesSkipped++
		return nil
	}
	root, err := a.rootFolder(ctx, obs)
	if err != nil || root == nil {
		return err
	}
	rel := relPath(root.Spec.Path, obs.Path)
	ref := refOf(obs.Attribution)
	if obs.Attribution.Create != nil && obs.Basis == nil {
		// The item a create names is named here, never by the agent.
		ref.Name = createName(obs.Attribution.Create)
	}

	existing, stale, err := a.basis(ctx, obs, ref)
	if err != nil {
		return err
	}
	if stale {
		return a.reobserve(ctx, obs, root, t)
	}
	if existing != nil && (existing.Spec.MediaRef.Kind != ref.Kind || existing.Spec.MediaRef.Name != ref.Name) {
		// P41: spec.mediaRef is immutable; a path is never re-pointed.
		t.unmatched(root, rel, CodeRecordedElsewhere, fmt.Sprintf(
			"attributed to %s/%s, but MediaFile %s already records this path against %s/%s; spec.mediaRef is "+
				"immutable, so delete that MediaFile to re-attribute the file",
			ref.Kind, ref.Name, existing.Name, existing.Spec.MediaRef.Kind, existing.Spec.MediaRef.Name),
			[]string{existing.Spec.MediaRef.Name}, a.now())
		return nil
	}
	if existing != nil && existing.Spec.Original != nil && !*existing.Spec.Original {
		return a.handOver(ctx, existing, obs, t)
	}
	if obs.Manual {
		if reason, err := a.validateManual(ctx, obs.Scan.Namespace, root, ref); err != nil {
			return err
		} else if reason != "" {
			t.unmatched(root, rel, CodeManualTarget, reason, nil, a.now())
			return nil
		}
	}

	created := false
	if c := obs.Attribution.Create; c != nil && existing == nil {
		ok, made, err := a.ensureItem(ctx, obs, root, c, rel, t)
		if err != nil || !ok {
			return err
		}
		created = made
		if ref.Kind == commonv1.MediaKindSeries {
			// A new series has no episodes yet: its files await them (the
			// walk counted it), and the scan of its folder its first
			// episodes ask for attributes them (folderscan, smoke O8).
			return nil
		}
	} else if existing == nil {
		itemRoot, ok, err := itemRootFolder(ctx, a.reader(), obs.Scan.Namespace, ref)
		if err != nil {
			return err
		}
		if !ok {
			t.unmatched(root, rel, CodeNoItem, fmt.Sprintf("attributed to %s/%s, which no longer exists", ref.Kind, ref.Name), nil, a.now())
			return nil
		}
		if itemRoot != root.Name {
			// O4: never a new MediaFile outside its item's root folder.
			a.otherRootFolder(t, root, rel, ref, itemRoot)
			return nil
		}
	}
	return a.recordFile(ctx, obs, root, existing, ref, created, t)
}

// basis reads the MediaFile the observation was made against, fresh: the
// one its basis names, or the one a new file's name would take. stale is
// true when another write moved it since the agent read it -- deleted, a
// new object under the name, a spec that no longer hashes to the basis --
// unless what is stored is already what this observation would write (a
// redelivery after the write landed).
func (a *Applier) basis(
	ctx context.Context, obs schema.ScanObservation, ref commonv1.MediaRef,
) (existing *catalogv1alpha1.MediaFile, stale bool, err error) {
	ns := obs.Scan.Namespace
	name := k8s.ChildName(ref.Name, "mediafile", obs.Path)
	if b := obs.Basis; b != nil {
		name = b.Name
	}
	var mf catalogv1alpha1.MediaFile
	switch err := a.apiReader().Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &mf); {
	case apierrors.IsNotFound(err):
		return nil, obs.Basis != nil, nil
	case err != nil:
		return nil, false, fmt.Errorf("scanapply: get media file %s: %w", name, err)
	}
	if k8s.IsDeleting(&mf) {
		return nil, true, nil
	}
	if b := obs.Basis; b != nil && string(mf.UID) == b.UID &&
		(mediafilespec.SpecHash(mf.Spec) == b.SpecHash || applied(&mf, obs)) {
		return &mf, false, nil
	}
	if obs.Basis == nil && applied(&mf, obs) {
		return &mf, false, nil
	}
	return nil, true, nil
}

// applied reports whether mf already records obs: its path, size and mtime.
func applied(mf *catalogv1alpha1.MediaFile, obs schema.ScanObservation) bool {
	return filepath.Clean(mf.Spec.Path) == filepath.Clean(obs.Path) && mf.Spec.SizeBytes == obs.SizeBytes &&
		mf.Spec.ModTime.UTC().Truncate(time.Second).Equal(obs.ModTime.UTC().Truncate(time.Second))
}

// recordFile applies the MediaFile spec under import-worker
// (specwrite.ApplyFacts, the one render of that manager's set): a new
// file's frozen fields from the observation, an existing one's re-asserted
// verbatim, with the observed size and mtime, carrying the resourceVersion
// read so a write landing in between is refused, never reverted.
func (a *Applier) recordFile(
	ctx context.Context, obs schema.ScanObservation, root *catalogv1alpha1.RootFolder,
	existing *catalogv1alpha1.MediaFile, ref commonv1.MediaRef, created bool, t *tally,
) error {
	ns := obs.Scan.Namespace
	if existing != nil && applied(existing, obs) {
		// A full scan's unchanged file, or a redelivery after the write
		// landed: re-asserting the same set would change nothing.
		t.FilesMatched++
		t.ItemsUpdated++
		return nil
	}
	name, rv := k8s.ChildName(ref.Name, "mediafile", obs.Path), ""
	var frozen mediafilespec.Frozen
	if existing != nil {
		name, rv = existing.Name, existing.ResourceVersion
		ref = existing.Spec.MediaRef
		frozen = mediafilespec.ReassertFrozen(&existing.Spec)
	} else {
		frozen = mediafilespec.FrozenFromScan(obs.Frozen, obs.Manual, a.now())
		if frozen.Track != "" {
			ref.Track = frozen.Track
		}
	}
	if err := a.pace(ctx, types.UID(ns+"/"+name)); err != nil {
		return err
	}
	if _, err := specwrite.ApplyFacts(ctx, a.Client, ns, name, rv, ref, obs.Path, obs.SizeBytes, obs.ModTime, frozen); err != nil {
		var staleRead *mediafilespec.StaleReadError
		if errors.As(err, &staleRead) {
			return a.reobserve(ctx, obs, root, t)
		}
		return err
	}
	t.FilesMatched++
	if !created {
		t.ItemsUpdated++
	}
	logging.FromContext(ctx).Debug("scanapply: recorded a scanned file", "mediaFile", name, "item", ref.Name)
	return nil
}

// handOver tells catalog that a post-transcode file changed on disk (P42,
// W17): one observed-fingerprint annotation apply under k8s.ManagerImport,
// which owns nothing else on a MediaFile, skipped when the annotation
// already records the observation. catalog owns the size, mtime and
// original flag of such a file, so nothing else is written.
func (a *Applier) handOver(ctx context.Context, existing *catalogv1alpha1.MediaFile, obs schema.ScanObservation, t *tally) error {
	fp := mediafilespec.ObservedFingerprint(obs.SizeBytes, obs.ModTime)
	if existing.Annotations[mediafilespec.AnnotationObservedFingerprint] != fp {
		if err := a.pace(ctx, existing.UID); err != nil {
			return err
		}
		ac := catalogac.MediaFile(existing.Name, existing.Namespace).
			WithAnnotations(map[string]string{mediafilespec.AnnotationObservedFingerprint: fp})
		if _, err := k8s.Apply(ctx, a.Client, k8s.ManagerImport, ac); err != nil {
			return fmt.Errorf("scanapply: hand media file %s over to catalog: %w", existing.Name, err)
		}
	}
	t.HandedOver++
	t.FilesMatched++
	t.ItemsUpdated++
	logging.FromContext(ctx).Info("scanapply: a transcoded file changed on disk; handed to catalog",
		"mediaFile", existing.Name, "observed", fp)
	return nil
}

// pace waits for the write limiter's slot for key; it never fails a write
// for want of one (A6.1: creates and deletes wait, they are not refused).
func (a *Applier) pace(ctx context.Context, key types.UID) error {
	for {
		d := a.Limiter.Admit(key, a.now())
		if d <= 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
}

// refOf is the MediaRef an attribution names.
func refOf(at *schema.ScanAttribution) commonv1.MediaRef {
	return commonv1.MediaRef{Kind: commonv1.MediaKind(at.Kind), Name: at.Name, Keys: at.Keys, Track: at.Track}
}

// relPath is path relative to the root folder, as UnmatchedFile.Path
// documents it; the absolute path when the two are unrelated.
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

// tally is one observation's outcome, added to the scan's applied tally.
type tally struct{ scanprogress.Progress }

// unmatched lists a file the applier refused, and counts it by root kind and
// code (clustarr_import_unmatched_total).
func (t *tally) unmatched(root *catalogv1alpha1.RootFolder, rel, code, reason string, candidates []string, at time.Time) {
	t.Unmatched = append(t.Unmatched, scanprogress.UnmatchedFile{Path: rel, Reason: reason, Candidates: candidates, SeenAt: at})
	kind := ""
	if root != nil {
		kind = string(root.Spec.Kind)
	}
	metrics.ImportUnmatchedTotal.WithLabelValues(kind, code).Inc()
}

// tallyAttempts bounds the compare-and-swap of the applied tally.
const tallyAttempts = 5

// record adds t to the scan's applied tally (scanprogress.AppliedKey) by
// compare-and-swap. It is a report, never a reason to redo a write: a tally
// that cannot be written is logged and lost, rather than retried by the
// intake and counted twice.
func (a *Applier) record(ctx context.Context, scanUID string, t *tally) {
	if a.Bus == nil || t.zero() {
		return
	}
	kv := a.Bus.KV(events.BucketProgress)
	key := scanprogress.AppliedKey(scanUID)
	for range tallyAttempts {
		var (
			cur scanprogress.Progress
			rev uint64
		)
		e, err := kv.Get(ctx, key)
		switch {
		case errors.Is(err, events.ErrKeyNotFound):
		case err != nil:
			logging.FromContext(ctx).Warn("scanapply: could not read the scan's applied tally", "error", err)
			return
		default:
			rev = e.Revision
			if cur, err = scanprogress.DecodeProgress(e.Value); err != nil {
				cur = scanprogress.Progress{}
			}
		}
		data, err := cur.Merge(t.Progress).Encode()
		if err != nil {
			return
		}
		if rev == 0 {
			_, err = kv.Create(ctx, key, data)
		} else {
			_, err = kv.Update(ctx, key, data, rev)
		}
		switch {
		case err == nil:
			return
		case errors.Is(err, events.ErrKeyExists), errors.Is(err, events.ErrRevisionMismatch):
			continue
		default:
			logging.FromContext(ctx).Warn("scanapply: could not write the scan's applied tally", "error", err)
			return
		}
	}
	logging.FromContext(ctx).Warn("scanapply: the scan's applied tally kept changing; this outcome is not counted")
}

// zero reports whether t counts nothing.
func (t *tally) zero() bool {
	p := t.Progress
	return p.FilesMatched == 0 && p.ItemsCreated == 0 && p.ItemsUpdated == 0 && p.FilesSkipped == 0 &&
		p.Deferred == 0 && p.TranscodeOutputs == 0 && p.HandedOver == 0 && p.FilesRemoved == 0 &&
		len(p.Unmatched) == 0
}

// ReobserveWindow bounds re-observation: at most one LibraryScan per folder
// per window, named by it, so a burst of stale observations from one folder
// asks for one walk.
const ReobserveWindow = 10 * time.Minute

// reobserve answers a stale basis (ADR-0019 §7.6): nothing is applied over
// a MediaFile another write moved since the agent read it. The file is
// counted Deferred, and its folder is walked again by a LibraryScan of its
// own (incremental, the root folder's label, the CRD's TTL), created once
// per folder per ReobserveWindow: AlreadyExists is the dedupe. A folder that
// is the root folder itself re-observes the one file.
func (a *Applier) reobserve(ctx context.Context, obs schema.ScanObservation, root *catalogv1alpha1.RootFolder, t *tally) error {
	t.Deferred++
	t.FilesSkipped++
	sub := relPath(root.Spec.Path, filepath.Dir(obs.Path))
	if sub == filepath.Dir(obs.Path) || sub == "" {
		sub = relPath(root.Spec.Path, obs.Path)
	}
	slot := a.now().UTC().Truncate(ReobserveWindow).Format("20060102T1504")
	scan := &catalogv1alpha1.LibraryScan{}
	scan.Name = k8s.ChildName(root.Name, "reobserve", sub, slot)
	scan.Namespace = root.Namespace
	scan.Labels = map[string]string{labelRootFolder: root.Name}
	scan.Spec = catalogv1alpha1.LibraryScanSpec{
		RootFolderRef: root.Name, Subpath: sub, Mode: catalogv1alpha1.ScanModeIncremental,
	}
	if err := a.pace(ctx, types.UID(root.Namespace+"/"+scan.Name)); err != nil {
		return err
	}
	err := a.Client.Create(ctx, scan, client.FieldOwner(k8s.ManagerImport.String()))
	switch {
	case err == nil:
		logging.FromContext(ctx).Info("scanapply: a scanned file's MediaFile moved since the scan read it; re-observing its folder",
			"path", obs.Path, "libraryScan", scan.Name)
	case apierrors.IsAlreadyExists(err):
	default:
		return fmt.Errorf("scanapply: create library scan %s: %w", scan.Name, err)
	}
	return nil
}

// labelRootFolder is rootfolderschedule.LabelRootFolder: the label every
// LibraryScan of a root folder carries.
const labelRootFolder = "catalog.clustarr.io/root-folder"
