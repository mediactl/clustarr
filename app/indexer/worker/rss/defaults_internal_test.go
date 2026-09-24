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

package rss

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
)

// The default restated in worker.go must equal what controller-gen actually
// generated, or the floor drifts away from the value every apiserver-created
// Indexer gets. Read from the generated schema rather than from the marker,
// because the schema is what is installed.
//
// The two are compared as DURATIONS, not as strings: the marker is written
// "15m" and time.Duration renders "15m0s", so a string comparison would fail
// on a pair that agrees.
func TestRssIntervalDefaultMatchesTheGeneratedCRD(t *testing.T) {
	raw, err := os.ReadFile("../../../../config/crd/bases/index.clustarr.io_indexers.yaml")
	require.NoError(t, err)

	var crd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Properties map[string]struct {
									Default string `json:"default"`
								} `json:"properties"`
							} `json:"spec"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	require.NotEmpty(t, crd.Spec.Versions, "the CRD was not parsed; run `make manifests`")

	got := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec.Properties["rssInterval"].Default
	require.NotEmpty(t, got, "spec.rssInterval lost its +kubebuilder:default")
	d, err := time.ParseDuration(got)
	require.NoError(t, err)
	require.Equal(t, defaultRssInterval, d,
		"worker.go's defaultRssInterval no longer mirrors spec.rssInterval's +kubebuilder:default")
}

// A kubebuilder default fills an ABSENT field, and metav1.Duration is a
// struct that `omitempty` cannot drop, so every Indexer created through a
// typed Go client arrives carrying "0s" and is never defaulted. Scheduling on
// that unfloored puts the next poll at `now`, which the broker can redeliver
// immediately: one indexer polled as fast as JetStream will hand the task
// back.
func TestRssIntervalFloorsAtTheCRDDefault(t *testing.T) {
	tests := []struct {
		name string
		in   metav1.Duration
		want time.Duration
	}{
		{"a typed client's zero", metav1.Duration{}, defaultRssInterval},
		{"a negative interval", metav1.Duration{Duration: -time.Second}, defaultRssInterval},
		{"an operator's own interval wins", metav1.Duration{Duration: 30 * time.Minute}, 30 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := &indexv1alpha1.Indexer{Spec: indexv1alpha1.IndexerSpec{RssInterval: tt.in}}
			require.Equal(t, tt.want, rssInterval(idx))
		})
	}
}
