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
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// kindLife is the least a differential test needs to know of a kind: a
// spec the schema takes, two disjoint parts of status for two managers, a
// spec patch that is a new generation, one the schema or a rule refuses,
// and a second refused write where the kind has another rule worth holding.
type kindLife struct {
	kind, file string
	spec       map[string]any
	statusA    map[string]any // applied by catalog
	statusB    map[string]any // applied by catalog-grab
	specPatch  map[string]any // allowed: a new generation
	badPatch   map[string]any // refused: an immutable field, or a rule
	badStatus  map[string]any // refused by the schema's values
}

var kindLives = []kindLife{
	{
		kind: "MediaFile", file: "catalog.clustarr.io_mediafiles.yaml",
		spec:      map[string]any{"mediaRef": map[string]any{"kind": "episode", "name": "show-s01e01"}, "path": "/data/media/tv/Show/S01E01.mkv"},
		statusA:   map[string]any{"probeHash": "abc", "conditions": []any{cond("Ready", "True")}},
		statusB:   map[string]any{"graftTag": "g1"},
		specPatch: map[string]any{"path": "/data/media/tv/Show/Season 01/S01E01.mkv"},
		// mediaRef's kind and name are immutable; and a track needs kind album.
		badPatch:  map[string]any{"mediaRef": map[string]any{"kind": "episode", "name": "show-s01e02"}},
		badStatus: map[string]any{"transcode": map[string]any{"phase": "Sideways"}},
	},
	{
		kind: "Episode", file: "catalog.clustarr.io_episodes.yaml",
		spec:      map[string]any{"seriesRef": "show", "seasonNumber": int64(1), "episodeNumber": int64(1)},
		statusA:   map[string]any{"hasFile": false, "cutoffReason": "NoFile", "conditions": []any{cond("Ready", "False")}},
		statusB:   map[string]any{"activeDownloadRef": "dl-1"},
		specPatch: map[string]any{"monitored": false},
		badPatch:  map[string]any{"seasonNumber": int64(2)},
		badStatus: map[string]any{"downloadPhase": "Sideways"},
	},
	{
		kind: "Album", file: "catalog.clustarr.io_albums.yaml",
		spec:      map[string]any{"artistRef": "band", "releaseGroupID": "11111111-1111-1111-1111-111111111111"},
		statusA:   map[string]any{"hasFile": false, "trackSummary": "0/10", "conditions": []any{cond("Ready", "False")}},
		statusB:   map[string]any{"activeDownloadRef": "dl-1"},
		specPatch: map[string]any{"monitored": false},
		badPatch:  map[string]any{"artistRef": "other"},
		badStatus: map[string]any{"downloadPhase": "Sideways"},
	},
	{
		kind: "Book", file: "catalog.clustarr.io_books.yaml",
		spec:      map[string]any{"workID": "OL45804W", "authorRef": "herbert"},
		statusA:   map[string]any{"hasFile": false, "fileFormat": "epub", "conditions": []any{cond("Ready", "False")}},
		statusB:   map[string]any{"activeDownloadRef": "dl-1"},
		specPatch: map[string]any{"monitored": false},
		// a book without an author names its profile and its root folder
		badPatch:  map[string]any{"authorRef": nil},
		badStatus: map[string]any{"downloadPhase": "Sideways"},
	},
	{
		kind: "Audiobook", file: "catalog.clustarr.io_audiobooks.yaml",
		spec:      map[string]any{"asin": "B002V0QK4C", "qualityProfileRef": "audio", "rootFolderRef": "audiobooks"},
		statusA:   map[string]any{"hasFile": false, "path": "/data/media/audiobooks/Dune", "conditions": []any{cond("Ready", "False")}},
		statusB:   map[string]any{"activeDownloadRef": "dl-1"},
		specPatch: map[string]any{"monitored": false},
		badPatch:  map[string]any{"asin": "B000000000"},
		badStatus: map[string]any{"downloadPhase": "Sideways"},
	},
}

func cond(typ, status string) map[string]any {
	return map[string]any{"type": typ, "status": status, "reason": "Test", "message": "", "lastTransitionTime": "2026-11-26T00:00:00Z"}
}

// TestEveryKindsWritesMatchTheAPIServer runs one short life of each library
// kind beside TestIssueWritesMatchTheAPIServer's long one, against a real
// apiserver and the store: the kind's own schema decides the defaults, what
// each of two managers owns, the generation, and which writes are refused
// -- by a value the schema does not take, and by the kind's own rule.
func TestEveryKindsWritesMatchTheAPIServer(t *testing.T) {
	for _, life := range kindLives {
		t.Run(life.kind, func(t *testing.T) {
			a, s := startDifferential(t, "schemas/"+life.file)
			const name = "subject"
			object := func(extra map[string]any) map[string]any {
				o := map[string]any{
					"apiVersion": "catalog.clustarr.io/v1alpha1", "kind": life.kind,
					"metadata": map[string]any{"name": name, "namespace": diffNamespace},
				}
				maps.Copy(o, runtime.DeepCopyJSON(extra))
				return o
			}
			step := func(what string, f func(side) (*unstructured.Unstructured, error)) {
				t.Helper()
				t.Log(what)
				both(t, a, s, name, f)
			}
			step("create", func(x side) (*unstructured.Unstructured, error) {
				return x.create(object(map[string]any{"spec": life.spec, "status": life.statusA}), "clustarr")
			})
			step("two managers apply their parts of status", func(x side) (*unstructured.Unstructured, error) {
				return x.apply(object(map[string]any{"status": life.statusA}), "catalog", true)
			})
			step("", func(x side) (*unstructured.Unstructured, error) {
				return x.apply(object(map[string]any{"status": life.statusB}), "catalog-grab", true)
			})
			step("a spec patch: a new generation", func(x side) (*unstructured.Unstructured, error) {
				return x.patch(name, map[string]any{"spec": life.specPatch}, "clustarr-ui", false)
			})
			refused := func(what string, f func(side) (*unstructured.Unstructured, error)) {
				t.Helper()
				// Both must refuse it as invalid: two sides that both took a
				// write the kind forbids would agree, and prove nothing.
				for _, x := range []side{a, s} {
					_, err := f(x)
					require.Truef(t, apierrors.IsInvalid(err), "%s: want Invalid, got %v", what, err)
				}
				step(what, f)
			}
			refused("a spec patch the kind refuses", func(x side) (*unstructured.Unstructured, error) {
				return x.patch(name, map[string]any{"spec": life.badPatch}, "clustarr-ui", false)
			})
			refused("a status value the schema refuses", func(x side) (*unstructured.Unstructured, error) {
				return x.apply(object(map[string]any{"status": life.badStatus}), "catalog", true)
			})
			step("a manager releases its part", func(x side) (*unstructured.Unstructured, error) {
				return x.apply(object(map[string]any{"status": map[string]any{}}), "catalog-grab", true)
			})
			step("delete", func(x side) (*unstructured.Unstructured, error) { return nil, x.delete(name) })
		})
	}
}
