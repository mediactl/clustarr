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

package release_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/release"
)

func TestCleanTitleIsStableAcrossArticleCaseAndPunctuation(t *testing.T) {
	tests := []struct{ a, b string }{
		{"The Matrix", "the matrix"},
		{"The Matrix", "Matrix, The"},
		{"The  Matrix!", "The Matrix"},
		{"Amélie", "Amelie"},
	}
	for _, tt := range tests {
		assert.Equal(t, release.CleanTitle(tt.a), release.CleanTitle(tt.b),
			"%q and %q must clean to the same key", tt.a, tt.b)
	}
}

func TestNormalizeStripsAccentsPreservesCase(t *testing.T) {
	assert.Equal(t, "Amelie", release.Normalize("Amélie"))
}
