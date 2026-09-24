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

package grabarr

import "testing"

// TestSplitEngineIdentity proves splitEngineIdentity mirrors
// grabarr/controller/downloadclient/workload.go's own encoding
// ("${HOSTNAME##*-}" strips everything after the last hyphen to get the
// ordinal), including the case that shape exists for: a DownloadClient name
// that itself contains hyphens.
func TestSplitEngineIdentity(t *testing.T) {
	cases := []struct {
		engine   string
		wantName string
		wantOK   bool
	}{
		{"torrents-0", "torrents", true},
		{"sabnzbd-0", "sabnzbd", true},
		{"my-torrent-client-12", "my-torrent-client", true},
		{"", "", false},
		{"noordinal", "", false},
		{"-0", "", false},
		{"torrents-", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.engine, func(t *testing.T) {
			gotName, gotOK := splitEngineIdentity(tc.engine)
			if gotOK != tc.wantOK {
				t.Fatalf("splitEngineIdentity(%q) ok = %v, want %v", tc.engine, gotOK, tc.wantOK)
			}
			if gotOK && gotName != tc.wantName {
				t.Fatalf("splitEngineIdentity(%q) name = %q, want %q", tc.engine, gotName, tc.wantName)
			}
		})
	}
}
