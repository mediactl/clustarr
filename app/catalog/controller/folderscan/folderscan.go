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
//     [Request.ChildrenSettled]): an Album its track list ([AlbumListed]), a
//     Book its metadata ([BookListed]); the message names how many. A scan
//     now would count their files as awaiting, and this one is the item's
//     only scan, so it waits: on talos 390 scanned artists' albums are
//     thousands of MusicBrainz lookups at one a second, hours after the
//     artists exist. No timer runs; each child's arrival wakes the item
//     (the container's watch on its children). A Series' episodes come whole
//     from its fan-out, so a Series never waits here.
//   - False, Settling: the children are in, and the import agent reads them
//     from its own cache, fed by its own watch. The scan is asked for
//     [SettleAfter] after the condition turned False, the pass that found the
//     last child in, so it reads them all. A child that falls back to
//     waiting (an artist's new album) returns the condition to Unknown.
//   - False, Pending: the folder is not known yet (the metadata write that
//     makes it known wakes the item), could not be read, or the LibraryScan
//     could not be created (both retried after [RetryAfter]).
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
	"github.com/mediactl/clustarr/pkg/events"
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
	// say never scans early. Until it is true the scan stays Settling.
	ChildrenSettled bool
	// Pending is how many of the Count children are not settled yet, and
	// Awaits what they wait for ("track lists"), for the condition's message
	// while it waits ("waiting for the track lists of 7 of its 23 albums").
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
// While req.ChildrenSettled is false a Settling scan waits, asking for no
// timer; the pass that finds it true starts the settle. A nil folders does
// nothing.
func Pass(ctx context.Context, c client.Client, folders Folders, req Request, conditions *[]metav1.Condition) Outcome {
	if folders == nil {
		return Outcome{}
	}
	const condType = catalogv1alpha1.SeriesConditionFolderScanned
	cond := k8s.FindCondition(*conditions, condType)
	// waiting holds the scan for the children's lists (D18): Unknown, so the
	// pass that finds the last of them in turns it False and the settle
	// counts from then (a status change stamps lastTransitionTime). No
	// timer: each child's arrival wakes the item.
	waiting := func() Outcome {
		k8s.MarkUnknown(req.Item, conditions, condType, ReasonSettling,
			"waiting for the %s of %d of its %d %s; its folder is scanned for their files once they are in",
			req.Awaits, req.Pending, req.Count, req.Children)
		return Outcome{}
	}
	switch {
	case cond == nil && (req.HadChildren || !req.HasChildren):
		return Outcome{}
	case cond == nil && !req.ChildrenSettled:
		return waiting()
	case cond == nil:
		k8s.MarkFalse(req.Item, conditions, condType, ReasonSettling,
			"its first %s are listed; its folder is scanned for their files once they settle", req.Children)
		return Outcome{Retry: SettleAfter}
	case cond.Status == metav1.ConditionTrue:
		return Outcome{}
	case cond.Reason == ReasonSettling && !req.ChildrenSettled:
		return waiting()
	case cond.Reason == ReasonSettling && cond.Status != metav1.ConditionFalse:
		k8s.MarkFalse(req.Item, conditions, condType, ReasonSettling,
			"the %s of all %d of its %s are in; its folder is scanned for their files once they settle",
			req.Awaits, req.Count, req.Children)
		return Outcome{Retry: SettleAfter}
	case cond.Reason == ReasonSettling:
		if wait := cond.LastTransitionTime.Add(SettleAfter).Sub(req.Now); wait > 0 {
			return Outcome{Retry: wait}
		}
	}

	pending := func(format string, args ...any) Outcome {
		k8s.MarkFalse(req.Item, conditions, condType, ReasonPending, format, args...)
		return Outcome{Retry: RetryAfter}
	}
	if req.Root == nil || req.Path == "" {
		// The folder is known once the item's metadata is: the write that
		// brings it wakes the item, so nothing needs a timer.
		k8s.MarkFalse(req.Item, conditions, condType, ReasonPending, "waiting for its folder to be known")
		return Outcome{}
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
		return pending("could not read its folder %s: %v", req.Path, err)
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
		return pending("could not create LibraryScan %s: %v", scan.Name, err)
	}
	k8s.MarkTrue(req.Item, conditions, condType, ReasonScanRequested,
		"LibraryScan %s scans %s for the files that awaited its %s", scan.Name, sub, req.Children)
	logging.FromContext(ctx).Info("folderscan: scanning an item's folder for the files its first children awaited",
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

// albumConditionTracksSynced is the Album reconciler's own condition on its
// track-listing RPC (album.conditionTracksSynced, unexported there; this
// package cannot import album, which imports artist, which imports this):
// True once a listing was taken (or the release group lists no releases),
// False with the request's class (events.RequestErrorClass.Reason) when the
// ask failed, or with the selection's verdict (NoAcceptedRelease,
// PinnedReleaseMissing) when the listing holds nothing the artist takes.
// The artist package's envtest runs the real Album reconciler and reads its
// listing through AlbumListed, which holds this name to it.
const albumConditionTracksSynced = "TracksSynced"

// AlbumListed reports whether an Album's track list is in (design D18): it
// lists tracks, or its track-listing RPC was answered with none -- the
// release group lists no releases, no release is one the artist's metadata
// profile takes, a pinned release is missing, or the gateway answered with
// an error of its own (a release group MusicBrainz no longer has). An Album
// whose first pass has not landed, or whose ask failed for a reason a retry
// cures (no agent, a busy one, a timeout: the request classes the Album
// retries on its own backoff), is still to come.
//
// D18 reads "status.tracks or a MetadataReady False condition"; on the
// Album, MetadataReady is False ("Refreshing") on every pass before its
// document lands, the first included, whether the listing landed or not, and
// True once the document is in whatever the listing holds, so that reading
// would take an Album whose ask timed out as settled (the 1/s MusicBrainz
// queue on talos times out most first asks) and wait for good on one whose
// listing holds no accepted release. The listing's own condition says what
// the design meant: whether the Album will list tracks without another ask.
func AlbumListed(alb *catalogv1alpha1.Album) bool {
	if len(alb.Status.Tracks) > 0 {
		return true
	}
	cond := k8s.FindCondition(alb.Status.Conditions, albumConditionTracksSynced)
	switch {
	case cond == nil:
		return false
	case cond.Status == metav1.ConditionTrue:
		return true
	}
	return !retriedRequest(cond.Reason)
}

// retriedRequest reports whether reason names a request class an
// idempotent caller asks again (events.RequestErrorClass.Retryable).
func retriedRequest(reason string) bool {
	for _, c := range []events.RequestErrorClass{events.RequestNoResponders, events.RequestBusy, events.RequestTimeout} {
		if c.Retryable() && c.Reason() == reason {
			return true
		}
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
// attributes a book file by the Book's title, which its document brings.
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
