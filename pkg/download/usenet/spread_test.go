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

package usenet

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download"
)

// MaxActive's transfer slots are divided among the primaries by their
// connections, at least one each, and a transfer's share of a server is
// its connections over its slots: every primary carries transfers at once
// (owner, 2026-10-28), where each transfer used to take limit / MaxActive
// of the first primary with room.
func TestPrimarySharesSplitTheSlotsByConnections(t *testing.T) {
	p := func(name string, conns int, backup bool) Provider {
		return Provider{Name: name, Connections: conns, Backup: backup}
	}
	require.Equal(t, map[string]int{"solo": 4}, primaryShares([]Provider{p("solo", 8, false)}, 2),
		"one primary: limit / MaxActive, as before")
	talos := []Provider{p("frugal", 50, false), p("easynews", 30, false), p("newsdemon", 20, false), p("frugal-bonus", 20, true)}
	require.Equal(t, map[string]int{"frugal": 16, "easynews": 15, "newsdemon": 20}, primaryShares(talos, 6),
		"six slots: three, two and one; a backup has none")
	require.Equal(t, map[string]int{"frugal": 50, "easynews": 30, "newsdemon": 20}, primaryShares(talos, 2),
		"fewer slots than primaries: one each")
	require.Equal(t, map[string]int{"tiny": 1}, primaryShares([]Provider{p("tiny", 0, false)}, 4))
}

// waitGrants waits until every id holds a transfer grant at once and
// returns them.
func waitGrants(t *testing.T, c *Client, ids ...string) []download.Grant {
	t.Helper()
	var out []download.Grant
	require.Eventually(t, func() bool {
		out = out[:0]
		for _, id := range ids {
			it, err := c.Get(context.Background(), id)
			if err != nil || it.Grant == nil {
				return false
			}
			out = append(out, *it.Grant)
		}
		return true
	}, 5*time.Second, 5*time.Millisecond, "every transfer holds a grant at once")
	return out
}

// Two transfers on two primaries of four connections each, MaxActive 2:
// each primary is one slot, so the second transfer takes the second
// server's four connections rather than half of the first's.
func TestTransfersSpreadAcrossThePrimaries(t *testing.T) {
	a, b := newStubServer(t), newStubServer(t)
	a.bodyDelay, b.bodyDelay = 20*time.Millisecond, 20*time.Millisecond
	one := buildNZBWithPrefix(t, a, "One", "one-", []fileSpec{{name: "one.mkv", parts: manyParts(1, 60, 300)}})
	two := buildNZBWithPrefix(t, a, "Two", "two-", []fileSpec{{name: "two.mkv", parts: manyParts(2, 60, 300)}})
	b.mirror(a)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{a.provider("a", 4, 1), b.provider("b", 4, 2)}, MaxActive: 2, PipelineDepth: 1})
	ctx := context.Background()

	id1, err := c.Add(ctx, download.AddRequest{Name: "One", Payload: one})
	require.NoError(t, err)
	id2, err := c.Add(ctx, download.AddRequest{Name: "Two", Payload: two})
	require.NoError(t, err)
	require.ElementsMatch(t, []download.Grant{{Server: "a", Connections: 4}, {Server: "b", Connections: 4}}, waitGrants(t, c, id1, id2))

	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, id1).Status)
	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, id2).Status)
}

// A transfer takes a full share on another primary before a leftover: with
// 11 and 6 connections and three slots (two of five, one of six), the third
// transfer takes the second server's six, not the first server's last one.
func TestAFullShareElsewhereComesBeforeALeftover(t *testing.T) {
	a, b := newStubServer(t), newStubServer(t)
	a.bodyDelay, b.bodyDelay = 20*time.Millisecond, 20*time.Millisecond
	var nzbs [][]byte
	for i, name := range []string{"One", "Two", "Three"} {
		nzbs = append(nzbs, buildNZBWithPrefix(t, a, name, name+"-", []fileSpec{{name: name + ".mkv", parts: manyParts(byte(i+1), 80, 300)}}))
	}
	b.mirror(a)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{a.provider("a", 11, 1), b.provider("b", 6, 2)}, MaxActive: 3, PipelineDepth: 1})
	ctx := context.Background()

	var ids []string
	for i, name := range []string{"One", "Two", "Three"} {
		id, err := c.Add(ctx, download.AddRequest{Name: name, Payload: nzbs[i]})
		require.NoError(t, err)
		ids = append(ids, id)
	}
	require.ElementsMatch(t, []download.Grant{{Server: "a", Connections: 5}, {Server: "a", Connections: 5}, {Server: "b", Connections: 6}},
		waitGrants(t, c, ids...))
	for _, id := range ids {
		require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, id).Status)
	}
}
