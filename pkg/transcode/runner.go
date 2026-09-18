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

package transcode

import (
	"bufio"
	"math"
	"strconv"
	"strings"
)

// Progress mirrors TranscodeJobStatus.Progress's scaled-int fields exactly
// -- FPSMilli, SpeedMilli, OutTimeMillis, Percent, BitrateKbps -- so a
// controller can copy a Progress straight into the CRD status without a
// float anywhere in between.
type Progress struct {
	Frame         int64
	FPSMilli      int32
	SpeedMilli    int32
	OutTimeMillis int64
	BitrateKbps   int32
	Percent       int32
}

// ParseProgressStream reads ffmpeg's `-progress pipe:1` key=value lines from
// sc, accumulating one block per "progress=continue"/"progress=end" line
// (ffmpeg repeats the block at -stats_period cadence during a real run) and
// calling emit once per block. durationMillis is the source duration, used
// to derive Percent; 0 disables the percentage (Percent stays 0).
func ParseProgressStream(sc *bufio.Scanner, durationMillis int64, emit func(Progress)) error {
	fields := make(map[string]string)
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		fields[key] = value

		if key != "progress" || (value != "continue" && value != "end") {
			continue
		}
		emit(progressFromFields(fields, durationMillis))
		fields = make(map[string]string)
	}
	return sc.Err()
}

// progressFromFields converts one accumulated -progress block into a
// Progress. Parsing rules transcribed from docs/research/transcode.md §7:
// out_time_us is authoritative (out_time_ms is also microseconds despite
// its name, a documented ffmpeg quirk, and is ignored here); speed and fps
// are scaled by 1000; bitrate strips the "kbits/s" suffix and truncates to
// whole kbps; any missing or "N/A" value parses as 0, never panics.
func progressFromFields(fields map[string]string, durationMillis int64) Progress {
	p := Progress{
		Frame:         parseIntField(fields["frame"]),
		FPSMilli:      parseMilliField(fields["fps"]),
		OutTimeMillis: parseIntField(fields["out_time_us"]) / 1000,
		SpeedMilli:    parseMilliField(strings.TrimSuffix(fields["speed"], "x")),
		BitrateKbps:   parseBitrateKbps(fields["bitrate"]),
	}
	if durationMillis > 0 {
		pct := p.OutTimeMillis * 100 / durationMillis
		switch {
		case pct < 0:
			pct = 0
		case pct > 100:
			pct = 100
		}
		p.Percent = int32(pct)
	}
	return p
}

func parseIntField(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseMilliField scales a decimal ffmpeg progress value (fps, speed) by
// 1000. The float64 is a local, immediately-rounded intermediate -- never
// an exported value.
func parseMilliField(s string) int32 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int32(math.Round(f * 1000))
}

func parseBitrateKbps(s string) int32 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "kbits/s"))
	if s == "" || s == "N/A" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int32(f) // truncates towards zero, matching "truncates to whole kbps"
}
