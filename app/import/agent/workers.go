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

package agent

import (
	"net/http"
	"time"

	"github.com/jonboulle/clockwork"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/app/import/worker/importlist"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/scenemap"
)

// newScanWorker builds the work.import.scan handler with o's sample size
// floor. rescan.NewWorker already defaults the floor, so the assignment
// matters exactly when o carries a different one -- a non-default
// --sample-max-bytes, or 0 to disable the rule. It gets the import domain's
// one prober; it reads through the cache and writes nothing (its
// observations are the manager's scan applier's to decide, ADR-0019 §7.6).
func newScanWorker(c client.Client, bus events.Bus, prober mediainfo.Prober, o Options) *rescan.Worker {
	w := rescan.NewWorker(c, bus, prober)
	w.SampleMaxBytes = o.SampleMaxBytes
	return w
}

// newListWorker builds the work.import.list handler with o's Trakt and
// Plex base URLs (see Options.TraktBaseURL) and the manager's API reader,
// which the provider credentials and the Trakt token Secret are read
// through (importlist.Worker.APIReader).
func newListWorker(c client.Client, api client.Reader, bus events.Bus, o Options) *importlist.Worker {
	w := importlist.NewWorker(c, bus)
	w.APIReader = api
	w.TraktBaseURL = o.TraktBaseURL
	w.PlexBaseURL = o.PlexBaseURL
	return w
}

// newImportWorker builds the work.import.fileimport handler with o's
// sample size floor, for the reason [newScanWorker] gives, and the
// manager's API reader, which a movie's existing files are read through
// (fileimport.Worker.APIReader), and the import domain's one prober.
func newImportWorker(c client.Client, api client.Reader, bus events.Bus, prober mediainfo.Prober, o Options) *fileimport.Worker {
	w := fileimport.NewWorker(c, bus, prober)
	w.APIReader = api
	w.SampleMaxBytes = o.SampleMaxBytes
	w.SceneMaps = newSceneMaps()
	return w
}

// sceneMapCacheSize bounds the import agent's TheXEM cache, as the catalog
// agent's does; sceneMapTimeout one request to it.
const (
	sceneMapCacheSize = 4096
	sceneMapTimeout   = 30 * time.Second
)

// newSceneMaps builds the import domain's TheXEM source: a download's
// files are attributed through the scene numbering its release was grabbed
// by (fileimport.Worker.SceneMaps). Like the catalog agent's
// (app/catalog/agent.NewSceneMaps) it is this process's own client and
// in-process cache -- the havemap and names every three hours, a mapped
// series' rows every twelve -- under this caller's limiter. A cache that
// cannot be built leaves the worker reading numbers literally.
func newSceneMaps() scenemap.Source {
	cache, err := pkgmetadata.NewLRUCache(sceneMapCacheSize, clockwork.NewRealClock())
	if err != nil {
		return nil
	}
	xem := scenemap.NewXEM(scenemap.XEMConfig{
		HTTPClient: &http.Client{Timeout: sceneMapTimeout},
		Limiter:    pkgmetadata.NewLimiter(scenemap.DefaultRate, scenemap.DefaultBurst),
	})
	return scenemap.NewCached(xem, cache, scenemap.Options{})
}
