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

// Package folderscan asks, once, for a scan of an item's folder when the
// item first lists its children: Sonarr's RefreshSeries, then RescanSeries.
//
// A library scan attributes an episode file only to an Episode that exists.
// A Series a scan creates (or Add New, or an import list) has none until the
// Series reconciler fans them out from its metadata, so the scan that found
// the series counted the rest of its files as awaiting episodes
// (Progress.AwaitingChildren) and attributed none of them -- they reached the
// library only at the next scan (smoke finding O8: 131 series created, 433 of
// 11,122 files matched). [Pass] is the one step that closes that gap, a state
// machine kept in the item's FolderScanned condition:
//
//   - absent: nothing owed. The pass that first lists the item's children,
//     when the status recorded none before, sets it Unknown or False
//     (Settling). An item that already had its children -- every item when
//     this shipped -- never owes a scan.
//   - Unknown, Settling: the children are listed, but some do not yet carry
//     what the scan attributes the item's files by (design D18,
//     [Request.ChildrenSettled]): an Album its document and its track list
//     ([AlbumListed]), a Book its document ([BookListed]); the message names
//     how many. A scan now would count their files as awaiting, and this one
//     is the item's only scan, so it waits: on talos 390 scanned artists'
//     albums are thousands of MusicBrainz lookups at one a second, hours
//     after the artists exist. Each child's arrival wakes the item (the
//     container's watch on its children); the one timer is the wait's
//     deadline, [ChildrenWait] after it began (the condition's
//     lastTransitionTime). A child whose lookup fails for good -- a release
//     group or a work the provider answers 404 for, asked again on every
//     backoff and never listed -- would otherwise hold the item's only scan
//     for good, so once the deadline passes the scan is asked for at once,
//     with what is in, and its message names how many were not. A Series'
//     episodes come whole from its fan-out, so a Series never waits here.
//   - Unknown, Pending: a scan the deadline released that could not be
//     asked for yet (as False, Pending below); Unknown, so the deadline is
//     not moved and the next pass tries again without a second wait.
//   - False, Settling: the children are in, and the import agent reads them
//     from its own cache, fed by its own watch. The scan is asked for
//     [SettleAfter] after the condition turned False, the pass that found the
//     last child in, so it reads them all. A child that falls back to
//     waiting (an artist's new album) returns the condition to Unknown, its
//     wait begun again.
//   - False, Pending: the folder is not known yet (the metadata write that
//     makes it known wakes the item), could not be read, or the LibraryScan
//     could not be created (both retried after [RetryAfter]). A child that
//     falls back to waiting returns this to Unknown too: a scan is never
//     asked for while a child is still to come, but past the deadline.
//   - True, ScanRequested or NothingOnDisk: done, for the item's life. The
//     folder held files and one LibraryScan of it was created (incremental,
//     spec.subpath the item's folder); or it was missing or empty, and a
//     download's import brings any file later.
//
// The scan is the ordinary one, so the scanner still never guesses: it
// attributes what it can and lists the rest as unmatched. One item asks for
// one scan of its own folder, never more: a scan that created 131 series asks
// for 131 small scans as their episodes arrive (each series' metadata on its
// own schedule), each a task on the import queue's scan consumer and a walk
// of one folder, together no more than the second full scan they replace. A
// folder that is missing or holds nothing costs one directory read on the
// remediation loop's /data executor and no scan at all.
package folderscan

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/datapath"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The manager creates the LibraryScan; its role already holds create on
// libraryscans (the scan applier's re-observing scans).
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=libraryscans,verbs=create

// The FolderScanned condition's reasons.
const (
	ReasonSettling      = "Settling"
	ReasonPending       = "Pending"
	ReasonScanRequested = "ScanRequested"
	ReasonNothingOnDisk = "NothingOnDisk"
)

// SettleAfter is how long after an item's children are in -- first listed,
// or for an Artist or an Author the last of them listed (D18) -- its folder
// scan is asked for: long enough for the import agent's cache to hold what
// the pass just saw.
const SettleAfter = 30 * time.Second

// RetryAfter is how long a Pending scan waits before it is tried again.
const RetryAfter = time.Minute

// ChildrenWait is the longest a scan waits for its children's lists (design
// D18), from the pass that began the wait: long enough for talos's shape,
// thousands of albums' documents and track lists at one MusicBrainz lookup a
// second (hours), and short enough that a child whose lookup fails for good
// costs its container's folder a day, not its only scan.
const ChildrenWait = 24 * time.Hour

