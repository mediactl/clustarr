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

// Package plexfilter is clustarr's catalogue of Plex's smart-filter fields
// and operators (docs/superpowers/specs/2026-11-10-plex-collections-design.md):
// what a PlexCollection's filter may name, validation of a spec before the
// UI writes it, and the one-line reading the Collections page lists.
// cluster-plex compiles and checks the filter against a library's live Meta
// again; this table is the editor's and was transcribed from talos's PMS
// 1.43.5 (test/data/plexfilter, TestCatalogueMatchesRecordedMeta).
package plexfilter

import (
	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// FieldType is a filter field's type in Plex's Meta, which decides the
// operators it takes.
type FieldType string

const (
	TypeTag              FieldType = "tag"
	TypeString           FieldType = "string"
	TypeInteger          FieldType = "integer"
	TypeDate             FieldType = "date"
	TypeBoolean          FieldType = "boolean"
	TypeAudioLanguage    FieldType = "audioLanguage"
	TypeSubtitleLanguage FieldType = "subtitleLanguage"
	TypeResolution       FieldType = "resolution"
)

// Field is one filter field: Plex's key, its title in Plex Web, its type.
type Field struct {
	Key   string
	Title string
	Type  FieldType
}

// movieFields are a movie library's 39 filter fields.
var movieFields = []Field{
	{Key: "title", Title: "Title", Type: FieldType("string")},
	{Key: "studio", Title: "Studio", Type: FieldType("string")},
	{Key: "userRating", Title: "Rating", Type: FieldType("integer")},
	{Key: "contentRating", Title: "Content Rating", Type: FieldType("tag")},
	{Key: "year", Title: "Year", Type: FieldType("integer")},
	{Key: "decade", Title: "Decade", Type: FieldType("integer")},
	{Key: "originallyAvailableAt", Title: "Release Date", Type: FieldType("date")},
	{Key: "duration", Title: "Duration", Type: FieldType("integer")},
	{Key: "unmatched", Title: "Unmatched", Type: FieldType("boolean")},
	{Key: "duplicate", Title: "Duplicate", Type: FieldType("boolean")},
	{Key: "genre", Title: "Genre", Type: FieldType("tag")},
	{Key: "collection", Title: "Collection", Type: FieldType("tag")},
	{Key: "director", Title: "Director", Type: FieldType("tag")},
	{Key: "writer", Title: "Writer", Type: FieldType("tag")},
	{Key: "producer", Title: "Producer", Type: FieldType("tag")},
	{Key: "actor", Title: "Actor", Type: FieldType("tag")},
	{Key: "country", Title: "Country", Type: FieldType("tag")},
	{Key: "addedAt", Title: "Date Added", Type: FieldType("date")},
	{Key: "viewCount", Title: "Plays", Type: FieldType("integer")},
	{Key: "lastViewedAt", Title: "Last Watched", Type: FieldType("date")},
	{Key: "unwatched", Title: "Unwatched", Type: FieldType("boolean")},
	{Key: "resolution", Title: "Resolution", Type: FieldType("resolution")},
	{Key: "hdr", Title: "HDR", Type: FieldType("boolean")},
	{Key: "dovi", Title: "DOVI", Type: FieldType("boolean")},
	{Key: "hdr10plus", Title: "HDR10+", Type: FieldType("boolean")},
	{Key: "atmos", Title: "Atmos", Type: FieldType("boolean")},
	{Key: "mediaSize", Title: "File Size", Type: FieldType("integer")},
	{Key: "mediaBitrate", Title: "Bitrate", Type: FieldType("integer")},
	{Key: "videoCodec", Title: "Video Codec", Type: FieldType("tag")},
	{Key: "audioCodec", Title: "Audio Codec", Type: FieldType("tag")},
	{Key: "subtitleCodec", Title: "Subtitle Codec", Type: FieldType("tag")},
	{Key: "audioLayout", Title: "Audio Layout", Type: FieldType("tag")},
	{Key: "subtitleLanguage", Title: "Subtitle Language", Type: FieldType("subtitleLanguage")},
	{Key: "audioLanguage", Title: "Audio Language", Type: FieldType("audioLanguage")},
	{Key: "inProgress", Title: "In Progress", Type: FieldType("boolean")},
	{Key: "trash", Title: "Trash", Type: FieldType("boolean")},
	{Key: "location", Title: "Folder Location", Type: FieldType("tag")},
	{Key: "editionTitle", Title: "Edition", Type: FieldType("string")},
	{Key: "label", Title: "Label", Type: FieldType("tag")},
}

// showFields are a TV library's 20 show fields.
var showFields = []Field{
	{Key: "show.title", Title: "Show Title", Type: FieldType("string")},
	{Key: "show.studio", Title: "Studio", Type: FieldType("string")},
	{Key: "show.network", Title: "Network", Type: FieldType("tag")},
	{Key: "show.country", Title: "Country", Type: FieldType("tag")},
	{Key: "show.userRating", Title: "Show Rating", Type: FieldType("integer")},
	{Key: "show.contentRating", Title: "Content Rating", Type: FieldType("tag")},
	{Key: "show.year", Title: "Show Year", Type: FieldType("integer")},
	{Key: "show.viewCount", Title: "Show Plays", Type: FieldType("integer")},
	{Key: "show.lastViewedAt", Title: "Show Last Watched", Type: FieldType("date")},
	{Key: "show.genre", Title: "Genre", Type: FieldType("tag")},
	{Key: "show.collection", Title: "Show Collection", Type: FieldType("tag")},
	{Key: "show.director", Title: "Show Director", Type: FieldType("tag")},
	{Key: "show.writer", Title: "Show Writer", Type: FieldType("tag")},
	{Key: "show.producer", Title: "Show Producer", Type: FieldType("tag")},
	{Key: "show.actor", Title: "Show Actor", Type: FieldType("tag")},
	{Key: "show.addedAt", Title: "Date Show Added", Type: FieldType("date")},
	{Key: "show.unmatched", Title: "Show Unmatched", Type: FieldType("boolean")},
	{Key: "show.unwatchedLeaves", Title: "Unwatched Episodes", Type: FieldType("boolean")},
	{Key: "show.editionTitle", Title: "Show Edition", Type: FieldType("string")},
	{Key: "show.label", Title: "Label", Type: FieldType("tag")},
}

// episodeFields are a TV library's 26 episode fields.
var episodeFields = []Field{
	{Key: "episode.title", Title: "Episode Title", Type: FieldType("string")},
	{Key: "episode.addedAt", Title: "Date Episode Added", Type: FieldType("date")},
	{Key: "episode.originallyAvailableAt", Title: "Episode Air Date", Type: FieldType("date")},
	{Key: "episode.year", Title: "Episode Year", Type: FieldType("integer")},
	{Key: "episode.userRating", Title: "Episode Rating", Type: FieldType("integer")},
	{Key: "episode.viewCount", Title: "Episode Plays", Type: FieldType("integer")},
	{Key: "episode.lastViewedAt", Title: "Episode Last Watched", Type: FieldType("date")},
	{Key: "episode.unwatched", Title: "Episode Unwatched", Type: FieldType("boolean")},
	{Key: "episode.inProgress", Title: "Episode In Progress", Type: FieldType("boolean")},
	{Key: "episode.duplicate", Title: "Episode Duplicate", Type: FieldType("boolean")},
	{Key: "episode.hdr", Title: "HDR", Type: FieldType("boolean")},
	{Key: "episode.dovi", Title: "DOVI", Type: FieldType("boolean")},
	{Key: "episode.hdr10plus", Title: "HDR10+", Type: FieldType("boolean")},
	{Key: "episode.atmos", Title: "Atmos", Type: FieldType("boolean")},
	{Key: "episode.resolution", Title: "Resolution", Type: FieldType("resolution")},
	{Key: "episode.mediaSize", Title: "File size", Type: FieldType("integer")},
	{Key: "episode.mediaBitrate", Title: "Bitrate", Type: FieldType("integer")},
	{Key: "episode.videoCodec", Title: "Video Codec", Type: FieldType("tag")},
	{Key: "episode.audioCodec", Title: "Audio Codec", Type: FieldType("tag")},
	{Key: "episode.subtitleCodec", Title: "Subtitle Codec", Type: FieldType("tag")},
	{Key: "episode.audioLayout", Title: "Audio Layout", Type: FieldType("tag")},
	{Key: "episode.subtitleLanguage", Title: "Subtitle Language", Type: FieldType("subtitleLanguage")},
	{Key: "episode.audioLanguage", Title: "Audio Language", Type: FieldType("audioLanguage")},
	{Key: "episode.trash", Title: "Trash", Type: FieldType("boolean")},
	{Key: "episode.unmatched", Title: "Episode Unmatched", Type: FieldType("boolean")},
	{Key: "episode.location", Title: "Folder Location", Type: FieldType("tag")},
}

// Fields are the fields a collection of the item type may filter on: a
// movie library's for movies, a TV library's show and episode fields for
// shows, seasons and episodes (Anime Dubbed, a show collection, filters on
// episode.audioLanguage), in Meta's order.
func Fields(t catalogv1.PlexItemType) []Field {
	switch t {
	case catalogv1.PlexItemMovie:
		return append([]Field(nil), movieFields...)
	case catalogv1.PlexItemShow, catalogv1.PlexItemSeason, catalogv1.PlexItemEpisode:
		out := append([]Field(nil), showFields...)
		return append(out, episodeFields...)
	}
	return nil
}

// Sort is one of an item type's sort keys and Plex Web's label for it.
type Sort struct {
	Key, Title string
}

// The sort keys of each item type, in Meta's order (talos's PMS 1.43.5,
// 2026-11-10, test/data/plexfilter).
var sortsByType = map[catalogv1.PlexItemType][]Sort{
	catalogv1.PlexItemMovie: {
		{"titleSort", "Title"}, {"originallyAvailableAt", "Release Date"}, {"rating", "Critic Rating"},
		{"audienceRating", "Audience Rating"}, {"duration", "Duration"}, {"addedAt", "Date Added"},
		{"lastViewedAt", "Date Viewed"}, {"mediaHeight", "Resolution"}, {"random", "Randomly"},
	},
	catalogv1.PlexItemShow: {
		{"titleSort", "Title"}, {"originallyAvailableAt", "Release Date"}, {"rating", "Critic Rating"},
		{"audienceRating", "Audience Rating"}, {"unviewedLeafCount", "Unwatched"},
		{"episode.addedAt", "Last Episode Date Added"}, {"lastViewedAt", "Date Viewed"}, {"random", "Randomly"},
	},
	catalogv1.PlexItemSeason: {{"show.titleSort,index", "Show"}, {"random", "Randomly"}},
	catalogv1.PlexItemEpisode: {
		{"show.titleSort,season.index:nullsLast,episode.index:nullsLast,episode.originallyAvailableAt:nullsLast,episode.titleSort,episode.id", "Show"},
		{"originallyAvailableAt", "Release Date"}, {"rating", "Critic Rating"}, {"audienceRating", "Audience Rating"},
		{"addedAt", "Date Added"}, {"lastViewedAt", "Date Viewed"}, {"random", "Randomly"},
	},
}

// Sorts are the keys a smart collection of the item type may sort by.
func Sorts(t catalogv1.PlexItemType) []Sort {
	return append([]Sort(nil), sortsByType[t]...)
}

// Lookup finds a field a collection of the item type may filter on.
func Lookup(t catalogv1.PlexItemType, key string) (Field, bool) {
	for _, f := range Fields(t) {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// operatorOrder is the design's operator table, row by row.
var operatorOrder = []catalogv1.PlexOperator{
	catalogv1.PlexOpIs, catalogv1.PlexOpIsNot, catalogv1.PlexOpContains, catalogv1.PlexOpDoesNotContain,
	catalogv1.PlexOpBeginsWith, catalogv1.PlexOpEndsWith, catalogv1.PlexOpGreaterThan, catalogv1.PlexOpLessThan,
	catalogv1.PlexOpBefore, catalogv1.PlexOpAfter, catalogv1.PlexOpIsTrue, catalogv1.PlexOpIsFalse,
}

// operatorsByType is the design's operator table, column by column: which
// operators Plex's Meta gives each field type.
var operatorsByType = map[FieldType]map[catalogv1.PlexOperator]bool{
	TypeTag:              {catalogv1.PlexOpIs: true, catalogv1.PlexOpIsNot: true},
	TypeString:           {catalogv1.PlexOpIs: true, catalogv1.PlexOpIsNot: true, catalogv1.PlexOpContains: true, catalogv1.PlexOpDoesNotContain: true, catalogv1.PlexOpBeginsWith: true, catalogv1.PlexOpEndsWith: true},
	TypeInteger:          {catalogv1.PlexOpIs: true, catalogv1.PlexOpIsNot: true, catalogv1.PlexOpGreaterThan: true, catalogv1.PlexOpLessThan: true},
	TypeDate:             {catalogv1.PlexOpBefore: true, catalogv1.PlexOpAfter: true},
	TypeBoolean:          {catalogv1.PlexOpIsTrue: true, catalogv1.PlexOpIsFalse: true},
	TypeAudioLanguage:    {catalogv1.PlexOpIs: true, catalogv1.PlexOpIsNot: true},
	TypeSubtitleLanguage: {catalogv1.PlexOpIs: true, catalogv1.PlexOpIsNot: true},
	TypeResolution:       {catalogv1.PlexOpIs: true},
}

// Operators are the operators a field type takes, in the table's order.
func Operators(ft FieldType) []catalogv1.PlexOperator {
	var out []catalogv1.PlexOperator
	for _, op := range operatorOrder {
		if operatorsByType[ft][op] {
			out = append(out, op)
		}
	}
	return out
}

// takes says whether a field type takes the operator.
func takes(ft FieldType, op catalogv1.PlexOperator) bool { return operatorsByType[ft][op] }

var operatorLabels = map[catalogv1.PlexOperator]string{
	catalogv1.PlexOpIs: "is", catalogv1.PlexOpIsNot: "is not",
	catalogv1.PlexOpContains: "contains", catalogv1.PlexOpDoesNotContain: "does not contain",
	catalogv1.PlexOpBeginsWith: "begins with", catalogv1.PlexOpEndsWith: "ends with",
	catalogv1.PlexOpGreaterThan: "is greater than", catalogv1.PlexOpLessThan: "is less than",
	catalogv1.PlexOpBefore: "is before", catalogv1.PlexOpAfter: "is after",
	catalogv1.PlexOpIsTrue: "is true", catalogv1.PlexOpIsFalse: "is false",
}

// OperatorLabel is the operator as Plex Web words it.
func OperatorLabel(op catalogv1.PlexOperator) string {
	if l, ok := operatorLabels[op]; ok {
		return l
	}
	return string(op)
}
