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

package rss

import (
	"maps"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// ProjectRelease turns one wire release into the firehose payload.
//
// indexerName is the Indexer object's metadata.name (NOT a display name):
// the matcher looks up indexer priority by Info.IndexerRef, and the envelope
// key's second segment must match it. Info.IndexerName is set to the same
// value because the CRD carries no display-name field; if one is ever wanted
// the CALLER overwrites Info.IndexerName after projection, and never
// Info.IndexerRef.
//
// pkg/release fills six fields plus ids through ApplyTo; every other field on
// ReleaseInfo is indexer-sourced and hand-filled here. FormatScore and
// MatchedFormats are deliberately NOT set: pkg/decision.Evaluate writes both
// on the consumer side before scoring, and a value invented here would be
// overwritten or, worse, believed.
//
// FetchedAt is left zero. The publisher stamps it, so exactly one place owns
// it -- and schema.Release.FetchedAt has no omitempty, so a value forgotten
// here would ship as "0001-01-01T00:00:00Z" rather than simply be absent.
func ProjectRelease(r torznab.Release, indexerName, protocol string) schema.Release {
	info := commonv1.ReleaseInfo{
		GUID:        r.GUID,
		IndexerRef:  indexerName,
		IndexerName: indexerName,
		Title:       r.Title,
		Protocol:    commonv1.Protocol(protocol),
		SizeBytes:   r.Size,
		DownloadURL: r.Link,
		MagnetURL:   r.MagnetURL,
		InfoHash:    r.InfoHash,
		InfoURL:     r.CommentURL,
		Seeders:     r.Seeders,
		Leechers:    r.Leechers,
		Categories:  categoryIDs(r.Categories),
		// Cloned, not aliased: ApplyTo WRITES the parsed ids into this map,
		// and the caller is the poll loop, which holds every fetched row
		// alive for the index write that follows the projection.
		IDs:          maps.Clone(r.IDs),
		IndexerFlags: indexerFlags(r),
	}

	// PublishedAt is a *metav1.Time on purpose: nil means the indexer
	// reported no date, which is genuinely different from a date. Ranking
	// uses publish age as the usenet tiebreaker, so backfilling now() makes
	// a dateless release sort as brand new and backfilling the zero time
	// makes it sort as ancient. Both are lies. Pass absence through.
	//
	// Both candidates are indexer-reported, so preferring one over the other
	// is a choice between two truths, not a backfill: Newznab-native feeds
	// carry <newznab:attr name="usenetdate"> and often a later <pubDate> for
	// when the row was indexed.
	switch {
	case !r.PubDate.IsZero():
		info.PublishedAt = ptr.To(metav1.NewTime(r.PubDate))
	case r.UsenetDate != nil && !r.UsenetDate.IsZero():
		info.PublishedAt = ptr.To(metav1.NewTime(*r.UsenetDate))
	}

	// Classify once and pin it. Parse would classify internally, but it does
	// so on the id-stripped title and does not return the kind it chose, so
	// calling ClassifyKind separately afterwards can disagree with the parse
	// that actually ran. rel.Kind is the matcher's first dispatch and a wrong
	// kind matches nothing, silently -- so the classification that steers the
	// parse and the one on the wire are the same value by construction.
	//
	// (The tidy fix is a Kind field on release.ParsedRelease, which is a
	// pkg/release change no Phase D1 task owns; it is filed as a
	// carry-forward.)
	kind := release.ClassifyKind(r.Title)
	parsed, err := release.Parse(r.Title, release.Options{Kind: kind})
	if err != nil {
		// An unparsable title is still a real release: it can match on
		// tmdb/tvdb ids, and dropping it here would hide it from the matcher
		// entirely. Publish the wire half with no parsed fields.
		return schema.Release{Info: info}
	}
	parsed.ApplyTo(&info)

	return schema.Release{
		Info: info,
		// The RAW parser title. rssmatcher.TitleYearKey applies CleanTitle
		// itself; pre-cleaning here is only harmless because CleanTitle is
		// idempotent, and relying on that is how the next change breaks it.
		ParsedTitle: parsed.Title,
		Year:        int32(parsed.Year),
		Seasons:     widen(parsed.Seasons),
		Episodes:    widen(parsed.Episodes),
		Absolute:    widen(parsed.Absolute),
		AirDate:     parsed.AirDate,
		FullSeason:  parsed.FullSeason,
		MultiSeason: parsed.MultiSeason,
		Special:     parsed.Special,
		Kind:        kind,
		Hints:       hints(parsed.Hints),
	}
}

// categoryIDs widens newznab ids to the []int32 the CRD carries. A nil slice
// stays nil so the omitempty tag drops the field rather than encoding [].
func categoryIDs(cats []newznab.CategoryID) []int32 {
	if len(cats) == 0 {
		return nil
	}
	out := make([]int32, len(cats))
	for i, c := range cats {
		out[i] = int32(c)
	}
	return out
}

// widen converts the parser's []int to the []int32 the wire schema carries,
// preserving nil so omitempty drops the key.
func widen(in []int) []int32 {
	if len(in) == 0 {
		return nil
	}
	out := make([]int32, len(in))
	for i, v := range in {
		out[i] = int32(v)
	}
	return out
}

// hints renders the parser's custom-format tag detail as the wire's
// map[string][]string, omitting every empty entry so an all-empty Hints
// encodes as an absent key rather than a map of empty lists.
func hints(h release.Hints) map[string][]string {
	out := make(map[string][]string, 6)
	put := func(k string, v []string) {
		if len(v) > 0 {
			out[k] = v
		}
	}
	put("codec", h.Codec)
	put("hdr", h.HDR)
	put("audio", h.Audio)
	put("streaming", h.Streaming)
	if h.Channels != "" {
		out["channels"] = []string{h.Channels}
	}
	if h.Container != "" {
		out["container"] = []string{h.Container}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// knownFlags is the enum ReleaseInfo.IndexerFlags declares. A tag outside it
// is dropped: the apiserver rejects the whole object otherwise, and it would
// do so at grab time, in grabarr, not here.
var knownFlags = map[string]string{
	"freeleech":    commonv1.IndexerFlagFreeleech,
	"halfleech":    commonv1.IndexerFlagHalfleech,
	"neutralleech": commonv1.IndexerFlagNeutralleech,
	"doubleupload": commonv1.IndexerFlagDoubleUpload,
	"internal":     commonv1.IndexerFlagInternal,
	"exclusive":    commonv1.IndexerFlagExclusive,
	"scene":        commonv1.IndexerFlagScene,
}

// indexerFlags maps the indexer's flags onto the CRD's closed enum. It is an
// allow-list, never a passthrough: indexer-supplied strings are untrusted
// input and this field's enum is enforced by the apiserver, far downstream.
func indexerFlags(r torznab.Release) []string {
	var out []string
	if f := r.DownloadVolumeFactor; f != nil {
		switch {
		case *f == 0:
			out = append(out, commonv1.IndexerFlagFreeleech)
		case *f > 0 && *f < 1:
			out = append(out, commonv1.IndexerFlagHalfleech)
		}
	}
	for _, tag := range r.Attrs["tag"] {
		if v, ok := knownFlags[strings.ToLower(strings.TrimSpace(tag))]; ok && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
