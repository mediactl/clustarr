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

package projection

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	subtitlev1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/pipeline"
)

// relatedIndex buckets every resource pipeline.Project needs beyond the
// catalog item itself, keyed by the owning object's UID rather than any
// Name or Kind field. A Name match is a guess: names can be reused once an
// object is deleted and recreated, and nothing stops a stale reference from
// silently resolving to the wrong, newer object. A metav1.OwnerReference's
// UID cannot: UID is unique for the life of the cluster, so a deleted
// owner's UID never resolves to a different object -- it just stops
// resolving, which is the correct outcome.
//
// Downloads and Searches are owned directly by the catalog item they
// target: catalogarr/worker/grab/perform.go and
// catalogarr/controller/search/reconciler.go both call
// k8s.OwnerReferenceAC(owner, ...) against the resolved catalog item before
// creating a Download, and that owner reference is what this index reads.
// TranscodeJobs and SubtitleRequests are owned by the MediaFile they act on
// (per their doc comments and pkg/pipeline.Related's own field docs), not by
// the catalog item directly, so resolving one to a pipeline row goes through
// mediaFileOwner: a TranscodeJob's or SubtitleRequest's owner UID looks up
// which MediaFile owns it, and mediaFileOwner in turn maps that MediaFile's
// own owner UID to the catalog item.
//
// As of this task, importarr's MediaFile writer (importarr/worker/rescan)
// sets no OwnerReference on the MediaFiles it creates -- it links back only
// through the Name-based spec.mediaRef, which this index deliberately does
// not consult, for the same reason Download's Target field is not consulted
// either. Until a later phase sets that reference (and the TranscodeJob and
// SubtitleRequest references squasharr and captionarr do not exist yet to
// set), every bucket past downloads and searches stays empty and every
// pipeline row simply does not advance past the stages that do not need
// them. The moment those controllers start setting the owner reference,
// this index starts filling in with no changes here.
type relatedIndex struct {
	downloads map[types.UID][]downloadv1.Download
	searches  map[types.UID][]catalogv1.Search
	jobs      map[types.UID][]transcodev1.TranscodeJob
	subtitles map[types.UID][]subtitlev1.SubtitleRequest

	// all is every Download this round's single List call returned,
	// regardless of ownership -- unlike downloads above, which drops any
	// Download with no controlling owner reference. [relatedIndex.AllDownloads]
	// exposes it so Projection can feed /events/downloads (Task D3-3) from
	// the same list round this index already does for ownership
	// resolution, per ruling R4: no second List call for the downloads
	// stream.
	all []downloadv1.Download

	mediaFiles map[types.UID]*catalogv1.MediaFile

	// mediaFileOwner maps a MediaFile's own UID to whichever catalog item's
	// UID owns it, so jobs and subtitles -- owned by the MediaFile, not the
	// catalog item -- can be re-bucketed under the item a pipeline row is
	// rendered for.
	mediaFileOwner map[types.UID]types.UID
}

