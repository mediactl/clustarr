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

// Package extended is the per-item document of the metadata the Plex
// provider shows but no CRD carries: the people (Role, Director, Writer,
// Producer) and the similar titles (spec 2026-09-30 plex-full-metadata-
// response §4). Every clustarr service caches Movies, Series and Episodes,
// so a cast list per Episode would grow every cache; the document lives in
// the clustarr-metadata-extended KV bucket instead. The metadata gateway is
// its only writer; the ui reads it.
//
// Since the 2026-11-10 design (§5.4) an Artist and an Audiobook have a
// document too, in the same shape, so the Plex roots map them as they map
// a movie's: an artist's members and similar artists (FromArtist), an
// audiobook's narrators as its Role list and its authors as its Writer list
// (FromAudiobook).
package extended

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// Caps on the document, each list and the whole.
const (
	MaxRole     = 50
	MaxCrew     = 20 // director, writer and producer each
	MaxSimilar  = 20
	MaxDocBytes = 64 << 10
)

// Person is one credit as Plex shows it.
type Person struct {
	Name      string `json:"name"`
	Character string `json:"character,omitempty"`
	Job       string `json:"job,omitempty"`
	Order     int32  `json:"order,omitempty"`
	Photo     string `json:"photo,omitempty"`
}

// Similar is a title the provider recommends alongside the item: a movie
// or series by its TMDB or TVDB id, an artist by its MusicBrainz id.
type Similar struct {
	Title      string `json:"title"`
	Year       int32  `json:"year,omitempty"`
	TmdbID     int64  `json:"tmdbID,omitempty"`
	TvdbID     int64  `json:"tvdbID,omitempty"`
	MBArtistID string `json:"mbArtistID,omitempty"`
}

// Doc is one item's document.
type Doc struct {
	Role     []Person  `json:"role,omitempty"`
	Director []Person  `json:"director,omitempty"`
	Writer   []Person  `json:"writer,omitempty"`
	Producer []Person  `json:"producer,omitempty"`
	Similar  []Similar `json:"similar,omitempty"`
	// Collection is a movie's collection's summary and artwork, which the
	// Plex provider shows on the collection.
	Collection *Collection `json:"collection,omitempty"`
}

// Collection is what the Plex provider shows on a movie collection beside
// the name and ids status.metadata.collection already carries.
type Collection struct {
	Summary string `json:"summary,omitempty"`
	Poster  string `json:"poster,omitempty"`
	Art     string `json:"art,omitempty"`
}

// MaxCollectionSummary caps the summary, in bytes.
const MaxCollectionSummary = 4096

// FromCollection is a provider collection's document: its summary, its
// first poster and its first fanart; nil for none.
func FromCollection(c *metadata.Collection) *Collection {
	if c == nil {
		return nil
	}
	out := &Collection{Summary: c.Overview}
	if len(out.Summary) > MaxCollectionSummary {
		out.Summary = strings.ToValidUTF8(out.Summary[:MaxCollectionSummary], "")
	}
	for _, img := range c.Images {
		switch {
		case img.Type == metadata.ImageTypePoster && out.Poster == "":
			out.Poster = img.URL
		case img.Type == metadata.ImageTypeFanart && out.Art == "":
			out.Art = img.URL
		}
	}
	return out
}

// Key is the item's key in the bucket: its kind and UID, escaped for the
// KV key grammar.
func Key(kind commonv1.MediaKind, uid types.UID) string {
	return events.KVKeyToken(string(kind) + "/" + string(uid))
}

