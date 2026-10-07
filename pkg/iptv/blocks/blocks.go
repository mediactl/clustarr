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

// Package blocks splits a provider's active channel numbers into the runs
// Plex sees as one DVR each (Live TV spec §3.1, §4.5). PMS takes one
// device's channel map in one request, capped at 32 KiB of request line, so
// a lineup larger than ~450 channels is several devices; the runs are
// contiguous and sticky, so a channel rarely changes DVR and keeps the Plex
// recording rules made on it.
package blocks

import (
	"errors"
	"slices"

	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

const (
	// DefaultMaxChannels is spec.dvr.maxChannels' default.
	DefaultMaxChannels = 400
	// DefaultMaxBytes keeps a block's channel-map query under PMS's 32 KiB
	// request line, with room for the path (recorded 2026-10-07: 454
	// four-digit channels are a 32,738-byte path and query, 455 a 400).
	DefaultMaxBytes = 30000
	// FirstFill is how full a first packing leaves each block, in percent,
	// so channels can be added without a split.
	FirstFill = 75
	// MaxBlocks bounds status.blocks.
	MaxBlocks = 32
)

// ErrTooManyBlocks is a lineup that needs more than MaxBlocks blocks.
var ErrTooManyBlocks = errors.New("blocks: the lineup needs more than 32 blocks")

// Limits bound one block.
type Limits struct {
	MaxChannels int
	MaxBytes    int
}

// Block is one device's run of numbers. Start is its identity and lower
// boundary; Numbers are sorted by xmltv.CompareNumbers.
type Block struct {
	Start   string
	Numbers []string
}

// The channel map cluster-plex sends, under url.Values' encoding, when PMS's
// three identifiers are each the GuideNumber: "channelsEnabled=" and the
// numbers joined by "%2C", then per channel "&channelMapping%5B<n>%5D=<n>"
// and "&channelMappingByKey%5B<n>%5D=<n>". The order url.Values sorts them
// into does not change the length.
const (
	baseBytes       = len("channelsEnabled=") - len("%2C") // the first number has no comma
	perChannelBytes = len("%2C") + len("&channelMapping%5B%5D=") + len("&channelMappingByKey%5B%5D=")
)

// cost is what one number adds to the query: its comma and two pairs, each
// carrying the number twice.
func cost(n string) int { return perChannelBytes + 5*len(n) }

// MapBytes is the length of the channel-map query cluster-plex sends for
// numbers: 13 + Σ(52 + 5·len(n)). Verified against the recording: 454
// four-digit numbers give 32,701, which with the 37-byte path is the
// 32,738 PMS accepted.
func MapBytes(numbers []string) int {
	if len(numbers) == 0 {
		return len("channelsEnabled=")
	}
	n := baseBytes
	for _, s := range numbers {
		n += cost(s)
	}
	return n
}

// fits reports whether a block of count numbers and bytes stays within pct
// percent of lim.
func fits(count, bytes int, lim Limits, pct int) bool {
	return count*100 <= lim.MaxChannels*pct && bytes*100 <= lim.MaxBytes*pct
}

// Next renders the blocks for numbers (sorted, unique) from the previous
// blocks' starts:
//   - with no previous blocks, a first packing fills each block to FirstFill
//     percent of both limits, and always takes a block's first number;
//   - otherwise each number joins the last block whose Start is at or below
//     it, or the first block when it is below every Start (a Start is an
//     identity, never moved);
//   - a block with no numbers is dropped, and blocks are never merged;
//   - a block over either limit splits at its median: the median number
//     becomes the new block's Start, and only the upper half moves;
//   - more than MaxBlocks blocks is ErrTooManyBlocks.
func Next(prev []string, numbers []string, lim Limits) ([]Block, error) {
	if len(numbers) == 0 {
		return nil, nil
	}
	var out []Block
	if len(prev) == 0 {
		out = pack(numbers, lim)
	} else {
		out = assign(prev, numbers)
	}
	for i := 0; i < len(out); i++ {
		for len(out[i].Numbers) > 1 && !fits(len(out[i].Numbers), MapBytes(out[i].Numbers), lim, 100) {
			m := len(out[i].Numbers) / 2
			upper := Block{Start: out[i].Numbers[m], Numbers: slices.Clone(out[i].Numbers[m:])}
			out[i].Numbers = slices.Clip(out[i].Numbers[:m])
			out = slices.Insert(out, i+1, upper)
		}
	}
	if len(out) > MaxBlocks {
		return nil, ErrTooManyBlocks
	}
	return out, nil
}

// pack is a first packing: each block filled to FirstFill percent.
func pack(numbers []string, lim Limits) []Block {
	var out []Block
	cur := Block{Start: numbers[0]}
	bytes := baseBytes
	for _, n := range numbers {
		if len(cur.Numbers) > 0 && !fits(len(cur.Numbers)+1, bytes+cost(n), lim, FirstFill) {
			out = append(out, cur)
			cur, bytes = Block{Start: n}, baseBytes
		}
		cur.Numbers = append(cur.Numbers, n)
		bytes += cost(n)
	}
	return append(out, cur)
}

// assign places each number in the last block whose Start is at or below
// it, the first block taking anything below every Start, and drops blocks
// left empty.
func assign(prev []string, numbers []string) []Block {
	starts := slices.Clone(prev)
	slices.SortFunc(starts, xmltv.CompareNumbers)
	starts = slices.Compact(starts)
	bs := make([]Block, len(starts))
	for i, s := range starts {
		bs[i].Start = s
	}
	i := 0
	for _, n := range numbers {
		for i+1 < len(starts) && xmltv.CompareNumbers(starts[i+1], n) <= 0 {
			i++
		}
		bs[i].Numbers = append(bs[i].Numbers, n)
	}
	out := bs[:0]
	for _, b := range bs {
		if len(b.Numbers) > 0 {
			out = append(out, b)
		}
	}
	return out
}
