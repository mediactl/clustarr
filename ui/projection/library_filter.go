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

package projection

import (
	"slices"
	"strconv"
	"strings"
	"time"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// The library's custom filters (owner, 2026-11-10: "many more filtering
// options in the Movies and TV libraries like radarr/sonarr, including
// custom filter options"). A custom filter is Radarr's: rows of field,
// predicate and value, every row kept (AND), the values of one row any of
// (OR). Each row is a Condition, carried in the page's URL as a repeated
// ?f=<field>:<op>:<value>[|<value>...], so the page, its stream and a
// shared link all filter alike; Arrange applies them after the preset.

// FieldType is how a field's values compare, and so which predicates and
// which value control the builder offers.
type FieldType string

const (
	// FieldBool is a yes or no.
	FieldBool FieldType = "bool"
	// FieldNumber is a number, compared as one.
	FieldNumber FieldType = "number"
	// FieldDate is a moment, compared as days from now or to a date.
	FieldDate FieldType = "date"
	// FieldString is free text, compared case-insensitively.
	FieldString FieldType = "string"
	// FieldEnum is one value from a known set: the builder lists them.
	FieldEnum FieldType = "enum"
	// FieldList is several values from a known set (genres, tags).
	FieldList FieldType = "list"
	// FieldTextList is several free-text values (studios, keywords),
	// matched by a piece of their text: too many to list.
	FieldTextList FieldType = "textList"
)

// Op is a condition's predicate; its string is the URL's.
type Op string

const (
	OpEq          Op = "eq"
	OpNe          Op = "ne"
	OpGt          Op = "gt"
	OpGte         Op = "gte"
	OpLt          Op = "lt"
	OpLte         Op = "lte"
	OpContains    Op = "contains"
	OpNotContains Op = "notContains"
	OpStartsWith  Op = "startsWith"
	OpEndsWith    Op = "endsWith"
	OpIn          Op = "in"
	OpNotIn       Op = "notIn"
	OpInLast      Op = "inLast"
	OpNotInLast   Op = "notInLast"
	OpInNext      Op = "inNext"
	OpNotInNext   Op = "notInNext"
	OpBefore      Op = "before"
	OpAfter       Op = "after"
)

// opsByType are each type's predicates, in the builder's order.
var opsByType = map[FieldType][]Op{
	FieldBool:     {OpEq},
	FieldNumber:   {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte},
	FieldDate:     {OpInLast, OpNotInLast, OpInNext, OpNotInNext, OpBefore, OpAfter},
	FieldString:   {OpContains, OpNotContains, OpStartsWith, OpEndsWith, OpEq, OpNe},
	FieldEnum:     {OpIn, OpNotIn},
	FieldList:     {OpIn, OpNotIn},
	FieldTextList: {OpContains, OpNotContains},
}

// Ops are the predicates a field of type t takes, in the builder's order.
func Ops(t FieldType) []Op { return slices.Clone(opsByType[t]) }

// OpLabel is a predicate's wording under a field of type t, Radarr's.
func OpLabel(t FieldType, op Op) string {
	switch op {
	case OpEq:
		return "is"
	case OpNe:
		return "is not"
	case OpGt:
		return "greater than"
	case OpGte:
		return "greater than or equal"
	case OpLt:
		return "less than"
	case OpLte:
		return "less than or equal"
	case OpContains:
		return "contains"
	case OpNotContains:
		return "does not contain"
	case OpStartsWith:
		return "starts with"
	case OpEndsWith:
		return "ends with"
	case OpIn:
		if t == FieldList {
			return "has any of"
		}
		return "is"
	case OpNotIn:
		if t == FieldList {
			return "has none of"
		}
		return "is not"
	case OpInLast:
		return "in the last"
	case OpNotInLast:
		return "not in the last"
	case OpInNext:
		return "in the next"
	case OpNotInNext:
		return "not in the next"
	case OpBefore:
		return "before"
	case OpAfter:
		return "after"
	}
	return string(op)
}

// Field is one thing a custom filter can test. Exactly one of its readers
// is set, the one its Type reads.
type Field struct {
	Key   string
	Label string
	Type  FieldType
	// Unit names a number's or a date predicate's unit ("GB", "minutes",
	// "days"), for the builder.
	Unit string
	// Language marks a field whose values are language tags, which the
	// builder shows by name.
	Language bool
	// Fixed lists an enum's values when they are a known set (a status),
	// in the builder's order; otherwise the builder lists the values the
	// tab's items carry.
	Fixed []string
	// Labels names Fixed's values; a value it does not name is shown as is.
	Labels map[string]string

	tabs    []Tab
	boolean func(LibraryItem) bool
	number  func(LibraryItem) (float64, bool)
	date    func(LibraryItem) time.Time
	text    func(LibraryItem) string
	texts   func(LibraryItem) []string
}

// ValueLabel is how the builder and a filter's chip show value.
func (f Field) ValueLabel(value string) string {
	if l, ok := f.Labels[value]; ok {
		return l
	}
	return value
}

// onTab reports whether the field is offered on tab.
func (f Field) onTab(tab Tab) bool { return len(f.tabs) == 0 || slices.Contains(f.tabs, tab) }

var (
	videoTabs = []Tab{TabMovies, TabTV}
	movieTab  = []Tab{TabMovies}
	tvTab     = []Tab{TabTV}
)

// statusLabels name LibraryStatus's values.
var statusLabels = map[string]string{
	"missing": "Missing", "downloading": "Downloading", "cutoff-unmet": "Cutoff Unmet",
	"downloaded": "Downloaded", "unmonitored": "Unmonitored", "deleting": "Deleting",
}

// libraryFields is every field, in the builder's order: the ones every tab
// has first, then the video tabs', then a tab's own.
var libraryFields = []Field{
	{Key: "monitored", Label: "Monitored", Type: FieldBool, boolean: func(li LibraryItem) bool { return li.Monitored }},
	{Key: "status", Label: "Status", Type: FieldEnum,
		Fixed:  []string{"missing", "downloading", "cutoff-unmet", "downloaded", "unmonitored"},
		Labels: statusLabels, text: LibraryStatus},
	{Key: "hasFile", Label: "Has File", Type: FieldBool, boolean: func(li LibraryItem) bool { return li.HasFile }},
	{Key: "title", Label: "Title", Type: FieldString, text: func(li LibraryItem) string { return li.Title }},
	{Key: "year", Label: "Year", Type: FieldNumber, number: positive(func(li LibraryItem) float64 { return float64(li.Year) })},
	{Key: "added", Label: "Added", Type: FieldDate, Unit: "days", date: func(li LibraryItem) time.Time { return li.Added }},
	{Key: "profile", Label: "Quality Profile", Type: FieldEnum, text: func(li LibraryItem) string { return li.QualityProfileRef }},
	{Key: "rootFolder", Label: "Root Folder", Type: FieldEnum, text: func(li LibraryItem) string { return li.RootFolder }},
	{Key: "path", Label: "Path", Type: FieldString, text: func(li LibraryItem) string { return li.Path }},
	{Key: "tags", Label: "Tags", Type: FieldList, texts: func(li LibraryItem) []string { return li.Tags }},

	{Key: "genres", Label: "Genres", Type: FieldList, tabs: videoTabs, texts: func(li LibraryItem) []string { return li.Genres }},
	{Key: "subGenres", Label: "Sub-Genres", Type: FieldList, tabs: tvTab, texts: func(li LibraryItem) []string { return li.SubGenres }},
	{Key: "keywords", Label: "Keywords", Type: FieldTextList, tabs: videoTabs, texts: func(li LibraryItem) []string { return li.Keywords }},
	{Key: "originalLanguage", Label: "Original Language", Type: FieldEnum, Language: true, tabs: videoTabs,
		text: func(li LibraryItem) string { return li.OriginalLanguage }},
	{Key: "certification", Label: "Certification", Type: FieldEnum, tabs: videoTabs, text: func(li LibraryItem) string { return li.Certification }},
	{Key: "studio", Label: "Studio", Type: FieldTextList, tabs: videoTabs, texts: func(li LibraryItem) []string { return li.Studios }},
	{Key: "network", Label: "Network", Type: FieldTextList, tabs: tvTab, texts: func(li LibraryItem) []string { return li.Networks }},
	{Key: "countries", Label: "Country", Type: FieldList, tabs: videoTabs, texts: func(li LibraryItem) []string { return li.Countries }},
	{Key: "runtime", Label: "Runtime", Type: FieldNumber, Unit: "minutes", tabs: videoTabs,
		number: positive(func(li LibraryItem) float64 { return float64(li.RuntimeMinutes) })},
	{Key: "ratingTmdb", Label: "TMDB Rating", Type: FieldNumber, tabs: videoTabs, number: rating(catalogv1.RatingSourceTMDB)},
	{Key: "ratingImdb", Label: "IMDb Rating", Type: FieldNumber, tabs: videoTabs, number: rating(catalogv1.RatingSourceIMDb)},
	{Key: "ratingRt", Label: "Rotten Tomatoes", Type: FieldNumber, Unit: "%", tabs: movieTab, number: rating(catalogv1.RatingSourceRTCritic)},
	{Key: "size", Label: "Size on Disk", Type: FieldNumber, Unit: "GB", tabs: videoTabs,
		number: func(li LibraryItem) (float64, bool) { return float64(li.SizeBytes) / 1e9, true }},

	{Key: "releaseStatus", Label: "Release Status", Type: FieldEnum, tabs: movieTab,
		Fixed:  []string{"tba", "announced", "inCinemas", "released"},
		Labels: map[string]string{"tba": "TBA", "announced": "Announced", "inCinemas": "In Cinemas", "released": "Released"},
		text:   func(li LibraryItem) string { return li.ReleaseStatus }},
	{Key: "minimumAvailability", Label: "Minimum Availability", Type: FieldEnum, tabs: movieTab,
		Fixed:  []string{"tba", "announced", "inCinemas", "released"},
		Labels: map[string]string{"tba": "TBA", "announced": "Announced", "inCinemas": "In Cinemas", "released": "Released"},
		text:   func(li LibraryItem) string { return li.MinimumAvailability }},
	{Key: "inCinemas", Label: "In Cinemas", Type: FieldDate, Unit: "days", tabs: movieTab, date: func(li LibraryItem) time.Time { return li.InCinemas }},
	{Key: "digitalRelease", Label: "Digital Release", Type: FieldDate, Unit: "days", tabs: movieTab, date: func(li LibraryItem) time.Time { return li.DigitalRelease }},
	{Key: "physicalRelease", Label: "Physical Release", Type: FieldDate, Unit: "days", tabs: movieTab, date: func(li LibraryItem) time.Time { return li.PhysicalRelease }},
	{Key: "collection", Label: "Collection", Type: FieldString, tabs: movieTab, text: func(li LibraryItem) string { return li.Collection }},
	{Key: "quality", Label: "Quality", Type: FieldEnum, tabs: movieTab, text: func(li LibraryItem) string { return li.Quality }},
	{Key: "videoCodec", Label: "Video Codec", Type: FieldEnum, tabs: movieTab, text: func(li LibraryItem) string { return li.VideoCodec }},
	{Key: "hdr", Label: "HDR", Type: FieldEnum, tabs: movieTab, text: func(li LibraryItem) string { return li.HDR }},
	{Key: "audioLanguages", Label: "Audio Language", Type: FieldList, Language: true, tabs: movieTab,
		texts: func(li LibraryItem) []string { return li.AudioLanguages }},
	{Key: "transcoded", Label: "Transcoded", Type: FieldBool, tabs: movieTab, boolean: func(li LibraryItem) bool { return li.Phase == "Transcoded" }},

	{Key: "seriesStatus", Label: "Series Status", Type: FieldEnum, tabs: tvTab,
		Fixed:  []string{"continuing", "ended", "upcoming"},
		Labels: map[string]string{"continuing": "Continuing", "ended": "Ended", "upcoming": "Upcoming"},
		text:   func(li LibraryItem) string { return li.ReleaseStatus }},
	{Key: "seriesType", Label: "Series Type", Type: FieldEnum, tabs: tvTab,
		Fixed:  []string{"standard", "daily", "anime"},
		Labels: map[string]string{"standard": "Standard", "daily": "Daily", "anime": "Anime"},
		text:   func(li LibraryItem) string { return li.SeriesType }},
	{Key: "firstAired", Label: "First Aired", Type: FieldDate, Unit: "days", tabs: tvTab, date: func(li LibraryItem) time.Time { return li.FirstAired }},
	{Key: "nextAiring", Label: "Next Airing", Type: FieldDate, Unit: "days", tabs: tvTab, date: func(li LibraryItem) time.Time { return li.NextAiring }},
	{Key: "previousAiring", Label: "Previous Airing", Type: FieldDate, Unit: "days", tabs: tvTab, date: func(li LibraryItem) time.Time { return li.PreviousAiring }},
	{Key: "seasonCount", Label: "Seasons", Type: FieldNumber, tabs: tvTab,
		number: func(li LibraryItem) (float64, bool) { return float64(li.SeasonCount), true }},
	{Key: "episodeCount", Label: "Episodes", Type: FieldNumber, tabs: tvTab,
		number: func(li LibraryItem) (float64, bool) { return float64(li.EpisodeCount), true }},
	{Key: "episodeFileCount", Label: "Episodes on Disk", Type: FieldNumber, tabs: tvTab,
		number: func(li LibraryItem) (float64, bool) { return float64(li.EpisodeFileCount), true }},
	{Key: "missingEpisodes", Label: "Missing Episodes", Type: FieldNumber, tabs: tvTab,
		number: func(li LibraryItem) (float64, bool) { return float64(li.MissingEpisodes), true }},
	{Key: "episodeProgress", Label: "Episode Progress", Type: FieldNumber, Unit: "%", tabs: tvTab, number: episodeProgress},
}

// positive reads a number that is unknown at zero (a year, a runtime).
func positive(f func(LibraryItem) float64) func(LibraryItem) (float64, bool) {
	return func(li LibraryItem) (float64, bool) {
		v := f(li)
		return v, v > 0
	}
}

// rating reads the item's rating from source on its own scale: out of 10
// for TMDB and IMDb, a percentage for Rotten Tomatoes.
func rating(source catalogv1.RatingSource) func(LibraryItem) (float64, bool) {
	return func(li LibraryItem) (float64, bool) {
		for _, r := range li.Ratings {
			if r.Source == source {
				return float64(r.ValueCentis) / 100, true
			}
		}
		return 0, false
	}
}

// episodeProgress is a series' episodes on disk as a percentage of its
// episodes, Sonarr's progress bar; unknown with no episodes.
func episodeProgress(li LibraryItem) (float64, bool) {
	if li.EpisodeCount <= 0 {
		return 0, false
	}
	return 100 * float64(li.EpisodeFileCount) / float64(li.EpisodeCount), true
}

// LibraryFields are the fields a tab's custom filters can test, in the
// builder's order.
func LibraryFields(tab Tab) []Field {
	out := make([]Field, 0, len(libraryFields))
	for _, f := range libraryFields {
		if f.onTab(tab) {
			out = append(out, f)
		}
	}
	return out
}

// LookupField finds the field key names on tab.
func LookupField(tab Tab, key string) (Field, bool) {
	for _, f := range libraryFields {
		if f.Key == key && f.onTab(tab) {
			return f, true
		}
	}
	return Field{}, false
}

// FieldValues are the values the builder offers for an enum or list field:
// its Fixed set, else every distinct value items carry, sorted.
func (f Field) FieldValues(items []LibraryItem) []string {
	if len(f.Fixed) > 0 {
		return slices.Clone(f.Fixed)
	}
	seen := map[string]bool{}
	for _, li := range items {
		switch {
		case f.text != nil:
			if v := f.text(li); v != "" {
				seen[v] = true
			}
		case f.texts != nil:
			for _, v := range f.texts(li) {
				if v != "" {
					seen[v] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}

// Condition is one row of a custom filter: Field's value tested by Op
// against Values, any of which may match.
type Condition struct {
	Field  string
	Op     Op
	Values []string
}

// conditionValueSep separates a row's values in the URL. No genre,
// language, profile, tag or status name carries one.
const conditionValueSep = "|"

// String is the condition as the URL carries it: field:op:value[|value].
func (c Condition) String() string {
	return c.Field + ":" + string(c.Op) + ":" + strings.Join(c.Values, conditionValueSep)
}

// ParseConditions reads a tab's ?f values, dropping any that do not parse,
// name a field the tab does not have, use a predicate the field does not
// take or carry a value it cannot read -- a hand-edited URL filters by what
// it says correctly and ignores the rest.
func ParseConditions(tab Tab, raw []string) []Condition {
	var out []Condition
	for _, s := range raw {
		key, rest, ok := strings.Cut(s, ":")
		if !ok {
			continue
		}
		op, value, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		c, ok := NewCondition(tab, key, Op(op), strings.Split(value, conditionValueSep))
		if ok {
			out = append(out, c)
		}
	}
	return out
}

// NewCondition builds a condition from a builder row, trimming its values
// and dropping the empty ones, and reports false when the row says nothing
// the field can test.
func NewCondition(tab Tab, key string, op Op, values []string) (Condition, bool) {
	f, ok := LookupField(tab, key)
	if !ok || !slices.Contains(opsByType[f.Type], op) {
		return Condition{}, false
	}
	vals := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" && !slices.Contains(vals, v) {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return Condition{}, false
	}
	switch f.Type {
	case FieldBool:
		if vals[0] != "true" && vals[0] != "false" {
			return Condition{}, false
		}
		vals = vals[:1]
	case FieldNumber:
		if _, err := strconv.ParseFloat(vals[0], 64); err != nil {
			return Condition{}, false
		}
		vals = vals[:1]
	case FieldDate:
		switch op {
		case OpBefore, OpAfter:
			if _, err := time.Parse(time.DateOnly, vals[0]); err != nil {
				return Condition{}, false
			}
		default:
			if n, err := strconv.Atoi(vals[0]); err != nil || n < 0 {
				return Condition{}, false
			}
		}
		vals = vals[:1]
	case FieldString:
		vals = vals[:1]
	}
	return Condition{Field: key, Op: op, Values: vals}, true
}

// Matches reports whether li passes c on tab at now. A condition naming a
// field the tab does not have passes everything.
func (c Condition) Matches(tab Tab, li LibraryItem, now time.Time) bool {
	f, ok := LookupField(tab, c.Field)
	if !ok || len(c.Values) == 0 {
		return true
	}
	switch f.Type {
	case FieldBool:
		return f.boolean(li) == (c.Values[0] == "true")
	case FieldNumber:
		want, err := strconv.ParseFloat(c.Values[0], 64)
		if err != nil {
			return true
		}
		v, known := f.number(li)
		if !known {
			return c.Op == OpNe
		}
		return compareNumber(c.Op, v, want)
	case FieldDate:
		return matchDate(c.Op, f.date(li), c.Values[0], now)
	case FieldString:
		return matchText(c.Op, f.text(li), c.Values[0])
	case FieldEnum:
		in := slices.ContainsFunc(c.Values, func(v string) bool { return strings.EqualFold(v, f.text(li)) })
		return in == (c.Op == OpIn)
	case FieldList:
		have := f.texts(li)
		in := slices.ContainsFunc(c.Values, func(v string) bool {
			return slices.ContainsFunc(have, func(h string) bool { return strings.EqualFold(h, v) })
		})
		return in == (c.Op == OpIn)
	case FieldTextList:
		have := f.texts(li)
		in := slices.ContainsFunc(c.Values, func(v string) bool {
			return slices.ContainsFunc(have, func(h string) bool { return containsFold(h, v) })
		})
		return in == (c.Op == OpContains)
	}
	return true
}

func compareNumber(op Op, v, want float64) bool {
	switch op {
	case OpEq:
		return v == want
	case OpNe:
		return v != want
	case OpGt:
		return v > want
	case OpGte:
		return v >= want
	case OpLt:
		return v < want
	case OpLte:
		return v <= want
	}
	return true
}

// matchDate tests a moment; an unknown one (zero) is in no window and
// before or after nothing.
func matchDate(op Op, t time.Time, value string, now time.Time) bool {
	switch op {
	case OpBefore, OpAfter:
		day, err := time.Parse(time.DateOnly, value)
		if err != nil {
			return true
		}
		if t.IsZero() {
			return false
		}
		if op == OpBefore {
			return t.Before(day)
		}
		return !t.Before(day.AddDate(0, 0, 1))
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return true
	}
	window := time.Duration(n) * 24 * time.Hour
	var in bool
	switch op {
	case OpInLast, OpNotInLast:
		in = !t.IsZero() && !t.After(now) && !t.Before(now.Add(-window))
		return in == (op == OpInLast)
	case OpInNext, OpNotInNext:
		in = !t.IsZero() && !t.Before(now) && !t.After(now.Add(window))
		return in == (op == OpInNext)
	}
	return true
}

func matchText(op Op, have, want string) bool {
	switch op {
	case OpContains:
		return containsFold(have, want)
	case OpNotContains:
		return !containsFold(have, want)
	case OpStartsWith:
		return strings.HasPrefix(strings.ToLower(have), strings.ToLower(want))
	case OpEndsWith:
		return strings.HasSuffix(strings.ToLower(have), strings.ToLower(want))
	case OpEq:
		return strings.EqualFold(have, want)
	case OpNe:
		return !strings.EqualFold(have, want)
	}
	return true
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
