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
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mediactl/clustarr/app/catalog/controller/album"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
	"github.com/mediactl/clustarr/app/catalog/controller/book"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/controller/episode"
	"github.com/mediactl/clustarr/app/catalog/controller/folderscan"
	"github.com/mediactl/clustarr/app/catalog/controller/issue"
	"github.com/mediactl/clustarr/app/catalog/controller/movie"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestItemsRunEveryItemKind: the loop's item path covers every item kind --
// the six file-backed kinds and, since A3.2, the grab owners Series and
// Comic -- each with its own recorder name (loop spec §3.12), every one on
// the bus, and Album's track sync goes through its cache.
func TestItemsRunEveryItemKind(t *testing.T) {
	var names []string
	recorder := func(name string) k8sevents.EventRecorder { names = append(names, name); return nil }
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	env := &remediation.Env{IO: remediation.NewIOExecutor(1), DataDir: "/data"}
	got := items(c, k8s.MustNewScheme(), recorder, bus, env)
	assert.ElementsMatch(t, []remediation.KeyKind{remediation.KindMovie, remediation.KindEpisode, remediation.KindAlbum,
		remediation.KindBook, remediation.KindAudiobook, remediation.KindIssue, remediation.KindSeries, remediation.KindComic},
		slices.Collect(maps.Keys(got)))

	mov, ok := got[remediation.KindMovie].(*movie.Reconciler)
	require.True(t, ok)
	assert.Same(t, bus, mov.Bus)
	ep, ok := got[remediation.KindEpisode].(*episode.Reconciler)
	require.True(t, ok)
	assert.Same(t, bus, ep.Bus)
	alb, ok := got[remediation.KindAlbum].(*album.Reconciler)
	require.True(t, ok)
	assert.NotNil(t, alb.Releases, "the loop's Album path caches its releases")
	assert.Same(t, bus, alb.Bus, "Album asks the metadata gateway for its tracks over the bus")
	bk, ok := got[remediation.KindBook].(*book.Reconciler)
	require.True(t, ok)
	assert.Same(t, bus, bk.Bus)
	ab, ok := got[remediation.KindAudiobook].(*audiobook.Reconciler)
	require.True(t, ok)
	assert.Same(t, bus, ab.Bus)
	is, ok := got[remediation.KindIssue].(*issue.Reconciler)
	require.True(t, ok)
	assert.Same(t, bus, is.Bus)
	ser, ok := got[remediation.KindSeries].(*series.Reconciler)
	require.True(t, ok)
	assert.Same(t, bus, ser.Bus)
	// The folder scan a series' first episodes ask for (smoke finding O8)
	// reads /data through the loop's own executor.
	assert.Equal(t, folderscan.Probe{IO: env.IO, DataDir: "/data"}, ser.Folders)
	com, ok := got[remediation.KindComic].(*comic.Reconciler)
	require.True(t, ok)
	assert.Same(t, bus, com.Bus)
	assert.Same(t, c, mov.Client, "every kind reads and writes through the manager's client")

	assert.ElementsMatch(t, []string{"movie", "episode", "album", "book", "audiobook", "issue", "series", "comic"}, names)
}
