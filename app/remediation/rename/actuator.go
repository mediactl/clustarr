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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/app/import/mediafilespec/specwrite"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/fsops"
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

// collisionRecheck is how long a collision holds its proposal back: the
// proposal is not tried again before it has passed, and is once it has,
// since the file in the way may have moved on (refusedBefore). A var, so a
// test shortens it.
var collisionRecheck = time.Minute

// Options configures the actuator.
type Options struct {
	Recorder k8sevents.EventRecorder // recorder "rename"
}

// Actuator is the rename actuator.
type Actuator struct {
	o   Options
	now func() time.Time
	mu  sync.Mutex
	// refused is each file's last refused proposal: a hold stands until
	// the proposal changes, a collision for collisionRecheck.
	refused map[types.UID]refusal
}

// refusal is a proposal ("<spec.path>|<expectedPath>|<mode>|<container>",
// see proposal) that ended in a collision, at the time it did, or in a hold
// (at zero).
type refusal struct {
	proposal string
	at       time.Time
}

// New is the rename actuator.
func New(o Options) *Actuator {
	return &Actuator{o: o, now: time.Now, refused: map[types.UID]refusal{}}
}

// Name implements remediation.Actuator.
func (*Actuator) Name() string { return "rename" }

// Act renames applied's file when it is renameable (mediafilespec.Renameable),
// its RootFolder sets renameFiles (or renameTranscoded for a file transcode
// transcoded, in its own folder; with organizeFolders beside renameFiles a
// non-video file may move to another folder inside its container's), no
// LibraryScan walks its folder or the folder it moves to, and this proposal,
// in this mode, is not held or still waiting out a collision
// (refusedBefore). A scan in progress or a changed file asks for a pass in a
// minute; a collision asks for none of its own, and a pass that finds the
// proposal still waiting it out asks for the one that retries it.
func (a *Actuator) Act(ctx context.Context, env *remediation.Env, c client.Client, applied *catalogv1alpha1.MediaFile) (time.Duration, error) {
	if k8s.IsDeleting(applied) || !mediafilespec.Renameable(applied) || inFlight(applied) {
		a.forget(applied)
		return 0, nil
	}
	// The mode is read before the memo, which is keyed by it: a held file's
	// pass costs the cached RootFolder list and its walk's cached Gets.
	roots, how, err := a.renameMode(ctx, env.Reader, applied)
	if err != nil || !how.on {
		return 0, err
	}
	if wait, refused := a.refusedBefore(applied, how); refused {
		return wait, nil
	}
	scan, err := a.openScanOver(ctx, env.Reader, applied, roots, how)
	if err != nil {
		return 0, err
	}
	if scan != "" {
		// A scan walking the folder could see the vacated path mid-move and
		// re-attribute it, re-applying the frozen fields of a MediaFile the
		// rename is rewriting (spec D6); one walking the folder it moves to
		// could find it there before the move is recorded. Wait for it to
		// finish.
		a.event(applied, corev1.EventTypeNormal, ReasonScanInProgress, "not renamed yet: LibraryScan %s is walking the file's folder", scan)
		return recheckAfter, nil
	}
	out, err := specwrite.RenameFile(ctx, c, env.APIReader, applied, false, how.mode, how.container)
	if err != nil {
		return 0, err
	}
	switch {
	case out.Moved:
		a.event(applied, corev1.EventTypeNormal, ReasonRenamed, "renamed %s to %s", out.From, out.To)
		logging.FromContext(ctx).Info("renamed a library file to its canonical path", "from", out.From, "to", out.To)
	case out.Reason == mediafilespec.RenameCollision:
		// No pass of its own: the file's next wake retries it once the
		// interval has passed (refusedBefore). A pass a minute per
		// permanently colliding file -- tied canonical paths under a
		// preset without a part token, NamingCurrent False for good --
		// would cost an uncached Get and two stats each, forever.
		a.event(applied, corev1.EventTypeWarning, out.Reason, "not renamed to %s: something already exists there, or where one of its folders would be", out.To)
		a.remember(applied, how, a.now())
	case out.Reason == mediafilespec.RenameChanged:
		a.event(applied, corev1.EventTypeWarning, out.Reason,
			"not renamed to %s: the file changed since it was recorded; retrying once it is re-observed", out.To)
		return recheckAfter, nil
	case out.Reason == mediafilespec.RenameHeld:
		a.event(applied, corev1.EventTypeNormal, out.Reason, "not renamed to %s: %s", out.To, heldBecause(applied, how))
		a.remember(applied, how, time.Time{})
	}
	return 0, nil
}

