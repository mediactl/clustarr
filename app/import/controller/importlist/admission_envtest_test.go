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

package importlist_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	worker "github.com/mediactl/clustarr/app/import/worker/importlist"
)

// TestAdmissionMatchesYieldableKinds holds ImportListSpec's three R-10 CEL
// rules (task X14 applied them, verbatim from x7b-report) to the table the
// controller and the worker enforce, worker.YieldableKinds, for every
// provider and every kind the enum admits. The two encode one decision --
// a list may name only kinds its provider can yield -- and nothing else ties
// them together: a provider added to one and not the other would either be
// admitted and then refused by the controller as UnsupportedKind, or refused
// at admission for a kind the worker could sync.
//
// Each create is a server-side dry run against the real apiserver, so the
// CRD's compiled CEL is what answers.
func TestAdmissionMatchesYieldableKinds(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()

	providers := map[string]func(*catalogv1alpha1.ImportListSpec){
		"trakt": func(s *catalogv1alpha1.ImportListSpec) {
			s.Trakt = &catalogv1alpha1.TraktList{ListType: catalogv1alpha1.TraktListTypeWatchlist, Username: "u"}
		},
		"plex":     func(s *catalogv1alpha1.ImportListSpec) { s.Plex = &catalogv1alpha1.PlexWatchlist{} },
		"tmdb":     func(s *catalogv1alpha1.ImportListSpec) { s.Tmdb = &catalogv1alpha1.TmdbList{ListID: ptr.To("1")} },
		"mdblist":  func(s *catalogv1alpha1.ImportListSpec) { s.Mdblist = &catalogv1alpha1.MdbList{URL: "http://m/l.json"} },
		"stevenLu": func(s *catalogv1alpha1.ImportListSpec) { s.StevenLu = &catalogv1alpha1.StevenLu{} },
		"imdbCSV": func(s *catalogv1alpha1.ImportListSpec) {
			s.ImdbCSV = &catalogv1alpha1.CSVList{ConfigMapRef: corev1.LocalObjectReference{Name: "csv"}}
		},
		"custom": func(s *catalogv1alpha1.ImportListSpec) {
			s.Custom = &catalogv1alpha1.CustomList{URL: "http://c/l.json"}
		},
	}
	for _, kind := range []catalogv1alpha1.ArrKind{
		catalogv1alpha1.ArrKindRadarr, catalogv1alpha1.ArrKindSonarr, catalogv1alpha1.ArrKindLidarr,
		catalogv1alpha1.ArrKindReadarr, catalogv1alpha1.ArrKindClustarr,
	} {
		providers["arr/"+string(kind)] = func(s *catalogv1alpha1.ImportListSpec) {
			s.Arr = &catalogv1alpha1.ArrList{BaseURL: "http://arr", Kind: kind}
		}
	}
	kinds := []commonv1.MediaKind{
		commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindAlbum,
		commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindComic,
	}

	var admitted, refused int
	for name, set := range providers {
		for _, kind := range kinds {
			t.Run(fmt.Sprintf("%s/%s", name, kind), func(t *testing.T) {
				spec := catalogv1alpha1.ImportListSpec{
					Kinds:    []string{string(kind)},
					Defaults: catalogv1alpha1.ListDefaults{QualityProfileRef: "q", RootFolderRef: "r"},
				}
				set(&spec)
				il := &catalogv1alpha1.ImportList{
					ObjectMeta: metav1.ObjectMeta{Name: "r10-probe", Namespace: "default"},
					Spec:       spec,
				}
				err := c.Create(ctx, il, client.DryRunAll)
				if worker.CanYield(spec, kind) {
					admitted++
					require.NoError(t, err, "admission refused a kind worker.YieldableKinds says %s can yield", name)
					return
				}
				refused++
				require.Error(t, err, "admission accepted a kind worker.YieldableKinds says %s cannot yield", name)
				require.Contains(t, err.Error(), "yield", "refused for another reason than R-10: %v", err)
			})
		}
	}
	require.Positive(t, admitted, "no provider/kind pair was admitted; the fixture specs are invalid")
	require.Positive(t, refused, "no provider/kind pair was refused; the R-10 rules are not installed")
}
