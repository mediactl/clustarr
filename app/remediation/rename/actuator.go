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

package rename

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Event reasons. A refusal RenameFile reports is recorded under its
// mediafilespec.Rename* reason.
const (
	// ReasonRenamed: the file was moved to its canonical path.
	ReasonRenamed = "Renamed"
	// ReasonScanInProgress: the rename is held while a LibraryScan walks
	// the file's folder (spec D6).
	ReasonScanInProgress = "ScanInProgress"
)

// recheckAfter is how long a held file waits before it is tried again: a
// file that changed on disk, which the rescan has usually re-observed by
// then, or one under a scan still in progress.
const recheckAfter = time.Minute

// Options configures the actuator.
type Options struct {
	Recorder k8sevents.EventRecorder // recorder "rename"
}

// Actuator is the rename actuator.
type Actuator struct {
	o       Options
	mu      sync.Mutex
	refused map[types.UID]string // "<spec.path>|<expectedPath>" of a proposal that ended in a collision or a hold
}

// New is the rename actuator.
func New(o Options) *Actuator { return &Actuator{o: o, refused: map[types.UID]string{}} }

// Name implements remediation.Actuator.
func (*Actuator) Name() string { return "rename" }

// Act renames applied's file when it is renameable (mediafilespec.Renameable),
// its RootFolder sets renameFiles (or renameTranscoded for a file squasharr
// transcoded, in its own folder), no LibraryScan walks its folder, and this
// proposal was not refused before. A scan in progress or a changed file asks
// for a pass in a minute.
func (a *Actuator) Act(ctx context.Context, env *remediation.Env, c client.Client, applied *catalogv1alpha1.MediaFile) (time.Duration, error) {
	if k8s.IsDeleting(applied) || !mediafilespec.Renameable(applied) || inFlight(applied) {
		a.forget(applied)
		return 0, nil
	}
	if a.refusedBefore(applied) {
		return 0, nil
	}
	roots, mode, err := a.renameMode(ctx, env.Reader, applied)
	if err != nil || mode == renameOff {
		return 0, err
	}
	scan, err := a.openScanOver(ctx, env.Reader, applied, roots)
	if err != nil {
		return 0, err
	}
	if scan != "" {
		// A scan walking the folder could see the vacated path mid-move and
		// re-attribute it, re-applying the frozen fields of a MediaFile the
		// rename is rewriting (spec D6). Wait for it to finish.
		a.event(applied, corev1.EventTypeNormal, ReasonScanInProgress, "not renamed yet: LibraryScan %s is walking the file's folder", scan)
		return recheckAfter, nil
	}
	out, err := mediafilespec.RenameFile(ctx, c, env.APIReader, applied, false, mode == renameInFolder)
	if err != nil {
		return 0, err
	}
	switch {
	case out.Moved:
		a.event(applied, corev1.EventTypeNormal, ReasonRenamed, "renamed %s to %s", out.From, out.To)
		logging.FromContext(ctx).Info("renamed a library file to its canonical path", "from", out.From, "to", out.To)
	case out.Reason == mediafilespec.RenameCollision:
		a.event(applied, corev1.EventTypeWarning, out.Reason, "not renamed to %s: something already exists there", out.To)
		a.remember(applied)
	case out.Reason == mediafilespec.RenameChanged:
		a.event(applied, corev1.EventTypeWarning, out.Reason,
			"not renamed to %s: the file changed since it was recorded; retrying once it is re-observed", out.To)
		return recheckAfter, nil
	case out.Reason == mediafilespec.RenameHeld:
		a.event(applied, corev1.EventTypeNormal, out.Reason, "not renamed to %s: %s", out.To, heldBecause(applied))
		a.remember(applied)
	}
	return 0, nil
}

// inFlight holds a rename while a remediation block of the file is in
// flight (§3.9). Before F6.2 and F7.2 no block carries a phase, and a running
// TranscodeJob holds the rename through naming's TranscodePending; F6.2 adds
// status.transcode's Queued, Running and Swapping here, F7.2 status.graft's
// in-flight phases.
func inFlight(*catalogv1alpha1.MediaFile) bool { return false }

func proposal(mf *catalogv1alpha1.MediaFile) string {
	if mf.Status.Naming == nil {
		return mf.Spec.Path + "|"
	}
	return mf.Spec.Path + "|" + mf.Status.Naming.ExpectedPath
}

func (a *Actuator) refusedBefore(mf *catalogv1alpha1.MediaFile) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.refused[mf.UID]
	return ok && p == proposal(mf)
}

func (a *Actuator) remember(mf *catalogv1alpha1.MediaFile) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refused[mf.UID] = proposal(mf)
}

func (a *Actuator) forget(mf *catalogv1alpha1.MediaFile) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.refused, mf.UID)
}

// heldBecause says why RenameFile held a rename, for the Event.
func heldBecause(mf *catalogv1alpha1.MediaFile) string {
	if n := mf.Status.Naming; n != nil && n.Reason != "" {
		return "catalogarr holds it (" + string(n.Reason) + ")"
	}
	return "the proposed path is in another folder, and only files are renamed"
}

func (a *Actuator) event(mf *catalogv1alpha1.MediaFile, eventType, reason, note string, args ...any) {
	if a.o.Recorder != nil {
		a.o.Recorder.Eventf(mf, nil, eventType, reason, "Rename", note, args...)
	}
}

