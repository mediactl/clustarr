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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download"
)

// A transfer whose granted server lacks the post moves to the primary its
// chases recover it from: the rest of the post is there too. On talos
// (2026-10-28) transfers granted on frugal and newsdemon fetched what those
// lacked through easynews's chase share -- three connections, a tenth of its
// thirty -- while easynews carried no transfer at all, and the engine moved
// 16-20 MB/s.
func TestATransferMovesToThePrimaryItsChasesRecoverFrom(t *testing.T) {
	first := newStubServer(t)
	second := newStubServer(t)
	const parts = 200
	payloads := make([][]byte, parts)
	for i := range payloads {
		payloads[i] = partPayload(byte(i+1), 64)
	}
	nzb := buildNZB(t, first, "Moved", []fileSpec{{name: "movie.mkv", parts: payloads}})
	second.mirror(first)
	for i := range parts {
		first.refuse[fmt.Sprintf("f0-p%d@clustarr.test", i)] = 430
	}

	c, _, _ := newTestClient(t, Config{
		Providers: []Provider{first.provider("first", 2, 1), second.provider("second", 2, 2)},
		Ledger:    NewLocalLedger(),
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Moved", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, parts, second.servedTotal())
	require.Less(t, first.askedTotal(), parts/2,
		"the transfer stayed on the server that holds none of the post: it was asked for %d of %d articles", first.askedTotal(), parts)
}

// A chase that recovers a stray article or two moves nothing: the granted
// server holds the post.
func TestAFewChasedArticlesLeaveTheTransferWhereItIs(t *testing.T) {
	first := newStubServer(t)
	second := newStubServer(t)
	const parts = 200
	payloads := make([][]byte, parts)
	for i := range payloads {
		payloads[i] = partPayload(byte(i+1), 64)
	}
	nzb := buildNZB(t, first, "Stays", []fileSpec{{name: "movie.mkv", parts: payloads}})
	second.mirror(first)
	first.refuse["f0-p7@clustarr.test"] = 430
	first.refuse["f0-p8@clustarr.test"] = 430

	c, _, _ := newTestClient(t, Config{
		Providers: []Provider{first.provider("first", 2, 1), second.provider("second", 2, 2)},
		Ledger:    NewLocalLedger(),
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Stays", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, 2, second.servedTotal(), "the second server served only the two the first lacked")
}
