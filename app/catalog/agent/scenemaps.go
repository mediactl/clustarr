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
	"fmt"
	"net/http"
	"time"

	"github.com/jonboulle/clockwork"

	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/scenemap"
)

// sceneMapCacheSize bounds the in-process TheXEM cache: two whole-catalogue
// entries (havemap and the scene names) plus one row set per mapped series
// asked about. TheXEM maps a few thousand series in all.
const sceneMapCacheSize = 4096

// NewSceneMaps builds a TheXEM scene-numbering source (x4b-report,
// x6b-report "W2 decides the cache").
//
// The cache is per process, and each catalog domain that reads scene
// numbers builds its own: the catalog domain's search worker and the events
// domain's RSS matcher (spec §3.5.3). It is a metadata.LRUCache rather than
// the metadata gateway's tiered L1/L2: that cache lives in the metadata
// domain's process, and the agent reads TheXEM through its own client. The
// cost is that each agent replica asks TheXEM itself -- the havemap and
// names every three hours, a mapped series' rows every twelve
// (scenemap.Cached's Sonarr-matching TTLs), and nothing at all for the
// unmapped series that are almost all of them -- which is well inside
// TheXEM's undocumented limit and the client's own 1 req/s limiter.
//
// The limiter is the caller's, per the caller-owns-rate-limiting rule:
// scenemap.NewXEM never defaults one on, and this package is that caller.
func NewSceneMaps() (scenemap.Source, error) {
	cache, err := pkgmetadata.NewLRUCache(sceneMapCacheSize, clockwork.NewRealClock())
	if err != nil {
		return nil, fmt.Errorf("catalog agent: scene-map cache: %w", err)
	}
	xem := scenemap.NewXEM(scenemap.XEMConfig{
		HTTPClient: &http.Client{Timeout: sceneMapTimeout},
		Limiter:    pkgmetadata.NewLimiter(scenemap.DefaultRate, scenemap.DefaultBurst),
	})
	return scenemap.NewCached(xem, cache, scenemap.Options{}), nil
}

// sceneMapTimeout bounds one TheXEM request, so a hung upstream cannot hold
// a search or an RSS decision for the consumer's whole AckWait.
const sceneMapTimeout = 30 * time.Second
