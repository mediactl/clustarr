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

package xmltv

import (
	"fmt"
	"time"

	"github.com/mediactl/clustarr/pkg/release"
)

const (
	// maxRepeats is the most airings of one listing that are one episode:
	// a repeat airs a few times in a guide's week, a series whose every
	// listing reads alike far more often.
	maxRepeats = 3
	// maxMovieAirings is the most airings of one title that are one film.
	maxMovieAirings = 14
)

// repeats is how one built guide's listings recur: BuildGuideWith counts
// them before it enriches, and the Enricher keeps airings that read alike
// as one episode, or one movie, only when they recur as a repeat or a film
// does. The provider's guide gives most programmes a series' description
// and no episode title, so an id made from the listing alone made one
// episode of a show's whole week -- "Paid Programming" 1,077 airings on 70
// channels, a newscast every day, a music block the art resolver took for a
// film 98 times -- and every Live TV hub that showed one listed all its
// airings (talos, 2026-10-28: What's On took 20 to 46 s a hub).
type repeats struct {
	episodes map[string]*recurrence
	titles   map[string]int
}

// recurrence is one listing's airings: how many, and whether one came back
// in the same slot of its channel on another day (a daily show).
type recurrence struct {
	airings int
	slots   map[string]bool
	daily   bool
}

// episodeKey is the listing an EP id is made from.
func episodeKey(title, subtitle, desc string) string {
	return release.TitleNorm(title) + "\x00" + subtitle + "\x00" + desc
}

// movieKey is the title and year an MV id is made from.
func movieKey(title string, year int) string {
	return release.TitleNorm(title) + "\x00" + fmt.Sprint(year)
}

// count records one airing of inner on channel at start.
func (r *repeats) count(inner []byte, channel string, start time.Time) {
	elems, ok := splitElements(inner)
	if !ok || len(elems) == 0 {
		return
	}
	f := facts(elems)
	title, _ := stripLive(f.title)
	title, year := stripYear(title)
	r.titles[movieKey(title, year)]++
	key := episodeKey(title, f.subtitle, f.desc)
	rec := r.episodes[key]
	if rec == nil {
		rec = &recurrence{slots: map[string]bool{}}
		r.episodes[key] = rec
	}
	rec.airings++
	// A channel airs one programme at a time, so a slot seen before was
	// seen on another day.
	slot := channel + " " + start.UTC().Format("15:04")
	if rec.slots[slot] {
		rec.daily = true
	}
	rec.slots[slot] = true
}

// oneEpisode reports whether the airings of a listing are one episode: no
// count (Enrich on its own) keeps the listing's rule.
func (r *repeats) oneEpisode(key string) bool {
	if r == nil {
		return true
	}
	rec := r.episodes[key]
	return rec == nil || (rec.airings <= maxRepeats && !rec.daily)
}

// tooOftenForAFilm reports whether a title airs more often than a film.
func (r *repeats) tooOftenForAFilm(title string, year int) bool {
	return r != nil && r.titles[movieKey(title, year)] > maxMovieAirings
}
