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

package events

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMediaKeyIsStableAndReadable(t *testing.T) {
	k := MediaKey("Movie", "default", "the-matrix")
	assert.Equal(t, k, MediaKey("Movie", "default", "the-matrix"), "must be deterministic")
	assert.True(t, strings.HasPrefix(k, "Movie-default-the-matrix-"), "keeps a readable prefix, got %q", k)
	assert.Len(t, strings.TrimPrefix(k, "Movie-default-the-matrix-"), mediaKeyHashLen)
}

// The kind is load-bearing: without it a Movie and a Series of the same name
// in the same namespace collide on the clustarr-leases key, and the second
// grab is silently dropped.
func TestMediaKeyDistinguishesKinds(t *testing.T) {
	movie := MediaKey("Movie", "default", "thing")
	series := MediaKey("Series", "default", "thing")
	require.NotEqual(t, movie, series)
	assert.NotEqual(t, LeaseKey(movie), LeaseKey(series))
	assert.NotEqual(t, PendingKey(movie), PendingKey(series))
	assert.NotEqual(t, WorkGrabSubject(movie), WorkGrabSubject(series))
}

// The separator-flattening collision the digest exists to prevent: tok()
// rewrites "." and "/" to "-", so a readable prefix alone is ambiguous.
// Both of these are valid Kubernetes namespace/name pairs.
func TestMediaKeySurvivesSubjectTokenisation(t *testing.T) {
	a := MediaKey("Movie", "default-foo", "bar")
	b := MediaKey("Movie", "default", "foo-bar")
	require.NotEqual(t, a, b)

	// The prefixes really do collide -- this is the trap, not a hypothetical.
	assert.Equal(t, "Movie-default-foo-bar", strings.TrimSuffix(a, a[len(a)-mediaKeyHashLen-1:]))
	assert.Equal(t, "Movie-default-foo-bar", strings.TrimSuffix(b, b[len(b)-mediaKeyHashLen-1:]))

	// ... and the full keys still differ after every transform that touches them.
	assert.NotEqual(t, WorkGrabSubject(a), WorkGrabSubject(b))
	assert.NotEqual(t, WorkSearchSubject(PriorityNormal, a), WorkSearchSubject(PriorityNormal, b))
	assert.NotEqual(t, LeaseKey(a), LeaseKey(b))
}

// A media key is interpolated into a subject, so it must not reintroduce a
// token separator once tok() has run over it.
func TestMediaKeyIsASingleSubjectToken(t *testing.T) {
	for _, tc := range []struct{ kind, ns, name string }{
		{"Movie", "default", "a.b.c"},
		{"Episode", "media/ops", "s01e02"},
		{"Series", "", ""},
		{"Movie", "default", "Wall·E (2008)"},
	} {
		k := MediaKey(tc.kind, tc.ns, tc.name)
		assert.NotContains(t, k, ".", "media key %q would split the subject", k)
		assert.NotContains(t, k, "/", "media key %q would split the subject", k)
		assert.NotContains(t, k, " ", "media key %q is not a legal subject token", k)
		assert.Equal(t, k, tok(k), "media key %q must already be tok()-stable", k)
	}
}

// ForSingleNode switches every stream to memory storage, and the production
// MaxBytes reservations (~8 GiB in total) cannot survive that switch
// unchanged: config/nats sets max_memory_store to 256Mi on a pod with a 1Gi
// limit, so NATS rejects the stream and every controller CrashLoopBackOffs on
// the first one it tries to create. Found on the first real kind run -- no
// unit or envtest suite could see it, because neither applies a memory
// ceiling.
func TestForSingleNodeFitsTheMemoryCeiling(t *testing.T) {
	single := Default().ForSingleNode()

	var total int64
	for _, s := range single.Streams {
		assert.Equal(t, StorageMemory, s.Storage, "stream %s must be memory-backed", s.Name)
		assert.Positive(t, s.MaxBytes, "stream %s reserves nothing and would reject its first publish", s.Name)
		total += s.MaxBytes
	}

	assert.LessOrEqual(t, total, int64(singleNodeMemoryBudget),
		"single-node streams reserve %d bytes, over the %d-byte budget that fits config/nats' max_memory_store",
		total, int64(singleNodeMemoryBudget))

	// Object stores are not scaled and must not be memory-backed: the
	// artwork bucket reserves 5 GiB, which no single node's memory store
	// holds (every controller crash-looped on kind, 2026-09-24).
	for _, o := range single.ObjectStores {
		assert.Equal(t, StorageFile, o.Storage, "object store %s must stay on file storage on a single node", o.Name)
		assert.Equal(t, 1, o.Replicas, "object store %s replicas", o.Name)
	}
}

// Scaling must keep the relative sizing the production topology chose rather
// than flattening every stream to one cap.
func TestForSingleNodeKeepsRelativeStreamSizing(t *testing.T) {
	byName := func(ts Topology) map[string]int64 {
		m := make(map[string]int64, len(ts.Streams))
		for _, s := range ts.Streams {
			m[s.Name] = s.MaxBytes
		}
		return m
	}
	p, s := byName(Default()), byName(Default().ForSingleNode())

	require.Greater(t, p[StreamReleases], p[StreamWorkIndexarr], "premise changed: re-pick the streams")
	assert.Greater(t, s[StreamReleases], s[StreamWorkIndexarr], "scaling flattened production's ordering")
}