// inFlight holds a rename while a remediation block of the file is in
// flight (§3.9): status.transcode Queued, Running or Swapping (F6.2); F7.2
// adds status.graft's in-flight phases.
func inFlight(mf *catalogv1alpha1.MediaFile) bool {
	if t := mf.Status.Transcode; t != nil {
		switch t.Phase {
		case catalogv1alpha1.TranscodePhaseQueued, catalogv1alpha1.TranscodePhaseRunning, catalogv1alpha1.TranscodePhaseSwapping:
			return true
		}
	}
	return false
}

// proposal is the key a refusal is remembered under: the file's path, the
// path catalog proposes, and how the actuator renames it -- the mode and,
// under RenameOrganize, the container folder -- so a hold made under one
// mode is tried again under another (2026-11-10 design D19: turning a
// root's organizeFolders on retries what the whole-path rename held as a
// folder move), and an organized one when its container's folder moves.
func proposal(mf *catalogv1alpha1.MediaFile, how renameAs) string {
	expected := ""
	if mf.Status.Naming != nil {
		expected = mf.Status.Naming.ExpectedPath
	}
	return mf.Spec.Path + "|" + expected + "|" + how.mode.String() + "|" + how.container
}

// refusedBefore reports mf's proposal refused before and not due again --
// a held one until it changes, a collided one until collisionRecheck has
// passed since the collision -- and how long until it is. A collision's
// target may be vacated since (2026-11-10 design D11: X renamed to "(01)",
// its tied sibling Y collided there on its low-priority re-plan, X's own
// pass moved it on to "(02)", which woke Y again, and Y's unchanged
// proposal was never tried again before a restart), so a pass within the
// interval asks for the pass at its end, which tries it again; a file
// permanently in the way raises a Warning at most once an interval, on a
// pass something else asks for, never on a pass of its own every minute.
func (a *Actuator) refusedBefore(mf *catalogv1alpha1.MediaFile, how renameAs) (wait time.Duration, refused bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.refused[mf.UID]
	if !ok || r.proposal != proposal(mf, how) {
		return 0, false
	}
	if r.at.IsZero() {
		return 0, true
	}
	if wait = collisionRecheck - a.now().Sub(r.at); wait > 0 {
		return wait, true
	}
	return 0, false
}

// remember records mf's proposal, renamed as how says, as refused: a
// collision at the time it was seen, a hold at zero.
func (a *Actuator) remember(mf *catalogv1alpha1.MediaFile, how renameAs, at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refused[mf.UID] = refusal{proposal: proposal(mf, how), at: at}
}

func (a *Actuator) forget(mf *catalogv1alpha1.MediaFile) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.refused, mf.UID)
}

// heldBecause says why RenameFile held a rename renamed as how says, for
// the Event: catalog's own hold, else a proposal in another folder, which
// only RenameOrganize moves, and only inside the container's folder.
func heldBecause(mf *catalogv1alpha1.MediaFile, how renameAs) string {
	if n := mf.Status.Naming; n != nil && n.Reason != "" {
		return "catalog holds it (" + string(n.Reason) + ")"
	}
	switch {
	case how.mode == specwrite.RenameOrganize:
		return "the move would leave " + how.container + ", the folder the root organizes the file's item in"
	case videoKind(mf.Spec.MediaRef.Kind):
		return "the proposed path is in another folder, and a video file is renamed in its own folder only"
	case how.organizes:
		return "the proposed path is in another folder, and the folder of the item that holds the file is not known yet"
	}
	return "the proposed path is in another folder, and the root does not organize folders"
}

func (a *Actuator) event(mf *catalogv1alpha1.MediaFile, eventType, reason, note string, args ...any) {
	if a.o.Recorder != nil {
		a.o.Recorder.Eventf(mf, nil, eventType, reason, "Rename", note, args...)
	}
}

