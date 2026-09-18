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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/importlist"
)

func TestConfigValidateExactlyOne(t *testing.T) {
	url := "https://mdblist.com/lists/u/l"
	tests := map[string]struct {
		cfg     importlist.Config
		wantErr error
	}{
		"none set":     {cfg: importlist.Config{}, wantErr: importlist.ErrConfigExactlyOne},
		"trakt only":   {cfg: importlist.Config{Trakt: &importlist.TraktConfig{ListType: importlist.TraktListTypeWatchlist}}},
		"plex only":    {cfg: importlist.Config{Plex: &importlist.PlexConfig{}}},
		"mdblist only": {cfg: importlist.Config{Mdblist: &importlist.MdblistConfig{URL: url}}},
		"two set": {
			cfg:     importlist.Config{Plex: &importlist.PlexConfig{}, StevenLu: &importlist.StevenLuConfig{}},
			wantErr: importlist.ErrConfigExactlyOne,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}
