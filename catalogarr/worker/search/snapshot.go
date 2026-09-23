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

package search

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/quality"
)

// itemSnapshot is everything the worker reads off the cluster before it can
// decide anything: the catalog item's search identity, the QualityProfile to
// resolve, and the decision engine's Target (current file, live queue, the
// blocklist predicate and the item's decision.Identity included -- all but
// Identity.IDQueryIndexers, which only the search's reply can supply; Handle
// fills it in once indexarr has answered).
type itemSnapshot struct {
	IDs               TargetIDs
	QualityProfileRef string
	Target            decision.Target
}

// snapshot fetches the item named by ref in namespace ns, its current
// MediaFile if it has one, and the live queue for it, and folds the blocklist
// informer into a predicate the decision engine can call per release.
//
// Only movie and episode are implemented. Spec §16's M1 scopes catalogarr's
// non-video kinds (artist, album, author, book, audiobook, comic, issue) to
// M6, and Handle discards a SearchTask for any other kind before it gets
// here; the default branch is a belt-and-braces error, not a silent empty
// snapshot.
func (w *Worker) snapshot(ctx context.Context, ns string, ref commonv1.MediaRef) (itemSnapshot, error) {
	snap := itemSnapshot{
		Target: decision.Target{
			Kind: ref.Kind,
			Key:  events.MediaKey(string(ref.Kind), ns, ref.Name),
		},
	}

	var (
		hasFile bool
		fileRef *string
	)

	switch ref.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		if err := w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &m); err != nil {
			return snap, fmt.Errorf("get Movie %s/%s: %w", ns, ref.Name, err)
		}
		snap.QualityProfileRef = m.Spec.QualityProfileRef
		snap.IDs.TmdbID = m.Spec.TmdbID
		snap.Target.Monitored = ptr.Deref(m.Spec.Monitored, true)
		snap.Target.Available = m.Status.Available
		if md := m.Status.Metadata; md != nil {
			snap.IDs.Year = md.Year
			snap.IDs.ImdbID = md.ExternalIDs[commonv1.IDKeyIMDB]
			snap.IDs.OriginalLanguageTag = md.OriginalLanguage
			snap.IDs.Title = md.Title
			snap.Target.RuntimeMinutes = int(md.RuntimeMinutes)
			snap.Target.OriginalLanguageTag = md.OriginalLanguage
		}
		snap.Target.Identity = MovieIdentity(&m)
		hasFile, fileRef = m.Status.HasFile, m.Status.FileRef

	case commonv1.MediaKindEpisode:
		var e catalogv1alpha1.Episode
		if err := w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &e); err != nil {
			return snap, fmt.Errorf("get Episode %s/%s: %w", ns, ref.Name, err)
		}
		var s catalogv1alpha1.Series
		if err := w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: e.Spec.SeriesRef}, &s); err != nil {
			return snap, fmt.Errorf("get Series %s/%s: %w", ns, e.Spec.SeriesRef, err)
		}

		// An Episode carries no QualityProfileRef of its own: episodes are
		// ranked against the owning Series' profile (spec §4.2).
		snap.QualityProfileRef = s.Spec.QualityProfileRef
		// The SERIES tvdb id, not Episode.status.tvdbID. A Newznab/Torznab
		// tv-search is keyed `tvdbid=<series>&season=&ep=`; the episode's own
		// tvdb id identifies a different entity and no indexer accepts it.
		// (The task brief said status.tvdbID here -- see the task report.)
		snap.IDs.TvdbID = s.Spec.TvdbID
		snap.IDs.Season = ptr.To(e.Spec.SeasonNumber)
		snap.IDs.Episode = ptr.To(e.Spec.EpisodeNumber)
		snap.IDs.Anime = s.Spec.SeriesType == catalogv1alpha1.SeriesTypeAnime
		if snap.IDs.Anime && e.Status.AbsoluteNumber != nil {
			// Anime indexers key releases by absolute number, so the
			// absolute number -- not the in-season number -- is what goes
			// on the wire. See BuildSearchRequest for the Season/Episode
			// folding this feeds.
			snap.IDs.Episode = e.Status.AbsoluteNumber
		}
		// Prefer the episode's own runtime; fall back to the series'
		// average when metadata has not filled it in yet. Zero is left as
		// zero so pkg/decision applies its own 45-minute fallback rather
		// than this package inventing a second one.
		runtime := e.Status.RuntimeMinutes
		if runtime == 0 && s.Status.Metadata != nil {
			runtime = s.Status.Metadata.RuntimeMinutes
		}
		snap.Target.Monitored = ptr.Deref(e.Spec.Monitored, true)
		snap.Target.Available = episodeAvailable(&e, w.now())
		snap.Target.RuntimeMinutes = int(runtime)
		// pkg/decision reads an episode's runtime from EpisodeRuntimes, not
		// RuntimeMinutes (see targetRuntimeMinutes). A single-episode search
		// therefore carries exactly one entry; a pack search covering several
		// episodes is the wanted-cron task's job and would carry one entry
		// per key in SearchTask.Keys.
		snap.Target.EpisodeRuntimes = []int{int(runtime)}
		if md := s.Status.Metadata; md != nil {
			snap.IDs.Year = md.Year
			snap.IDs.OriginalLanguageTag = md.OriginalLanguage
			// The SERIES title, not the episode's own: a text fallback
			// query is "<series> SxxEyy" (see resolvedText), and an
			// Episode carries no title of its own to put there.
			snap.IDs.Title = md.Title
			snap.Target.OriginalLanguageTag = md.OriginalLanguage
		}
		snap.Target.Identity = EpisodeIdentity(&s, &e)
		hasFile, fileRef = e.Status.HasFile, e.Status.FileRef

	default:
		return snap, fmt.Errorf("unsupported media kind %q", ref.Kind)
	}

	if hasFile && fileRef != nil && *fileRef != "" {
		current, err := w.currentFile(ctx, ns, *fileRef)
		if err != nil {
			return snap, err
		}
		snap.Target.Current = current
	}

	queue, err := w.queue(ctx, ns, ref)
	if err != nil {
		return snap, err
	}
	snap.Target.Queue = queue
	blocklist, err := LoadBlocklist(ctx, w.Client, ns, w.now())
	if err != nil {
		return snap, err
	}
	snap.Target.Blocklist = blocklist.Contains
	return snap, nil
}