// FromPeople files a provider's credits under the document's lists, the cast
// in billing order, each list at its cap.
func FromPeople(people []metadata.Person, similar []metadata.SimilarRef) Doc {
	var d Doc
	convert := func(p metadata.Person) Person {
		return Person{Name: p.Name, Character: p.Character, Job: p.Job, Order: p.Order, Photo: p.ImageURL}
	}
	var cast []metadata.Person
	for _, p := range people {
		switch p.Kind {
		case metadata.PersonCast:
			cast = append(cast, p)
		case metadata.PersonDirector:
			if len(d.Director) < MaxCrew {
				d.Director = append(d.Director, convert(p))
			}
		case metadata.PersonWriter:
			if len(d.Writer) < MaxCrew {
				d.Writer = append(d.Writer, convert(p))
			}
		case metadata.PersonProducer:
			if len(d.Producer) < MaxCrew {
				d.Producer = append(d.Producer, convert(p))
			}
		}
	}
	slices.SortStableFunc(cast, func(a, b metadata.Person) int { return int(a.Order) - int(b.Order) })
	for _, p := range cast {
		if len(d.Role) >= MaxRole {
			break
		}
		d.Role = append(d.Role, convert(p))
	}
	d.Similar = fromSimilar(similar)
	return d
}

// fromSimilar files the similar titles, each by every id it carries, at
// the cap.
func fromSimilar(similar []metadata.SimilarRef) []Similar {
	var out []Similar
	for _, s := range similar {
		if len(out) >= MaxSimilar {
			break
		}
		ref := Similar{Title: s.Title, Year: s.Year, MBArtistID: s.IDs[metadata.KeyMBArtist]}
		ref.TmdbID, _ = strconv.ParseInt(s.IDs[metadata.KeyTMDB], 10, 64)
		ref.TvdbID, _ = strconv.ParseInt(s.IDs[metadata.KeyTVDB], 10, 64)
		out = append(out, ref)
	}
	return out
}

// FromArtist is an artist's document (2026-11-10 design §5.4): its members
// under Role, in the provider's order, each one's instrument or role as
// its Job and Character (the Plex roots read a Role's Character, as they
// read a crew credit's Job), and the artists related to it under Similar,
// by MusicBrainz id. A provider that carries neither gives an empty
// document, which still replaces an earlier refresh's.
func FromArtist(a *metadata.Artist) Doc {
	var d Doc
	if a == nil {
		return d
	}
	for _, m := range a.Members {
		if m.Name == "" || (m.Kind != "" && m.Kind != metadata.PersonMember) || len(d.Role) >= MaxRole {
			continue
		}
		d.Role = append(d.Role, Person{Name: m.Name, Character: m.Job, Job: m.Job, Order: m.Order, Photo: m.ImageURL})
	}
	d.Similar = fromSimilar(a.Similar)
	return d
}

// FromAudiobook is an audiobook's document (2026-11-10 design §5.4): the
// narrators under Role, each with "Narrator" as the role Plex shows, and
// the authors under Writer with "Author" as their Job, both in the
// recording's own order. Audnexus names them without photos.
func FromAudiobook(a *metadata.Audiobook) Doc {
	var d Doc
	if a == nil {
		return d
	}
	for i, n := range a.Narrators {
		if n == "" || len(d.Role) >= MaxRole {
			continue
		}
		d.Role = append(d.Role, Person{Name: n, Character: "Narrator", Job: "Narrator", Order: int32(i)})
	}
	for i, au := range a.Authors {
		if au.Name == "" || len(d.Writer) >= MaxCrew {
			continue
		}
		d.Writer = append(d.Writer, Person{Name: au.Name, Job: "Author", Order: int32(i)})
	}
	return d
}

// Encode marshals d, dropping entries from the end of the longest list until
// it fits MaxDocBytes, so an outsized cast is trimmed from the bottom of the
// bill rather than refused.
func Encode(d Doc) ([]byte, error) {
	for {
		b, err := json.Marshal(d)
		if err != nil || len(b) <= MaxDocBytes {
			return b, err
		}
		lists := []*[]Person{&d.Role, &d.Director, &d.Writer, &d.Producer}
		longest := lists[0]
		for _, l := range lists[1:] {
			if len(*l) > len(*longest) {
				longest = l
			}
		}
		switch {
		case len(*longest) > 0:
			*longest = (*longest)[:len(*longest)-1]
		case len(d.Similar) > 0:
			d.Similar = d.Similar[:len(d.Similar)-1]
		default:
			return b, nil
		}
	}
}

// Decode unmarshals a stored document.
func Decode(b []byte) (Doc, error) {
	var d Doc
	err := json.Unmarshal(b, &d)
	return d, err
}