// LabelRootFolder is the label every LibraryScan of a root folder carries
// (rootfolderschedule.LabelRootFolder, as the scan applier's re-observing
// scans carry it).
const LabelRootFolder = "catalog.clustarr.io/root-folder"

// Folders reports whether a folder, a logical /data path, holds anything a
// scan could attribute.
type Folders interface {
	HoldsFiles(ctx context.Context, path string) (bool, error)
}

// DirReader is the /data executor a [Probe] reads through: the remediation
// loop's IOExecutor, whose per-call deadline and mount-health breaker keep a
// hung mount from pinning a reconciler.
type DirReader interface {
	ReadDir(ctx context.Context, dir string) ([]fs.DirEntry, error)
}

// Probe is the [Folders] the manager runs: one directory read of the
// folder, mapped from the logical /data onto DataDir.
type Probe struct {
	IO DirReader
	// DataDir is where /data is mounted in this process; "" is /data.
	DataDir string
}

// HoldsFiles reports whether path exists and lists an entry that is not
// hidden: a file, or a folder such as "Season 01", whose tree the scan walks.
// A folder that is missing, not a folder, or off /data holds nothing; any
// other failed read (a timeout, a busy pool, an open breaker) is an error,
// never "nothing there".
func (p Probe) HoldsFiles(ctx context.Context, path string) (bool, error) {
	local, err := datapath.Local(p.DataDir, path)
	if err != nil {
		return false, nil //nolint:nilerr // a folder off /data is one no scan reaches
	}
	entries, err := p.IO.ReadDir(ctx, local)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return false, nil
	case err != nil:
		return false, err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			return true, nil
		}
	}
	return false, nil
}

// Request is one item's pass.
type Request struct {
	// Item is the Series (or other parent) whose folder is scanned.
	Item client.Object
	// Root is the item's RootFolder; nil while it is not known.
	Root *catalogv1alpha1.RootFolder
	// Path is the item's folder, status.path; "" while it is not known.
	Path string
	// HadChildren is whether the item's status recorded children before
	// this pass; HasChildren whether it has any now.
	HadChildren, HasChildren bool
	// Children names them, for the condition's message ("episodes").
	Children string
	// ChildrenSettled is whether every child the pass lists carries what a
	// scan attributes the item's files by (design D18): true for a Series,
	// whose episodes come whole from its fan-out; for an Artist, no Album of
	// its fan-out pending ([AlbumsPending]); for an Author, no Book
	// ([BooksPending]). The zero value waits, so a container that does not
	// say never scans early. Until it is true the scan stays Settling, for
	// at most [ChildrenWait].
	ChildrenSettled bool
	// Pending is how many of the Count children are not settled yet, and
	// Awaits what they wait for ("metadata and track lists"), for the
	// condition's message while it waits ("waiting for the metadata and
	// track lists of 7 of its 23 albums").
	Pending, Count int
	Awaits         string
	// FieldManager creates the LibraryScan.
	FieldManager k8s.FieldManager
	// Now is the pass's clock.
	Now time.Time
}

// Outcome is what a pass did.
type Outcome struct {
	// Scan is the LibraryScan this pass asked for; "" when none.
	Scan string
	// Retry is when the item needs another pass; zero is never.
	Retry time.Duration
}

// ScanName is the one LibraryScan an item's folder scan creates: named by
// the item and its UID, so a pass that repeats after a lost status apply
// finds it rather than creating a second.
func ScanName(item client.Object) string {
	return k8s.ChildName(item.GetName(), "folder-scan", string(item.GetUID()))
}