// renameAs is how a file may be renamed: not at all (on false), else through
// RenameFile's mode -- RenameWhole for renameFiles, RenameInFolder for
// renameTranscoded on a file the transcode controller transcoded, and
// RenameOrganize for a non-video file under renameFiles and organizeFolders
// whose container's folder is known, with that folder as container.
// organizes records that the root asks to organize folders, whether or not
// the file could be (the Event's wording).
type renameAs struct {
	on        bool
	mode      specwrite.RenameMode
	container string
	organizes bool
}

// renameMode reports how the RootFolder mf's item is stored under lets mf
// be renamed -- renameFiles wins over renameTranscoded -- and returns the
// namespace's RootFolders, read from the manager's cache: with both switches
// off everywhere -- the default -- the item is never looked up at all.
// renameFiles is read for every kind the loop names (2026-11-10 design D7);
// renameTranscoded is video-only, since only a video file is transcoded;
// organizeFolders is non-video-only and needs renameFiles (design D19), and
// is RenameOrganize only while the container's folder (rootFolderRef's
// walk) lies strictly under the root's path -- an Artist or an Author whose
// name has not arrived has the root itself as its folder, which must never
// bound a move -- else the whole-path rename, which holds a move to another
// folder.
func (a *Actuator) renameMode(ctx context.Context, c client.Reader, mf *catalogv1alpha1.MediaFile) ([]catalogv1alpha1.RootFolder, renameAs, error) {
	var roots catalogv1alpha1.RootFolderList
	if err := c.List(ctx, &roots, client.InNamespace(mf.Namespace)); err != nil {
		return nil, renameAs{}, err
	}
	video := videoKind(mf.Spec.MediaRef.Kind)
	renames := func(rf catalogv1alpha1.RootFolder) bool {
		return rf.Spec.Naming.RenameFilesOrDefault() || rf.Spec.Naming.RenameTranscoded && video && transcodedHere(mf)
	}
	if !slices.ContainsFunc(roots.Items, renames) {
		return roots.Items, renameAs{}, nil
	}
	ref, container, err := a.rootFolderRef(ctx, c, mf)
	if err != nil || ref == "" {
		return roots.Items, renameAs{}, err
	}
	i := slices.IndexFunc(roots.Items, func(rf catalogv1alpha1.RootFolder) bool { return rf.Name == ref })
	if i < 0 {
		return roots.Items, renameAs{}, nil
	}
	rf := roots.Items[i]
	switch {
	case rf.Spec.Naming.RenameFilesOrDefault():
		how := renameAs{on: true, mode: specwrite.RenameWhole, organizes: !video && rf.Spec.Naming.OrganizeFoldersOrDefault()}
		if how.organizes && container != "" && fsops.StrictlyUnder(container, rf.Spec.Path) {
			how.mode, how.container = specwrite.RenameOrganize, filepath.Clean(container)
		}
		return roots.Items, how, nil
	case rf.Spec.Naming.RenameTranscoded && video && transcodedHere(mf):
		return roots.Items, renameAs{on: true, mode: specwrite.RenameInFolder}, nil
	}
	return roots.Items, renameAs{}, nil
}

// videoKind reports whether a file of kind is a video file, the only kind
// the transcode controller transcodes.
func videoKind(kind commonv1.MediaKind) bool {
	return kind == commonv1.MediaKindMovie || kind == commonv1.MediaKindEpisode
}

// transcodedHere reports whether the transcode controller transcoded mf: spec.original is
// false -- catalog sets it when it incorporates a swap and never resets
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
// Failed -- whose walk covers the folder mf's file is in, or, for a move to
// another folder under RenameOrganize, the folder it moves to, or returns
// "". A scan's walk is its RootFolder's path joined with spec.subpath; one
// narrowed to a single file in the folder counts too.
func (a *Actuator) openScanOver(
	ctx context.Context, c client.Reader, mf *catalogv1alpha1.MediaFile, roots []catalogv1alpha1.RootFolder, how renameAs,
) (string, error) {
	var scans catalogv1alpha1.LibraryScanList
	if err := c.List(ctx, &scans, client.InNamespace(mf.Namespace)); err != nil {
		return "", err
	}
	dirs := []string{filepath.Dir(filepath.Clean(mf.Spec.Path))}
	if n := mf.Status.Naming; how.mode == specwrite.RenameOrganize && n != nil && n.ExpectedPath != "" {
		if to := filepath.Dir(filepath.Clean(n.ExpectedPath)); to != dirs[0] {
			dirs = append(dirs, to)
		}
	}
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
		for _, dir := range dirs {
			if dir == walk || strings.HasPrefix(dir, walk+string(filepath.Separator)) || filepath.Dir(walk) == dir {
				return scan.Name, nil
			}
		}
	}
	return "", nil
}

