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

package views

import "fmt"

// SeasonRow is one season component on a series page (spec
// 2026-09-23-library-page-design): the rollup's counts and the season's
// effective monitored flag, plus the URLs its lazy load and its own page
// use. ui builds it from the Series (ui/series.go); the view only renders.
type SeasonRow struct {
	Series    string // the series' name, for the toggle's target
	Namespace string
	Number    int32
	Monitored bool
	Episodes  int32
	Files     int32
	// NextAiring is the next air date as YYYY-MM-DD, or "".
	NextAiring string
	// Error is set on a component rendered as the reply to a toggle that
	// failed: the stable code (data-action-error) and the detail.
	Error ActionFailure
}

// ActionFailure is an action's failure rendered inside the component it
// was meant to change, so the page stays usable and the failure is visible
// where it happened. Code is the stable reason ("no-writer", "invalid",
// "failed"); Message the detail. A zero value is no failure.
type ActionFailure struct {
	Code    string
	Message string
}

// MonitorURL is the season's monitor action.
func (r SeasonRow) MonitorURL() string { return r.URL() + "/monitor" }

// URL is the season's own route, which serves the episodes component to
// htmx and a page to anyone else.
func (r SeasonRow) URL() string {
	return fmt.Sprintf("/library/%s/series/%s/seasons/%d", r.Namespace, r.Series, r.Number)
}

// Label is the season's heading; season 0 is the specials.
func (r SeasonRow) Label() string {
	if r.Number == 0 {
		return "Specials"
	}
	return fmt.Sprintf("Season %d", r.Number)
}

// EpisodeRow is one episode on a season's episodes component.
type EpisodeRow struct {
	Namespace string
	Name      string
	Number    int32
	Title     string
	// AirDate is YYYY-MM-DD, or "" when unknown.
	AirDate   string
	Monitored bool
	HasFile   bool
	// Quality is the file's quality name, "" without a file.
	Quality string
	Phase   string
	// Error is set on a row rendered as the reply to a toggle that failed.
	Error ActionFailure
}

// MonitorURL is the episode's monitor action, the existing per-item route.
func (r EpisodeRow) MonitorURL() string {
	return fmt.Sprintf("/library/%s/episode/%s/monitor", r.Namespace, r.Name)
}