// Pass is one reconcile's step of the folder scan (see the package doc),
// recorded in conditions under catalogv1alpha1.SeriesConditionFolderScanned.
// While req.ChildrenSettled is false the scan waits, Unknown, asking only
// for a pass at its deadline ([ChildrenWait] after the wait began); the pass
// that finds it true starts the settle, and the pass past the deadline asks
// for the scan with what is in. A nil folders does nothing.
func Pass(ctx context.Context, c client.Client, folders Folders, req Request, conditions *[]metav1.Condition) Outcome {
	if folders == nil {
		return Outcome{}
	}
	const condType = catalogv1alpha1.SeriesConditionFolderScanned
	cond := k8s.FindCondition(*conditions, condType)
	// waiting holds the scan for the children's lists (D18): Unknown, so the
	// pass that finds the last of them in turns it False and the settle
	// counts from then (a status change stamps lastTransitionTime), and so
	// the deadline counts from the pass that began the wait. Each child's
	// arrival wakes the item; left is the time to the deadline.
	waiting := func(left time.Duration) Outcome {
		k8s.MarkUnknown(req.Item, conditions, condType, ReasonSettling,
			"waiting for the %s of %d of its %d %s; its folder is scanned for their files once they are in",
			req.Awaits, req.Pending, req.Count, req.Children)
		return Outcome{Retry: left}
	}
	// overdue is a scan the deadline released while children are still to
	// come: asked for at once, and any step that cannot be made yet keeps
	// the condition Unknown, so the deadline does not move.
	overdue := false
	switch {
	case cond == nil && (req.HadChildren || !req.HasChildren):
		return Outcome{}
	case cond == nil && !req.ChildrenSettled:
		return waiting(ChildrenWait)
	case cond == nil:
		k8s.MarkFalse(req.Item, conditions, condType, ReasonSettling,
			"its first %s are listed; its folder is scanned for their files once they settle", req.Children)
		return Outcome{Retry: SettleAfter}
	case cond.Status == metav1.ConditionTrue:
		return Outcome{}
	case !req.ChildrenSettled && cond.Status == metav1.ConditionUnknown:
		if left := cond.LastTransitionTime.Add(ChildrenWait).Sub(req.Now); left > 0 {
			return waiting(left)
		}
		overdue = true
	case !req.ChildrenSettled:
		// A child fell back to waiting while the scan settled or was
		// pending: the scan waits for it again, from now.
		return waiting(ChildrenWait)
	case cond.Status == metav1.ConditionUnknown:
		// The last child came in, during the wait or after its deadline
		// released a scan that could not be asked for yet.
		k8s.MarkFalse(req.Item, conditions, condType, ReasonSettling,
			"the %s of all %d of its %s are in; its folder is scanned for their files once they settle",
			req.Awaits, req.Count, req.Children)
		return Outcome{Retry: SettleAfter}
	case cond.Reason == ReasonSettling:
		if wait := cond.LastTransitionTime.Add(SettleAfter).Sub(req.Now); wait > 0 {
			return Outcome{Retry: wait}
		}
	}

	pending := func(retry time.Duration, format string, args ...any) Outcome {
		status := metav1.ConditionFalse
		if overdue {
			status = metav1.ConditionUnknown
		}
		k8s.SetCondition(req.Item, conditions, k8s.NewCondition(condType, status, ReasonPending, format, args...))
		return Outcome{Retry: retry}
	}
	if req.Root == nil || req.Path == "" {
		// The folder is known once the item's metadata is: the write that
		// brings it wakes the item, so nothing needs a timer.
		return pending(0, "waiting for its folder to be known")
	}
	sub, ok := subpath(req.Root.Spec.Path, req.Path)
	if !ok {
		k8s.MarkTrue(req.Item, conditions, condType, ReasonNothingOnDisk,
			"its folder %s is not under root folder %s (%s): no scan of it reaches the folder",
			req.Path, req.Root.Name, req.Root.Spec.Path)
		return Outcome{}
	}
	holds, err := folders.HoldsFiles(ctx, req.Path)
	if err != nil {
		return pending(RetryAfter, "could not read its folder %s: %v", req.Path, err)
	}
	if !holds {
		k8s.MarkTrue(req.Item, conditions, condType, ReasonNothingOnDisk,
			"its folder %s holds no files; a download's import brings them", req.Path)
		return Outcome{}
	}

	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{
			Name: ScanName(req.Item), Namespace: req.Item.GetNamespace(),
			Labels: map[string]string{LabelRootFolder: req.Root.Name},
		},
		Spec: catalogv1alpha1.LibraryScanSpec{
			RootFolderRef: req.Root.Name, Subpath: sub, Mode: catalogv1alpha1.ScanModeIncremental,
		},
	}
	if err := c.Create(ctx, scan, client.FieldOwner(req.FieldManager.String())); err != nil && !apierrors.IsAlreadyExists(err) {
		return pending(RetryAfter, "could not create LibraryScan %s: %v", scan.Name, err)
	}
	log := logging.FromContext(ctx)
	if overdue {
		k8s.MarkTrue(req.Item, conditions, condType, ReasonScanRequested,
			"LibraryScan %s scans %s for the files that awaited its %s, though the %s of %d of its %d %s were not in after %s",
			scan.Name, sub, req.Children, req.Awaits, req.Pending, req.Count, req.Children, ChildrenWait)
		log.Warn("folderscan: scanning an item's folder though some of its children never came in",
			"libraryScan", scan.Name, "rootFolder", req.Root.Name, "subpath", sub,
			"pending", req.Pending, "children", req.Count, "waited", ChildrenWait)
		return Outcome{Scan: scan.Name}
	}
	k8s.MarkTrue(req.Item, conditions, condType, ReasonScanRequested,
		"LibraryScan %s scans %s for the files that awaited its %s", scan.Name, sub, req.Children)
	log.Info("folderscan: scanning an item's folder for the files its first children awaited",
		"libraryScan", scan.Name, "rootFolder", req.Root.Name, "subpath", sub)
	return Outcome{Scan: scan.Name}
}

