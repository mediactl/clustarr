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

package scenemap_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/metadata/scenemap"
)

// Live TheXEM responses captured 2026-09-23, trimmed: all_72454 (Case
// Closed, the first 40 of 1,225 rows), all_80644 (whole), all_73388 (six
// rows mapping TVDB specials onto scene "1x0"), havemap (15 of 1,882 ids),
// allnames (five series), and the "no show" failure for TVDB 81797.
const fixtures = "../../../testdata/metadata/thexem/"

type xemServer struct {
	*httptest.Server
	mu     sync.Mutex
	counts map[string]int
	bodies map[string]string // path+"?"+id -> literal body, overriding fixtures
}

func serveXEM(t *testing.T) *xemServer {
	t.Helper()
	s := &xemServer{counts: map[string]int{}, bodies: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "tvdb", r.URL.Query().Get("origin"), "every call is origin=tvdb")
		key := r.URL.Path
		if id := r.URL.Query().Get("id"); id != "" {
			key += "?" + id
		}
		s.mu.Lock()
		s.counts[key]++
		body, literal := s.bodies[key]
		s.mu.Unlock()
		if literal {
			_, _ = w.Write([]byte(body))
			return
		}
		var name string
		switch key {
		case "/map/havemap":
			name = "havemap.json"
		case "/map/allNames":
			require.Equal(t, "1", r.URL.Query().Get("seasonNumbers"))
			name = "allnames.json"
		case "/map/all?72454", "/map/all?80644", "/map/all?73388":
			name = "all_" + r.URL.Query().Get("id") + ".json"
		default:
			name = "all_notfound.json"
		}
		b, err := os.ReadFile(fixtures + name)
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *xemServer) count(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[key]
}

func (s *xemServer) answer(key, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies[key] = body
}

func (s *xemServer) xem() *scenemap.XEM {
	return scenemap.NewXEM(scenemap.XEMConfig{HTTPClient: s.Client(), BaseURL: s.URL})
}

func TestMappingsCarryTheSceneAndTVDBNumbering(t *testing.T) {
	s := serveXEM(t)

	rows, err := s.xem().Mappings(context.Background(), 72454)

	require.NoError(t, err)
	require.Len(t, rows, 40)
	require.Equal(t, scenemap.Mapping{Scene: scenemap.Numbering{Season: 1, Episode: 1, Absolute: 1}, TVDB: scenemap.Numbering{Season: 1, Episode: 1, Absolute: 1}}, rows[0])
	require.Equal(t, scenemap.Mapping{
		Scene: scenemap.Numbering{Season: 1, Episode: 29, Absolute: 29},
		TVDB:  scenemap.Numbering{Season: 2, Episode: 1, Absolute: 29},
	}, rows[28], "Case Closed: scene numbers straight through; TVDB starts season 2 at episode 29")
}

func TestMappingsKeepSonarrsRowsAndDropItsInvalidOnes(t *testing.T) {
	s := serveXEM(t)

	specials, err := s.xem().Mappings(context.Background(), 73388)
	require.NoError(t, err)
	require.Len(t, specials, 6, `scene "1x0" is not all zeros; Sonarr keeps it and so does this`)
	require.Equal(t, scenemap.Numbering{Season: 0, Episode: 1}, specials[0].TVDB)

	s.answer("/map/all?1", `{"result":"success","data":[
	 {"scene":{"season":0,"episode":0,"absolute":0},"tvdb":{"season":1,"episode":1,"absolute":1}},
	 {"tvdb":{"season":1,"episode":2,"absolute":2}},
	 {"scene":{"season":1,"episode":3,"absolute":3}},
	 {"scene":{"season":1,"episode":4,"absolute":4},"tvdb":{"season":1,"episode":4,"absolute":4}}
	],"message":""}`)
	rows, err := s.xem().Mappings(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, []scenemap.Mapping{{Scene: scenemap.Numbering{Season: 1, Episode: 4, Absolute: 4}, TVDB: scenemap.Numbering{Season: 1, Episode: 4, Absolute: 4}}}, rows,
		"an all-zero scene, a missing scene and a missing tvdb are each dropped")
}

func TestAnUnmappedSeriesIsNoRowsAndNoError(t *testing.T) {
	s := serveXEM(t)

	rows, err := s.xem().Mappings(context.Background(), 81797)

	require.NoError(t, err, `TheXEM's "no show with the tvdb_id" failure is Sonarr's ignored error`)
	require.Empty(t, rows)
}

func TestAnyOtherFailureIsErrFailure(t *testing.T) {
	s := serveXEM(t)
	s.answer("/map/havemap", `{"result":"failure","data":[],"message":"database unavailable"}`)

	_, err := s.xem().HaveMap(context.Background())

	require.ErrorIs(t, err, scenemap.ErrFailure)
	require.Contains(t, err.Error(), "database unavailable")
}

func TestHaveMapParsesTheStringIDs(t *testing.T) {
	s := serveXEM(t)

	ids, err := s.xem().HaveMap(context.Background())

	require.NoError(t, err)
	require.Len(t, ids, 15)
	require.Contains(t, ids, int64(72454))
	require.Contains(t, ids, int64(195721))
}

func TestNamesCarryTheirSceneSeasons(t *testing.T) {
	s := serveXEM(t)

	names, err := s.xem().Names(context.Background())

	require.NoError(t, err)
	two, one := 2, 1
	require.ElementsMatch(t, []scenemap.SceneName{
		{Title: "Shinryaku!? Ika Musume", Season: &two},
		{Title: "Shinryaku! Ika Musume", Season: &one},
	}, names[195721])
	require.Contains(t, names[72454], scenemap.SceneName{Title: "Case Closed"}, "-1 is the whole series")
}

// TestNumberingConvertsToDecisionsEpisodeNumbering is the contract with
// pkg/decision: its EpisodeNumbering and this package's Numbering must
// stay convertible, so a consumer feeds TheXEM's rows into
// decision.Identity.SceneMappings without either package importing the
// other. If either side adds, drops, renames or retypes a field, this
// stops compiling.
func TestNumberingConvertsToDecisionsEpisodeNumbering(t *testing.T) {
	m := scenemap.Mapping{Scene: scenemap.Numbering{Season: 1, Episode: 29, Absolute: 29}, TVDB: scenemap.Numbering{Season: 2, Episode: 1, Absolute: 29}}

	got := decision.SceneMapping{Scene: decision.EpisodeNumbering(m.Scene), TVDB: decision.EpisodeNumbering(m.TVDB)}

	require.Equal(t, decision.SceneMapping{Scene: decision.EpisodeNumbering{Season: 1, Episode: 29, Absolute: 29}, TVDB: decision.EpisodeNumbering{Season: 2, Episode: 1, Absolute: 29}}, got)
}
