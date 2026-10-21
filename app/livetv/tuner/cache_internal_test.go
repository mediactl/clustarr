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

package tuner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAStreamURLIsCachedWithoutItsCredentials: the cache holds an Xtream
// stream URL with the account's username and password replaced, and puts
// them back when it is served; one naming the account any other way is not
// cached at all.
func TestAStreamURLIsCachedWithoutItsCredentials(t *testing.T) {
	for _, c := range []struct{ raw, tmpl, user, pass string }{
		{"http://tvload.win:80/live/USER/PASS/13427.ts", "http://tvload.win:80/live/{username}/{password}/13427.ts", "USER", "PASS"},
		{"http://panel.example/USER/PASS/9", "http://panel.example/{username}/{password}/9", "USER", "PASS"},
		{"http://panel.example/live/a%20b/p%2Fq/9.ts", "http://panel.example/live/{username}/{password}/9.ts", "a b", "p/q"},
		{"http://panel.example/play.php?username=USER&password=PASS&id=9", "http://panel.example/play.php?username={username}&password={password}&id=9", "USER", "PASS"},
		{"http://live.example/live/live/PASS/1.ts", "http://live.example/live/{username}/{password}/1.ts", "live", "PASS"},
		{"http://panel.example/other/9.ts", "http://panel.example/other/9.ts", "USER", "PASS"},
	} {
		tmpl, ok := templateURL(c.raw, c.user, c.pass)
		require.True(t, ok, c.raw)
		if c.user == "live" {
			// A username equal to another segment is templated there too, and
			// comes back the same.
			assert.Equal(t, "http://live.example/{username}/{username}/{password}/1.ts", tmpl)
		} else {
			assert.Equal(t, c.tmpl, tmpl, c.raw)
		}
		assert.Equal(t, c.raw, expandURL(tmpl, c.user, c.pass), "back again: %s", c.raw)
	}
	_, ok := templateURL("http://USER:PASS@panel.example/9.ts", "USER", "PASS")
	assert.False(t, ok, "userinfo is a credential the template cannot hide")
}
