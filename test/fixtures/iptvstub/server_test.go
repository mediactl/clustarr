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

package iptvstub_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
	"github.com/mediactl/clustarr/test/fixtures/iptvstub"
)

func TestTheStubServesAPlaylistAGuideAndEndlessStreams(t *testing.T) {
	srv := httptest.NewServer(nil)
	srv.Config.Handler = iptvstub.NewHandler(srv.URL)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/get.php?username=e2e&password=secret&type=m3u_plus&output=mpegts")
	require.NoError(t, err)
	pl, err := m3u.Parse(res.Body)
	_ = res.Body.Close()
	require.NoError(t, err)
	require.Len(t, pl.Entries, 3, "through the real parser")
	for i, e := range pl.Entries {
		assert.Equal(t, "E2E", e.Group)
		assert.Equal(t, "t:e2e."+string(rune('1'+i)), e.Key)
		assert.Equal(t, srv.URL+"/live/e2e/secret/"+string(rune('1'+i))+".ts", e.URL)
	}

	res, err = http.Get(srv.URL + "/guide.xml")
	require.NoError(t, err)
	now := time.Now()
	g, err := xmltv.Parse(res.Body, map[string]bool{"e2e.1": true}, now, now.Add(24*time.Hour))
	_ = res.Body.Close()
	require.NoError(t, err)
	require.Len(t, g.Channels, 3)
	assert.NotEmpty(t, g.Programmes["e2e.1"], "a programme airs now")

	res, err = http.Get(srv.URL + "/live/e2e/secret/1.ts")
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	buf := make([]byte, 188*64)
	_, err = io.ReadFull(res.Body, buf)
	require.NoError(t, err)
	for i := 0; i < len(buf); i += 188 {
		require.Equal(t, byte(0x47), buf[i], "188-aligned TS packets")
	}

	stats, err := http.Get(srv.URL + "/stats")
	require.NoError(t, err)
	var got struct {
		Connections map[string]int `json:"connections"`
	}
	require.NoError(t, json.NewDecoder(stats.Body).Decode(&got))
	_ = stats.Body.Close()
	assert.Equal(t, 1, got.Connections["1"], "one open upstream for channel 1")
}
