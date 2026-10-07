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

package manager

import (
	"k8s.io/apimachinery/pkg/runtime"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/app/catalog/controller/album"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
	"github.com/mediactl/clustarr/app/catalog/controller/book"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/controller/episode"
	"github.com/mediactl/clustarr/app/catalog/controller/issue"
	"github.com/mediactl/clustarr/app/catalog/controller/movie"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
)

// items is the loop's item path (loop spec §3.12): one reconciler per item
// kind, each recording Events under its kind's name as its controller did.
// recorder is mgr.GetEventRecorder.
//
// Every item kind needs the bus. Movie, Album, Book and Audiobook are
// metadata targets of their own and publish their own MetadataTasks; Album
// also asks the metadata gateway for its track listing
// (rpc.catalogarr.metadata.lookup), behind its ReleaseCache; Episode and
// Issue fetch no metadata of their own, but every kind publishes its catalog
// item and file events, and a nil Bus publishes nothing.
func items(c client.Client, s *runtime.Scheme, recorder func(name string) k8sevents.EventRecorder, bus events.Bus) map[remediation.KeyKind]rollup.Item {
	return map[remediation.KeyKind]rollup.Item{
		remediation.KindMovie:   &movie.Reconciler{Client: c, Scheme: s, Recorder: recorder("movie"), Bus: bus},
		remediation.KindEpisode: &episode.Reconciler{Client: c, Scheme: s, Recorder: recorder("episode"), Bus: bus},
		remediation.KindAlbum: &album.Reconciler{
			Client: c, Scheme: s, Recorder: recorder("album"), Bus: bus,
			Releases: album.NewReleaseCache(album.DefaultReleaseCacheSize),
		},
		remediation.KindBook:      &book.Reconciler{Client: c, Scheme: s, Recorder: recorder("book"), Bus: bus},
		remediation.KindAudiobook: &audiobook.Reconciler{Client: c, Scheme: s, Recorder: recorder("audiobook"), Bus: bus},
		remediation.KindIssue:     &issue.Reconciler{Client: c, Scheme: s, Recorder: recorder("issue"), Bus: bus},
		// The grab owners of episodes and issues (ADR-0019 §6.1, A3.2): the
		// controllers named "series" and "comic" are gone, their recorders
		// keep the names.
		remediation.KindSeries: &series.Reconciler{Client: c, Scheme: s, Recorder: recorder("series"), Bus: bus},
		remediation.KindComic:  &comic.Reconciler{Client: c, Scheme: s, Recorder: recorder("comic"), Bus: bus},
	}
}
