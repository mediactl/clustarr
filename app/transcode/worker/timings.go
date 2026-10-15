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

package worker

import "time"

// stageTimes is how long each stage of one attempt took, logged once as the
// attempt ends (Process): prepare (the source's stat, probe and plan),
// encode, verify, stage (the copy from local scratch onto the data volume,
// scratch.go) and swap (placing the output and its sidecars, retiring the
// source). A failed attempt's last stage is the one it failed in. Without
// it a remux's read of its source over NFS and its copy back are one
// number between "planned" and "output placed" (2026-10-15).
type stageTimes struct {
	now   func() time.Time
	cur   string
	since time.Time
	laps  []lap
}

type lap struct {
	stage string
	took  time.Duration
}

func newStageTimes(now func() time.Time) *stageTimes { return &stageTimes{now: now} }

// enter ends the stage in progress, if any, and starts stage; "" starts none.
func (s *stageTimes) enter(stage string) {
	at := s.now()
	if s.cur != "" {
		s.laps = append(s.laps, lap{s.cur, at.Sub(s.since)})
	}
	s.cur, s.since = stage, at
}

// attrs ends the stage in progress and returns each stage's milliseconds
// ("encodeMs") and their total ("totalMs"), as slog arguments.
func (s *stageTimes) attrs() []any {
	s.enter("")
	var out []any
	var total time.Duration
	for _, l := range s.laps {
		out = append(out, l.stage+"Ms", l.took.Milliseconds())
		total += l.took
	}
	return append(out, "totalMs", total.Milliseconds())
}
