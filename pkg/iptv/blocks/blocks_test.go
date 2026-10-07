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

package blocks_test

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/iptv/blocks"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

var lim = blocks.Limits{MaxChannels: blocks.DefaultMaxChannels, MaxBytes: blocks.DefaultMaxBytes}

// run is the numbers from..to step, as strings.
func run(from, to, step int) []string {
	var out []string
	for n := from; n <= to; n += step {
		out = append(out, fmt.Sprint(n))
	}
	return out
}

func starts(bs []blocks.Block) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Start
	}
	return out
}

func all(bs []blocks.Block) []string {
	var out []string
	for _, b := range bs {
		out = append(out, b.Numbers...)
	}
	return out
}

func requireContiguous(t *testing.T, bs []blocks.Block) {
	t.Helper()
	for i := range bs {
		require.NotEmpty(t, bs[i].Numbers)
		assert.Equal(t, bs[i].Start, bs[i].Numbers[0], "a block starts at its first number, or holds numbers below the first start")
		if i > 0 {
			prev := bs[i-1].Numbers[len(bs[i-1].Numbers)-1]
			assert.Negative(t, xmltv.CompareNumbers(prev, bs[i].Numbers[0]), "every number of block %d is above every number of block %d", i, i-1)
		}
	}
}

func TestTheRealPlaylistsChannelsAreOneBlock(t *testing.T) {
	f, err := os.Open("../../../test/data/iptv/m3u_plus.m3u")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	pl, err := m3u.Parse(f)
	require.NoError(t, err)
	nums := run(1000, 1000+len(pl.Entries)-1, 1)
	bs, err := blocks.Next(nil, nums, lim)
	require.NoError(t, err)
	assert.Equal(t, []blocks.Block{{Start: "1000", Numbers: nums}}, bs)
}

func TestAFirstPackingFillsEachBlockTo75PercentDeterministically(t *testing.T) {
	nums := run(1000, 1999, 1)
	bs, err := blocks.Next(nil, nums, lim)
	require.NoError(t, err)
	assert.Equal(t, []string{"1000", "1300", "1600", "1900"}, starts(bs), "300 of 400 channels each")
	assert.Equal(t, nums, all(bs))
	requireContiguous(t, bs)
	again, err := blocks.Next(nil, nums, lim)
	require.NoError(t, err)
	assert.Equal(t, bs, again)
}

func TestLongNumbersCloseABlockOnTheByteBudgetFirst(t *testing.T) {
	var nums []string
	for i := range 1000 {
		nums = append(nums, fmt.Sprintf("%d.%d", 100000+i, 1000+i)) // 11 characters
	}
	bs, err := blocks.Next(nil, nums, lim)
	require.NoError(t, err)
	for _, b := range bs[:len(bs)-1] {
		assert.Less(t, len(b.Numbers), 300, "bytes, not channels, closed block %s", b.Start)
		assert.LessOrEqual(t, blocks.MapBytes(b.Numbers), blocks.DefaultMaxBytes*blocks.FirstFill/100)
	}
	requireContiguous(t, bs)
}

func TestAChannelAddedInsideABlockStaysThereAndLaterStartsHold(t *testing.T) {
	nums := run(1000, 2998, 2) // even numbers: room between them
	first, err := blocks.Next(nil, nums, lim)
	require.NoError(t, err)
	prev := starts(first)

	added := append(slices.Clone(nums), "1651")
	slices.SortFunc(added, xmltv.CompareNumbers)
	next, err := blocks.Next(prev, added, lim)
	require.NoError(t, err)
	assert.Equal(t, prev, starts(next), "no start moves")
	for i, b := range next {
		want := slices.Clone(first[i].Numbers)
		if slices.Contains(b.Numbers, "1651") {
			want = append(want, "1651")
			slices.SortFunc(want, xmltv.CompareNumbers)
			assert.Equal(t, "1600", b.Start)
		}
		assert.Equal(t, want, b.Numbers, "block %s holds the same channels", b.Start)
	}
}

func TestABlockOverALimitSplitsAtItsMedianAndOnlyTheUpperHalfMoves(t *testing.T) {
	small := blocks.Limits{MaxChannels: 10, MaxBytes: blocks.DefaultMaxBytes}
	bs, err := blocks.Next([]string{"1", "100"}, append(run(1, 11, 1), "100"), small)
	require.NoError(t, err)
	assert.Equal(t, []blocks.Block{
		{Start: "1", Numbers: run(1, 5, 1)},
		{Start: "6", Numbers: run(6, 11, 1)},
		{Start: "100", Numbers: []string{"100"}},
	}, bs)
}

func TestAnEmptiedBlockIsDroppedAndNothingIsMerged(t *testing.T) {
	bs, err := blocks.Next([]string{"1", "5", "9"}, []string{"1", "2", "9", "10"}, lim)
	require.NoError(t, err)
	assert.Equal(t, []blocks.Block{{Start: "1", Numbers: []string{"1", "2"}}, {Start: "9", Numbers: []string{"9", "10"}}}, bs)
}

func TestANumberBelowTheFirstStartJoinsTheFirstBlock(t *testing.T) {
	bs, err := blocks.Next([]string{"10", "20"}, []string{"5", "10", "20"}, lim)
	require.NoError(t, err)
	assert.Equal(t, []blocks.Block{{Start: "10", Numbers: []string{"5", "10"}}, {Start: "20", Numbers: []string{"20"}}}, bs,
		"the start is an identity; it does not move down")
}

func TestMoreThan32BlocksIsAnError(t *testing.T) {
	tiny := blocks.Limits{MaxChannels: 4, MaxBytes: 1 << 30} // a first packing takes 3 a block
	bs, err := blocks.Next(nil, run(1, 96, 1), tiny)
	require.NoError(t, err)
	assert.Len(t, bs, 32)
	_, err = blocks.Next(nil, run(1, 97, 1), tiny)
	require.ErrorIs(t, err, blocks.ErrTooManyBlocks)
	_, err = blocks.Next(starts(bs), run(1, 129, 1), tiny)
	require.ErrorIs(t, err, blocks.ErrTooManyBlocks, "splits past 32 blocks too")
}

func TestNoNumbersIsNoBlocks(t *testing.T) {
	bs, err := blocks.Next([]string{"1"}, nil, lim)
	require.NoError(t, err)
	assert.Empty(t, bs)
}

// query is the channel map cluster-plex sends for numbers.
func query(numbers []string) string {
	q := url.Values{}
	for _, n := range numbers {
		q.Set("channelMappingByKey["+n+"]", n)
		q.Set("channelMapping["+n+"]", n)
	}
	q.Set("channelsEnabled", strings.Join(numbers, ","))
	return q.Encode()
}

func TestMapBytesIsTheQueryClusterPlexSends(t *testing.T) {
	for _, nums := range [][]string{nil, {"7"}, run(1000, 1453, 1), {"2.1", "2.10", "10", "105.3", "99999"}} {
		assert.Equal(t, len(query(nums)), blocks.MapBytes(nums), "%d numbers", len(nums))
	}
	assert.Equal(t, 32701, blocks.MapBytes(run(1000, 1453, 1)), "454 four-digit numbers, the largest map PMS took (recorded)")
}