// episodeAvailable decides §8.2's availability input for an episode. The air
// date is the fact; the phase is the derived state, so the air date wins when
// it is known and the phase is the fallback for an Episode the controller has
// already classified but whose metadata has not landed. A blank phase on an
// object with no air date is treated as NOT available: guessing "available"
// there would let a search for an unaired episode grab a fake.
func episodeAvailable(e *catalogv1alpha1.Episode, now time.Time) bool {
	if e.Status.AirDate != nil {
		return !e.Status.AirDate.After(now)
	}
	return e.Status.Phase != "" && e.Status.Phase != catalogv1alpha1.EpisodePhaseUnaired
}

// currentFile reads the MediaFile an item already has into the decision
// engine's Current. Quality, revision, format score and matched formats are
// read from MediaFileSpec, not status: spec §8.4 freezes the decided fields on
// spec at import time and MediaFileStatus carries none of them.
//
// SourceHash has no MediaFile-side source -- a torrent's info hash lives on
// the Download that produced the file -- so it is resolved from
// spec.importedFrom.downloadRef when that Download still exists. A missing
// Download is not an error: blocklisting keeps the Download around, and
// already-imported matching falls back to the release title, which
// spec.importedFrom.releaseTitle always carries.
func (w *Worker) currentFile(ctx context.Context, ns, name string) (*decision.Current, error) {
	var mf catalogv1alpha1.MediaFile
	if err := w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &mf); err != nil {
		if apierrors.IsNotFound(err) {
			// status.hasFile is a rollup written by another reconcile; a
			// dangling fileRef means the rollup has not caught up with a
			// deletion yet. Deciding as if there were no file is the safe
			// reading -- it can only approve an upgrade the user wanted.
			return nil, nil
		}
		return nil, fmt.Errorf("get MediaFile %s/%s: %w", ns, name, err)
	}

	cur := &decision.Current{
		Quality:     mf.Spec.Quality,
		Revision:    mf.Spec.Revision,
		FormatScore: int(mf.Spec.FormatScore),
		Formats:     mf.Spec.MatchedFormats,
	}
	if mf.Spec.ImportedFrom == nil {
		return cur, nil
	}
	cur.SourceTitle = mf.Spec.ImportedFrom.ReleaseTitle
	if ref := mf.Spec.ImportedFrom.DownloadRef; ref != "" {
		var d downloadv1alpha1.Download
		switch err := w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref}, &d); {
		case err == nil:
			cur.SourceHash = d.Spec.Release.InfoHash
		case apierrors.IsNotFound(err):
			// Expected once grabarr sweeps the Download.
		default:
			return nil, fmt.Errorf("get Download %s/%s: %w", ns, ref, err)
		}
	}
	return cur, nil
}

// queue lists the non-terminal Downloads already working on this item, as the
// decision engine's queue-preference check needs them.
func (w *Worker) queue(ctx context.Context, ns string, ref commonv1.MediaRef) ([]decision.Queued, error) {
	var list downloadv1alpha1.DownloadList
	if err := w.Client.List(ctx, &list,
		client.InNamespace(ns),
		client.MatchingFields{IndexDownloadTarget: TargetIndexValue(ref)},
	); err != nil {
		return nil, fmt.Errorf("list queued Downloads for %s: %w", TargetIndexValue(ref), err)
	}
	out := make([]decision.Queued, 0, len(list.Items))
	for i := range list.Items {
		rel := list.Items[i].Spec.Release
		out = append(out, quality.Candidate{
			Quality:     rel.Quality,
			Revision:    rel.Revision,
			FormatScore: int(rel.FormatScore),
		})
	}
	return out, nil
}
