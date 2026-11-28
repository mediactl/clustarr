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

package relay

import "time"

// DefaultHold is how much of a stream's media the relay holds back behind
// the newest byte its upstream sent, released to the viewers in real time.
// tvload.win's CDN (2026-11-27) sends a stream as whole HLS segments of
// about 10 s, each in under a second, with up to 21 s between: in three
// minutes of CNN the media fell 23 s behind the wall clock at worst. A
// replay of that timeline ran dry five times with a 20 s hold, never with
// 25 s (1.9 s left at worst), and kept 6.9 s at worst with 30 s.
const DefaultHold = 30 * time.Second

// clockHz is the 90 kHz clock a PCR's base and a presentation time count.
const clockHz = 90_000

// maxClockStep is the largest step from one PCR to the next that is read
// as the stream's own time passing (PCRs come at least every 100 ms). A
// bigger step, or one back, is a discontinuity -- an unaligned splice, an
// encoder restart -- and paces as no time at all.
const maxClockStep = 2 * clockHz

// markSpacing thins the marks: a PCR less than this after the last mark
// kept moves the head but is not kept, so a release moves about ten times
// a second.
const markSpacing = clockHz / 10

// dryCushion is the cushion a dried-out stream must hold again before a
// later drought counts as another.
const dryCushion = clockHz

// mark is one PCR the pacer releases by.
type mark struct {
	pos int64 // the PCR packet's stream position
	t   int64 // its time on the pacer's own continuous clock, in ticks
}

// pacer releases a ring's bytes to its viewers in real time by the
// stream's own clock, its PCR, holding back up to hold of media behind the
// newest PCR (owner, 2026-11-27: "This behavior is very noticeable on the
// Plex server"). tvload.win's CDN sends a stream in bursts: about a minute
// of backlog at connect, then about ten seconds of media every ten
// seconds with nothing between. Passed through as they came, the bursts
// put PMS at the live edge of what it had, and every gap between them
// stalled its clients; a real HDHomeRun sends a steady stream. The pacer
// is the ring's jitter buffer: the backlog fills the cushion at once, the
// clock drains it at real time while the upstream is silent, and each
// burst refills it.
//
// Its rules, in media time a released up to:
//   - the clock: a moves at real time from where it was last set;
//   - the cushion: a is never more than hold behind the newest PCR, so the
//     backlog's first part goes out at once and the latency stays hold;
//   - the head: a never passes the newest PCR. A clock that reaches it
//     waits there (the stream ran dry), and the next burst is paced from
//     that moment, never released in one go;
//   - the floor: no byte is held that the ring is about to overwrite.
//
// A stream with no PCR, or hold zero, is passed through as it comes.
type pacer struct {
	hold  int64 // ticks; zero passes bytes as they come
	marks []mark
	// raw is the newest PCR base as read, -1 before the first; head its
	// time on the continuous clock.
	raw, head int64
	// clock is the media time released at the wall time at; waiting is
	// set while the clock waits at the head for the upstream.
	clock   int64
	at      time.Time
	waiting bool
	// dry is set while the clock waits at the head, and cleared once the
	// cushion holds dryCushion again; ranDry is told each time it is set,
	// at dryAt, and refilled how long it was set each time it clears.
	dry      bool
	dryAt    time.Time
	ranDry   func()
	refilled func(dry time.Duration)
}

// newPacer starts dry: a stream that never had a cushion has not run out
// of one.
func newPacer(hold time.Duration) pacer {
	return pacer{hold: ticks(max(hold, 0)), raw: -1, dry: true}
}

// ticks is d on the 90 kHz clock.
func ticks(d time.Duration) int64 { return d.Microseconds() * 9 / 100 }

// span is n ticks as a duration.
func span(n int64) time.Duration { return time.Duration(n) * time.Second / clockHz }

// paced says whether the pacer holds anything back.
func (p *pacer) paced() bool { return p.hold > 0 && p.raw >= 0 }

// pcr records the PCR base raw (90 kHz, 33 bits) of the packet at pos.
func (p *pacer) pcr(pos, raw int64) {
	if p.hold == 0 {
		return
	}
	t := int64(0)
	if p.raw >= 0 {
		step := (raw - p.raw) & (ptsModulus - 1)
		if step > maxClockStep {
			step = 0
		}
		t = p.head + step
	}
	p.raw, p.head = raw, t
	if n := len(p.marks); n > 0 && t-p.marks[n-1].t < markSpacing {
		return
	}
	p.marks = append(p.marks, mark{pos: pos, t: t})
}

// release is how far viewers may read at now: the position of the first
// mark past the released media time, or written when every mark is
// released. Bytes before floor are never held.
func (p *pacer) release(now time.Time, written, floor int64) int64 {
	if !p.paced() {
		p.marks = p.marks[:0]
		return written
	}
	if p.at.IsZero() {
		p.clock, p.at = p.marks[0].t, now
	}
	if p.waiting {
		if p.head <= p.clock {
			return written
		}
		// The upstream sent more: paced from now, not from when the clock
		// began to wait, which would let the wait's length out at once.
		p.at, p.waiting = now, false
	}
	a := p.clock + ticks(now.Sub(p.at))
	if lo := p.head - p.hold; a < lo {
		a, p.clock, p.at = lo, lo, now
	}
	if a >= p.head {
		// Run dry: the clock waits at the head until the upstream sends
		// more, which is then paced from now.
		p.clock, p.at, p.waiting = p.head, now, true
		p.marks = p.marks[:0]
		if !p.dry {
			p.dry, p.dryAt = true, now
			if p.ranDry != nil {
				p.ranDry()
			}
		}
		return written
	}
	if p.head-a >= dryCushion && p.dry {
		p.dry = false
		if !p.dryAt.IsZero() && p.refilled != nil {
			p.refilled(now.Sub(p.dryAt))
		}
		p.dryAt = time.Time{}
	}
	i := 0
	for i < len(p.marks) && p.marks[i].t <= a {
		i++
	}
	for i < len(p.marks) && p.marks[i].pos < floor {
		p.clock, p.at = p.marks[i].t, now
		i++
	}
	p.marks = p.marks[i:]
	if len(p.marks) == 0 {
		return written
	}
	return p.marks[0].pos
}

// due is when the clock next moves the release, if it will.
func (p *pacer) due() (time.Time, bool) {
	if !p.paced() || len(p.marks) == 0 || p.at.IsZero() {
		return time.Time{}, false
	}
	return p.at.Add(span(p.marks[0].t - p.clock)), true
}

// cushion is the media held back at a, the clock's reading at now.
func (p *pacer) cushion(now time.Time) time.Duration {
	if !p.paced() || p.at.IsZero() {
		return 0
	}
	return span(max(p.head-p.clock-ticks(now.Sub(p.at)), 0))
}
