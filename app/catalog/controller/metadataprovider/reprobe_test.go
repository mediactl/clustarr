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

package metadataprovider

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// TheIntroDB's probe is a real lookup, counted against the key's
// allowance: it is probed every 6 hours, not every 15 minutes (~96 a
// day). A throttled provider of any type is probed again at its reset
// when that comes sooner, so Ready returns with the allowance.
func TestReprobeAfter(t *testing.T) {
	limited := func(d time.Duration) error { return &metadata.RateLimitedError{Provider: "x", RetryAfter: d} }
	tests := []struct {
		name string
		typ  catalogv1alpha1.MetadataProviderType
		err  error
		want time.Duration
	}{
		{"a healthy tmdb", catalogv1alpha1.MetadataProviderTMDB, nil, 15 * time.Minute},
		{"a healthy theintrodb", catalogv1alpha1.MetadataProviderTheIntroDB, nil, 6 * time.Hour},
		{"a failing theintrodb", catalogv1alpha1.MetadataProviderTheIntroDB, errors.New("boom"), 6 * time.Hour},
		{"theintrodb throttled for 90 minutes", catalogv1alpha1.MetadataProviderTheIntroDB, limited(90 * time.Minute), 90 * time.Minute},
		{"theintrodb throttled with no reset", catalogv1alpha1.MetadataProviderTheIntroDB, limited(0), 6 * time.Hour},
		{"tmdb throttled for 2 hours", catalogv1alpha1.MetadataProviderTMDB, limited(2 * time.Hour), 15 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, reprobeAfter(tt.typ, tt.err))
		})
	}
}