// rootFolderRef is the RootFolder mf's item is stored under, read through c
// (the manager's cache, which holds them): a Movie's own, an Episode's
// Series', an Album's Artist's, a Book's own override else its Author's, an
// Audiobook's own and an Issue's Comic's -- the same resolution the naming
// planner's owner lookup makes (mediafile.namingOwnerOf; 2026-11-10 design
// D7, §5.5). It is "" for an item or container that does not exist, a
// standalone Book with no root, and any other kind, which the loop does not
// name.
//
// container is the folder of the item that holds a non-video file, the one
// RenameOrganize keeps a move inside (design D19), read off the walk's
// status.path: an Album's Artist's, a Book's Author's -- or the Book's own
// when it names its own root (a standalone Book's folder, or an author's
// book stored under another root, whose author folder there is its
// status.path) -- an Audiobook's own and an Issue's Comic's. It is "" for a
// video file and for a container that has written no status.path yet.
func (a *Actuator) rootFolderRef(ctx context.Context, c client.Reader, mf *catalogv1alpha1.MediaFile) (ref, container string, err error) {
	key := func(name string) types.NamespacedName {
		return types.NamespacedName{Namespace: mf.Namespace, Name: name}
	}
	item := mf.Spec.MediaRef
	switch item.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		if err := c.Get(ctx, key(item.Name), &m); err != nil {
			return "", "", client.IgnoreNotFound(err)
		}
		return m.Spec.RootFolderRef, "", nil
	case commonv1.MediaKindEpisode:
		var ep catalogv1alpha1.Episode
		if err := c.Get(ctx, key(item.Name), &ep); err != nil {
			return "", "", client.IgnoreNotFound(err)
		}
		var s catalogv1alpha1.Series
		if err := c.Get(ctx, key(ep.Spec.SeriesRef), &s); err != nil {
			return "", "", client.IgnoreNotFound(err)
		}
		return s.Spec.RootFolderRef, "", nil
	case commonv1.MediaKindAlbum:
		var album catalogv1alpha1.Album
		if err := c.Get(ctx, key(item.Name), &album); err != nil {
			return "", "", client.IgnoreNotFound(err)
		}
		var artist catalogv1alpha1.Artist
		if err := c.Get(ctx, key(album.Spec.ArtistRef), &artist); err != nil {
			return "", "", client.IgnoreNotFound(err)
		}
		return artist.Spec.RootFolderRef, artist.Status.Path, nil
	case commonv1.MediaKindBook:
		var book catalogv1alpha1.Book
		if err := c.Get(ctx, key(item.Name), &book); err != nil {
			return "", "", client.IgnoreNotFound(err)
		}
		if own := ptr.Deref(book.Spec.RootFolderRef, ""); own != "" {
			return own, book.Status.Path, nil
		}
		authorRef := ptr.Deref(book.Spec.AuthorRef, "")
		if authorRef == "" {
			return "", "", nil
		}
		var author catalogv1alpha1.Author
		if err := c.Get(ctx, key(authorRef), &author); err != nil {
			return "", "", client.IgnoreNotFound(err)
		}
		return author.Spec.RootFolderRef, author.Status.Path, nil
	case commonv1.MediaKindAudiobook:
		var ab catalogv1alpha1.Audiobook
		if err := c.Get(ctx, key(item.Name), &ab); err != nil {
			return "", "", client.IgnoreNotFound(err)
		}
		return ab.Spec.RootFolderRef, ab.Status.Path, nil
	case commonv1.MediaKindIssue:
		var issue catalogv1alpha1.Issue
		if err := c.Get(ctx, key(item.Name), &issue); err != nil {
			return "", "", client.IgnoreNotFound(err)
		}
		var comic catalogv1alpha1.Comic
		if err := c.Get(ctx, key(issue.Spec.ComicRef), &comic); err != nil {
			return "", "", client.IgnoreNotFound(err)
		}
		return comic.Spec.RootFolderRef, comic.Status.Path, nil
	}
	return "", "", nil
}
