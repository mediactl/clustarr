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

package library_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	toolscache "k8s.io/client-go/tools/cache"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/library"
	"github.com/mediactl/clustarr/test/librarytest"
)

// TestALazyStoreReadsALibraryThatComesLater is the ui's start on a fresh
// install (kind, 2026-11-26): it was ready a minute before Postgres, its one
// connect at start failed, and it served empty pages until it was restarted.
// A lazy store opens with nothing listening at its address, and an informer
// on one that finds no library yet -- the manager has not made the tables --
// keeps asking and holds what is there once it has.
func TestALazyStoreReadsALibraryThatComesLater(t *testing.T) {
	const nobody = "postgres://clustarr@127.0.0.1:1/clustarr?sslmode=disable&connect_timeout=2"
	_, err := library.Open(t.Context(), nobody, library.Options{})
	require.Error(t, err, "an open that connects fails with nothing at the address")
	away, err := library.Open(t.Context(), nobody, library.Options{Lazy: true})
	require.NoError(t, err, "a lazy open does not connect")
	require.NoError(t, away.Close())
	_, err = library.Open(t.Context(), nobody, library.Options{Lazy: true, Migrate: true})
	require.Error(t, err, "a migration is never lazy")

	// A database with no library in it: Postgres is up, the manager has not
	// started.
	dsn := librarytest.Start(t).DSN(t)
	st, err := library.Open(t.Context(), dsn, library.Options{Lazy: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	st.PollInterval = 200 * time.Millisecond
	sch := issueSchema(t, st)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	lw, err := st.ListerWatcher(sch, catalogScheme(t), "")
	require.NoError(t, err)
	held, informer := toolscache.NewInformerWithOptions(toolscache.InformerOptions{
		ListerWatcher: lw, ObjectType: &catalogv1.Issue{}, Handler: toolscache.ResourceEventHandlerFuncs{},
	})
	go informer.RunWithContext(ctx)
	_, err = lw.ListWithContext(ctx, metav1.ListOptions{})
	require.Error(t, err, "there is no library to list yet")
	require.False(t, informer.HasSynced())

	manager, err := library.Open(t.Context(), dsn, library.Options{Migrate: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	_, err = manager.Create(ctx, newIssue("saga-1"), "clustarr")
	require.NoError(t, err)

	require.Eventually(t, informer.HasSynced, 30*time.Second, 100*time.Millisecond, "the informer lists once the library is there")
	require.Eventually(t, func() bool {
		_, ok, _ := held.GetByKey("default/saga-1")
		return ok
	}, 10*time.Second, 50*time.Millisecond)
}
