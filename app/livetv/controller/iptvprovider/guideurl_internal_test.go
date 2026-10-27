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

package iptvprovider

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateGuideURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://iptv-epg.org/files/0123/epg-us.xml":                                  true,
		"http://panel.example:8080/xmltv.php?username={username}&password={password}": true,
		" https://example.com/guide.xml ":                                             true,
		"ftp://example.com/guide.xml":                                                 false,
		"https:///guide.xml":                                                          false,
		"https://user:pass@example.com/guide.xml":                                     false,
		"example.com/guide.xml":                                                       false,
	} {
		err := ValidateGuideURL(raw)
		assert.Equal(t, ok, err == nil, "%q: %v", raw, err)
	}
}