// subpath is path relative to root, when it lies strictly beneath it.
func subpath(root, path string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// The Album reconciler's own condition on its track-listing RPC and the two
// verdicts it reports False on (album.conditionTracksSynced,
// album.SelectionNoAcceptedRelease, album.SelectionPinnedReleaseMissing;
// this package cannot import album, which imports artist, which imports
// this): True once a listing was taken (or the release group lists no
// releases), False with the selection's verdict when the listing holds
// nothing the artist takes, or with the request's class
// (events.RequestErrorClass.Reason) when the ask failed. The artist
// package's envtest runs the real Album reconciler and this package's tests
// read album's names through AlbumListed, which holds these to them.
const (
	albumConditionTracksSynced         = "TracksSynced"
	albumSelectionNoAcceptedRelease    = "NoAcceptedRelease"
	albumSelectionPinnedReleaseMissing = "PinnedReleaseMissing"
)

// AlbumListed reports whether an Album carries what a scan attributes a
// track file by (design D18): its document -- the title, year and release
// ids the scan's MatchAlbum reads (rescan's AlbumCandidate), which the
// queued metadata task brings, often long after the Album's first pass
// listed its tracks through a direct RPC -- and its track list. The list is
// in when the Album lists tracks, or when its listing was answered with
// none: the release group lists no releases, no release is one the artist's
// metadata profile takes, or a pinned release is missing.
//
// A failed ask is never an answer, whatever its class: the gateway answers
// a transient MusicBrainz failure -- its one-a-second limiter refusing a
// wait past the caller's deadline, a 503, a dropped connection -- with an
// error string, which the Album reads as RPCError like any other, and asks
// again on its backoff. A lookup that fails for good (a release group
// MusicBrainz no longer has) is not told apart from those; [ChildrenWait]
// bounds the scan's wait for it instead.
//
// D18 reads "status.tracks or a MetadataReady False condition"; on the
// Album, MetadataReady is False ("Refreshing") on every pass before its
// document lands, the first included, whether the listing landed or not, and
// True once the document is in whatever the listing holds, so that reading
// would take an Album whose ask failed as settled and wait for good on one
// whose listing holds no accepted release.
func AlbumListed(alb *catalogv1alpha1.Album) bool {
	if md := alb.Status.Metadata; md == nil || md.RefreshedAt.IsZero() {
		return false
	}
	if len(alb.Status.Tracks) > 0 {
		return true
	}
	cond := k8s.FindCondition(alb.Status.Conditions, albumConditionTracksSynced)
	switch {
	case cond == nil:
		return false
	case cond.Status == metav1.ConditionTrue:
		return true
	case cond.Status == metav1.ConditionFalse:
		return cond.Reason == albumSelectionNoAcceptedRelease || cond.Reason == albumSelectionPinnedReleaseMissing
	}
	return false
}

// AlbumsPending is how many of albums are not yet listed (AlbumListed): the
// Artist's [Request.Pending], over the Albums its fan-out holds.
func AlbumsPending(albums []catalogv1alpha1.Album) int {
	n := 0
	for i := range albums {
		if !AlbumListed(&albums[i]) {
			n++
		}
	}
	return n
}

// BookListed reports whether a Book's metadata is in (design D18): a scan
// attributes a book file by the Book's title, which its document brings. A
// Book whose lookup fails for good (a work Open Library answers 404 for)
// never gets one -- the metadata worker's failure record keeps no document,
// and nothing on the Book says the failure is final -- so [ChildrenWait]
// bounds the scan's wait for it.
func BookListed(bk *catalogv1alpha1.Book) bool {
	return bk.Status.Metadata != nil
}

// BooksPending is how many of books have no metadata yet (BookListed): the
// Author's [Request.Pending], over the Books its fan-out holds.
func BooksPending(books []catalogv1alpha1.Book) int {
	n := 0
	for i := range books {
		if !BookListed(&books[i]) {
			n++
		}
	}
	return n
}
