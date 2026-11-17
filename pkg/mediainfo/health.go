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

package mediainfo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrDamaged is a media file with parts of its timeline gone: read end to
// end, every audio and video stream skips the same stretches (Health). The
// import refuses such a file as the release's fault, as it refuses one
// that is no media (ErrNotMedia).
var ErrDamaged = errors.New("mediainfo: damaged media")

const (
	// MinStreamGap is the shortest step between two packets of one stream
	// that counts as a gap in it. Audio packets are tens of milliseconds
	// apart and video frames one frame; a variable-rate video can hold a
	// still for longer, which is why a gap in one stream alone is no hole.
	MinStreamGap = 500 * time.Millisecond
	// MinHole is the shortest stretch every stream skips that counts.
	MinHole = 250 * time.Millisecond
	// MinTail is the shortest stretch between a stream's last packet and
	// the container's stated end that counts as a gap in it. It is longer
	// than MinStreamGap because a container's length is its longest
	// track's, and a subtitle or a chapter may outlast the picture and the
	// sound by a few seconds in a file that is whole.
	MinTail = 10 * time.Second
	// MaxMissing is how much of a file's timeline may be gone before it is
	// damaged. One lost usenet article of a 1080p WEB-DL is about a second;
	// the two files this was written for had lost 373 s of 1,435 and more
	// (Fena: Pirate Princess S01E01 and E11, talos, 2026-11-17).
	MaxMissing = 2 * time.Second
)

// Health is what reading a whole file found of its timeline: the stretches
// no audio or video stream has a packet in. A hole in the bytes -- a usenet
// article that never arrived, a download cut short -- takes every stream's
// packets with it, and the demuxer carries on at the next thing it
// recognises; a file whose streams are merely uneven (a still held in a
// variable-rate video, an audio track that starts late) has no stretch they
// all skip.
type Health struct {
	// Judged is false when the file was not judged: a container whose
	// timestamps may jump by design (MPEG-TS), or a file without both an
	// audio and a video stream.
	Judged bool
	// Duration is the container's own statement of its length.
	Duration time.Duration
	// Holes is how many stretches every stream skips, Missing what they
	// come to, Largest the longest.
	Holes   int
	Missing time.Duration
	Largest time.Duration
}

// Damaged reports whether more of the timeline is gone than MaxMissing.
func (h Health) Damaged() bool { return h.Judged && h.Missing > MaxMissing }

// Err is ErrDamaged with what is missing for a damaged file, nil otherwise.
func (h Health) Err() error {
	if !h.Damaged() {
		return nil
	}
	return fmt.Errorf("%w: %s of %s is missing from every stream, in %d places (the longest %s)",
		ErrDamaged, h.Missing.Round(time.Second), h.Duration.Round(time.Second), h.Holes, h.Largest.Round(100*time.Millisecond))
}

// HealthProber is a Prober that can also read a file end to end for its
// Health. The read is the whole file's bytes, so it is bounded by the
// caller's context, not by a probe's deadline.
type HealthProber interface {
	Health(ctx context.Context, path string) (Health, error)
}

// Span is a stretch of a timeline.
type Span struct{ From, To time.Duration }

// Timeline collects one stream's packet timestamps, in the order the
// demuxer hands them out, into the gaps between them.
type Timeline struct {
	seen bool
	last time.Duration
	gaps []Span
}

// Add records a packet at ts. Timestamps need not rise: a video's
// presentation times run ahead of and behind its decode order by a few
// frames, so a gap is a step past the latest one seen.
func (t *Timeline) Add(ts time.Duration) {
	switch {
	case !t.seen:
		t.seen, t.last = true, ts
	case ts > t.last:
		if ts-t.last >= MinStreamGap {
			t.gaps = append(t.gaps, Span{t.last, ts})
		}
		t.last = ts
	}
}

// Holes is the health of a file whose audio and video streams have the
// timelines given and whose container ends at end: the stretches every
// stream has a gap over, the tail past every stream's last packet among
// them (a file cut short still states its full length).
func Holes(duration, end time.Duration, streams []*Timeline) Health {
	h := Health{Duration: duration}
	var common []Span
	n := 0
	for _, t := range streams {
		if t == nil || !t.seen {
			continue
		}
		gaps := t.gaps
		if end-t.last >= MinTail {
			gaps = append(gaps[:len(gaps):len(gaps)], Span{t.last, end})
		}
		if n == 0 {
			common = gaps
		} else {
			common = intersect(common, gaps)
		}
		n++
	}
	if n == 0 {
		return h
	}
	h.Judged = true
	for _, s := range common {
		d := s.To - s.From
		if d < MinHole {
			continue
		}
		h.Holes++
		h.Missing += d
		h.Largest = max(h.Largest, d)
	}
	return h
}

// intersect is the stretches a and b share; both are in order and neither
// overlaps itself.
func intersect(a, b []Span) []Span {
	var out []Span
	for i, j := 0, 0; i < len(a) && j < len(b); {
		from, to := max(a[i].From, b[j].From), min(a[i].To, b[j].To)
		if from < to {
			out = append(out, Span{from, to})
		}
		if a[i].To < b[j].To {
			i++
		} else {
			j++
		}
	}
	return out
}

// Tracker follows the packets of one file's audio and video streams as a
// demuxer hands them out, for the file's Health at the end: the prober's
// end-to-end read and the transcode engine's own demux both feed one. A nil
// Tracker takes packets and judges nothing.
type Tracker struct {
	duration, end time.Duration
	lines         map[int]*trackLine
	video, audio  bool
}

type trackLine struct {
	t    Timeline
	tick float64 // seconds per timestamp unit
}

// NewTracker is a Tracker for a file in the container format names (the
// demuxer's name, "matroska,webm"), duration long, whose timestamps start
// at start. Only Matroska and MP4 are judged: their timestamps run on from
// the file's start, where a transport stream's may jump by design. It is
// nil for any other container, and for one that states no length.
func NewTracker(format string, duration, start time.Duration) *Tracker {
	if duration <= 0 || !(strings.Contains(format, "matroska") || strings.Contains(format, "mp4")) {
		return nil
	}
	return &Tracker{duration: duration, end: start + duration, lines: map[int]*trackLine{}}
}

// Stream names one audio or video stream to follow: its index, and its time
// base as num/den seconds per timestamp unit. Attached pictures, subtitles
// and data streams are not named.
func (t *Tracker) Stream(index int, video bool, num, den int32) {
	if t == nil || den == 0 {
		return
	}
	if video {
		t.video = true
	} else {
		t.audio = true
	}
	t.lines[index] = &trackLine{tick: float64(num) / float64(den)}
}

// Add records a packet of stream index at ts, in the stream's own units.
// A stream that was not named is ignored.
func (t *Tracker) Add(index int, ts int64) {
	if t == nil {
		return
	}
	if l := t.lines[index]; l != nil {
		l.t.Add(time.Duration(float64(ts) * l.tick * float64(time.Second)))
	}
}

// Health is the file's health from the packets recorded. A file without
// both an audio and a video stream is not judged: one stream alone may
// skip time by design.
func (t *Tracker) Health() Health {
	if t == nil {
		return Health{}
	}
	if !t.video || !t.audio {
		return Health{Duration: t.duration}
	}
	streams := make([]*Timeline, 0, len(t.lines))
	for _, l := range t.lines {
		streams = append(streams, &l.t)
	}
	return Holes(t.duration, t.end, streams)
}
