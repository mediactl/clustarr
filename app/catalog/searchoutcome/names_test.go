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

package searchoutcome_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/mediactl/clustarr/app/catalog/searchoutcome"
)

// The reserved names share status.indexerOutcomes (listType=map keyed by
// name) with real Indexers, so none may be a name an Indexer can have.
func TestReservedOutcomeNamesCannotBeIndexerNames(t *testing.T) {
	for _, name := range []string{
		searchoutcome.WorkerOutcomeName,
		searchoutcome.TruncatedOutcomeName,
		searchoutcome.UnnamedOutcomeName(1),
		searchoutcome.UnnamedOutcomeName(12),
	} {
		assert.NotEmpty(t, validation.IsDNS1123Subdomain(name), "%q must not be a valid Indexer name", name)
	}
}

// Live Search objects carry these strings in status.indexerOutcomes.
func TestOutcomeNamesAreStable(t *testing.T) {
	assert.Equal(t, "catalogarr/search-worker", searchoutcome.WorkerOutcomeName)
	assert.Equal(t, "catalogarr/truncated", searchoutcome.TruncatedOutcomeName)
	assert.Equal(t, "catalogarr/unnamed-indexer-3", searchoutcome.UnnamedOutcomeName(3))
}