// renameMode is how a file may be renamed: not at all, to its whole
// canonical path (renameFiles), or to its canonical file name in its own
// folder (renameTranscoded, for a file squasharr transcoded).
type renameMode int

const (
	renameOff renameMode = iota
	renameWhole
	renameInFolder
)

// renameMode reports how the RootFolder mf's item is stored under lets mf
// be renamed -- renameFiles wins over renameTranscoded -- and returns the
// namespace's RootFolders, read from the manager's cache: with both switches
// off everywhere -- the default -- the item is never looked up at all.
func (a *Actuator) renameMode(ctx context.Context, c client.Reader, mf *catalogv1alpha1.MediaFile) ([]catalogv1alpha1.RootFolder, renameMode, error) {
	var roots catalogv1alpha1.RootFolderList
	if err := c.List(ctx, &roots, client.InNamespace(mf.Namespace)); err != nil {
		return nil, renameOff, err
	}
	modeOf := func(rf catalogv1alpha1.RootFolder) renameMode {
		switch {
		case rf.Spec.Naming.RenameFilesOrDefault():
			return renameWhole
		case rf.Spec.Naming.RenameTranscoded && transcodedHere(mf):
			return renameInFolder
		}
		return renameOff
	}
	if !slices.ContainsFunc(roots.Items, func(rf catalogv1alpha1.RootFolder) bool { return modeOf(rf) != renameOff }) {
		return roots.Items, renameOff, nil
	}
	ref, err := a.rootFolderRef(ctx, c, mf)
	if err != nil || ref == "" {
		return roots.Items, renameOff, err
	}
	i := slices.IndexFunc(roots.Items, func(rf catalogv1alpha1.RootFolder) bool { return rf.Name == ref })
	if i < 0 {
		return roots.Items, renameOff, nil
	}
	return roots.Items, modeOf(roots.Items[i]), nil
}

// transcodedHere reports whether squasharr transcoded mf: spec.original is
// false -- catalogarr sets it when it incorporates a swap and never resets
// it -- or status.transcode records the swap's profile tag. spec.original
// comes first because status.transcode can be cleared while the file stays
// a transcode.
func transcodedHere(mf *catalogv1alpha1.MediaFile) bool {
	if mf.Spec.Original != nil && !*mf.Spec.Original {
		return true
	}
	return mf.Status.Transcode != nil && mf.Status.Transcode.ProfileTag != ""
}

// openScanOver names a LibraryScan still in progress -- not Completed or
// Failed -- whose walk covers the folder mf's file is in, or returns "".
// A scan's walk is its RootFolder's path joined with spec.subpath; one
// narrowed to a single file in the folder counts too.
func (a *Actuator) openScanOver(
	ctx context.Context, c client.Reader, mf *catalogv1alpha1.MediaFile, roots []catalogv1alpha1.RootFolder,
) (string, error) {
	var scans catalogv1alpha1.LibraryScanList
	if err := c.List(ctx, &scans, client.InNamespace(mf.Namespace)); err != nil {
		return "", err
	}
	dir := filepath.Dir(filepath.Clean(mf.Spec.Path))
	for i := range scans.Items {
		scan := &scans.Items[i]
		if phase := scan.Status.Phase; phase == catalogv1alpha1.ScanPhaseCompleted || phase == catalogv1alpha1.ScanPhaseFailed ||
			k8s.IsDeleting(scan) {
			continue
		}
		j := slices.IndexFunc(roots, func(rf catalogv1alpha1.RootFolder) bool { return rf.Name == scan.Spec.RootFolderRef })
		if j < 0 {
			continue
		}
		walk := filepath.Clean(filepath.Join(roots[j].Spec.Path, scan.Spec.Subpath))
		if dir == walk || strings.HasPrefix(dir, walk+string(filepath.Separator)) || filepath.Dir(walk) == dir {
			return scan.Name, nil
		}
	}
	return "", nil
}

// rootFolderRef is the RootFolder mf's item is stored under: a Movie's own,
// or an Episode's Series', read through c (the manager's cache, which holds
// them). It is "" for an item that does not exist and for any other kind --
// catalogarr names only movies and episodes.
func (a *Actuator) rootFolderRef(ctx context.Context, c client.Reader, mf *catalogv1alpha1.MediaFile) (string, error) {
	key := func(name string) types.NamespacedName {
		return types.NamespacedName{Namespace: mf.Namespace, Name: name}
	}
	ref := mf.Spec.MediaRef
	switch ref.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		if err := c.Get(ctx, key(ref.Name), &m); err != nil {
			return "", client.IgnoreNotFound(err)
		}
		return m.Spec.RootFolderRef, nil
	case commonv1.MediaKindEpisode:
		var ep catalogv1alpha1.Episode
		if err := c.Get(ctx, key(ref.Name), &ep); err != nil {
			return "", client.IgnoreNotFound(err)
		}
		var s catalogv1alpha1.Series
		if err := c.Get(ctx, key(ep.Spec.SeriesRef), &s); err != nil {
			return "", client.IgnoreNotFound(err)
		}
		return s.Spec.RootFolderRef, nil
	}
	return "", nil
}
