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

package subtitleplan

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Moved from app/caption/worker/fetch with imdbNumber (F5.1): the planner
// builds the providers' query.
func TestIMDbNumber(t *testing.T) {
	assert.Equal(t, "133093", imdbNumber("tt0133093"))
	assert.Equal(t, "", imdbNumber("nm0000206x"))
	assert.Equal(t, "", imdbNumber(""))
}