// buildRelatedIndex lists Download, TranscodeJob, SubtitleRequest, Search
// and MediaFile once each -- five List calls total, whatever the catalogue's
// size or the number of open SSE connections -- and buckets every item by
// its owning object's UID.
func buildRelatedIndex(ctx context.Context, r client.Reader) (*relatedIndex, error) {
	idx := &relatedIndex{
		downloads:      map[types.UID][]downloadv1.Download{},
		searches:       map[types.UID][]catalogv1.Search{},
		jobs:           map[types.UID][]transcodev1.TranscodeJob{},
		subtitles:      map[types.UID][]subtitlev1.SubtitleRequest{},
		mediaFiles:     map[types.UID]*catalogv1.MediaFile{},
		mediaFileOwner: map[types.UID]types.UID{},
	}

	// MediaFile is listed first: jobs and subtitles below need
	// mediaFileOwner populated before they can resolve through it.
	var mediaFiles catalogv1.MediaFileList
	if err := r.List(ctx, &mediaFiles); err != nil {
		return nil, fmt.Errorf("projection: list media files: %w", err)
	}
	for i := range mediaFiles.Items {
		mf := &mediaFiles.Items[i]
		if owner, ok := controllingOwnerUID(mf); ok {
			idx.mediaFiles[owner] = mf
			idx.mediaFileOwner[mf.UID] = owner
		}
	}

	var downloads downloadv1.DownloadList
	if err := r.List(ctx, &downloads); err != nil {
		return nil, fmt.Errorf("projection: list downloads: %w", err)
	}
	idx.all = downloads.Items
	for i := range downloads.Items {
		if owner, ok := controllingOwnerUID(&downloads.Items[i]); ok {
			idx.downloads[owner] = append(idx.downloads[owner], downloads.Items[i])
		}
	}

	var searches catalogv1.SearchList
	if err := r.List(ctx, &searches); err != nil {
		return nil, fmt.Errorf("projection: list searches: %w", err)
	}
	for i := range searches.Items {
		if owner, ok := controllingOwnerUID(&searches.Items[i]); ok {
			idx.searches[owner] = append(idx.searches[owner], searches.Items[i])
		}
	}

	var jobs transcodev1.TranscodeJobList
	if err := r.List(ctx, &jobs); err != nil {
		return nil, fmt.Errorf("projection: list transcode jobs: %w", err)
	}
	for i := range jobs.Items {
		if mfOwner, ok := controllingOwnerUID(&jobs.Items[i]); ok {
			if item, ok := idx.mediaFileOwner[mfOwner]; ok {
				idx.jobs[item] = append(idx.jobs[item], jobs.Items[i])
			}
		}
	}

	var subs subtitlev1.SubtitleRequestList
	if err := r.List(ctx, &subs); err != nil {
		return nil, fmt.Errorf("projection: list subtitle requests: %w", err)
	}
	for i := range subs.Items {
		if mfOwner, ok := controllingOwnerUID(&subs.Items[i]); ok {
			if item, ok := idx.mediaFileOwner[mfOwner]; ok {
				idx.subtitles[item] = append(idx.subtitles[item], subs.Items[i])
			}
		}
	}

	return idx, nil
}

// controllingOwnerUID returns the UID of obj's controlling owner reference,
// per metav1.GetControllerOf -- the one owner reference Kubernetes garbage
// collection itself treats as authoritative -- falling back to the first
// owner reference when none is marked as controller, so a non-controlling
// reference (k8s.SetOwnerReference, used for a shared, non-exclusive link)
// still resolves.
func controllingOwnerUID(obj metav1.Object) (types.UID, bool) {
	if ref := metav1.GetControllerOf(obj); ref != nil {
		return ref.UID, true
	}
	refs := obj.GetOwnerReferences()
	if len(refs) == 0 {
		return "", false
	}
	return refs[0].UID, true
}

// AllDownloads returns every Download buildRelatedIndex's single List call
// returned this round, regardless of ownership -- the same population the
// Downloads page (ui/routes.go's listDownloads) shows via its own,
// independent List call. Projection uses this one instead of making a
// second List of its own, so the SAME list round that feeds pipeline rows
// also feeds /events/downloads (Task D3-3, ruling R4).
func (idx *relatedIndex) AllDownloads() []downloadv1.Download {
	return idx.all
}

// Related returns the Related bundle buildRelatedIndex has for the catalog
// item with the given UID: everything pipeline.Project needs beyond the
// item itself. A UID this index has no data for (nothing owned by it, or
// the item itself has no related resources) returns the zero Related,
// exactly as pipeline.Project already treats "nothing to report".
func (idx *relatedIndex) Related(uid types.UID) pipeline.Related {
	return pipeline.Related{
		Downloads: idx.downloads[uid],
		Jobs:      idx.jobs[uid],
		Subtitles: idx.subtitles[uid],
		Search:    latestSearch(idx.searches[uid]),
		MediaFile: idx.mediaFiles[uid],
	}
}

// latestSearch picks the Search Project should use when an item has more
// than one. pipeline.Related.Search's doc comment calls for "the active or
// most recent interactive Search for this item": an active search (Pending
// or Running) wins outright over a finished one regardless of age, and
// among searches with the same activity ranking the most recently created
// wins.
func latestSearch(searches []catalogv1.Search) *catalogv1.Search {
	var best *catalogv1.Search
	for i := range searches {
		s := &searches[i]
		switch {
		case best == nil:
			best = s
		case searchRank(s) != searchRank(best):
			if searchRank(s) > searchRank(best) {
				best = s
			}
		case s.CreationTimestamp.After(best.CreationTimestamp.Time):
			best = s
		}
	}
	return best
}

// searchRank orders a Search's activity for [latestSearch]: an active
// search outranks a finished one.
func searchRank(s *catalogv1.Search) int {
	switch s.Status.Phase {
	case catalogv1.SearchPhasePending, catalogv1.SearchPhaseRunning:
		return 1
	default:
		return 0
	}
}
