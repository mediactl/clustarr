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

package relindex_test

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/relindex"
)

func TestStoreIsSafeUnderConcurrentWritersAndReaders(t *testing.T) {
	// indexarr is one replica, so there is one writer PROCESS -- but inside
	// it the RSS worker and the search service both write, concurrently, on
	// different goroutines. SQLite allows exactly one writer at a time; the
	// store serialises them with a mutex so the loser waits instead of
	// getting SQLITE_BUSY. Readers under WAL never block and take no lock.
	//
	// Must be run with -race. It is the only test here that would notice a
	// data race on the store's own fields.
	const (
		writers   = 4
		readers   = 4
		perWriter = 25
		pruners   = 1
	)

	s := newStore(t)
	ctx := t.Context()
	var wg sync.WaitGroup

	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			indexer := "indexer-" + strconv.Itoa(w)
			for i := range perWriter {
				r := rel(indexer, "g"+strconv.Itoa(i), "The Matrix 1999 "+strconv.Itoa(i))
				r.TitleNorm = "matrix 1999 " + strconv.Itoa(i)
				r.FetchedAt = fetchedAt.Add(time.Duration(i) * time.Second)
				n, err := s.Upsert(ctx, []relindex.Release{r})
				if !assert.NoError(t, err) {
					return
				}
				assert.Equal(t, 1, n)
			}
		}()
	}

	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWriter {
				_, err := s.Search(ctx, relindex.Query{Text: "matrix", Limit: 50})
				if !assert.NoError(t, err) {
					return
				}
				if _, err := s.Stats(ctx); !assert.NoError(t, err) {
					return
				}
			}
		}()
	}

	for range pruners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWriter {
				// Older than anything written, so it competes for the
				// write lock without changing the expected row count.
				if _, err := s.Prune(ctx, fetchedAt.Add(-24*time.Hour)); !assert.NoError(t, err) {
					return
				}
			}
		}()
	}

	wg.Wait()

	st, err := s.Stats(ctx)
	require.NoError(t, err)
	require.EqualValues(t, writers*perWriter, st.Releases)
	require.EqualValues(t, writers, st.Indexers)
}

func TestConcurrentUpsertsOfTheSameKeyInsertItExactlyOnce(t *testing.T) {
	// Two goroutines racing on the same (indexer, guid) must produce one row
	// and exactly one reported insert across all of them. If UNIQUE were
	// missing or the count were derived from len(rels), this reports more.
	s := newStore(t)
	ctx := t.Context()

	const goroutines = 8
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total int
	)
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := s.Upsert(ctx, []relindex.Release{rel("nzbgeek", "g1", "The Matrix 1999")})
			if !assert.NoError(t, err) {
				return
			}
			mu.Lock()
			total += n
			mu.Unlock()
		}()
	}
	wg.Wait()

	require.Equal(t, 1, total, "exactly one goroutine may claim the insert")
	st, err := s.Stats(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, st.Releases)
}
