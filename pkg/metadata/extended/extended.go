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
package extended

import (
	"encoding/json"
	"slices"
	"strconv"

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

// Similar is a title the provider recommends alongside the item.
type Similar struct {
	Title  string `json:"title"`
	Year   int32  `json:"year,omitempty"`
	TmdbID int64  `json:"tmdbID,omitempty"`
	TvdbID int64  `json:"tvdbID,omitempty"`
}

// Doc is one item's document.
type Doc struct {
	Role     []Person  `json:"role,omitempty"`
	Director []Person  `json:"director,omitempty"`
	Writer   []Person  `json:"writer,omitempty"`
	Producer []Person  `json:"producer,omitempty"`
	Similar  []Similar `json:"similar,omitempty"`
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
	for _, s := range similar {
		if len(d.Similar) >= MaxSimilar {
			break
		}
		ref := Similar{Title: s.Title, Year: s.Year}
		ref.TmdbID, _ = strconv.ParseInt(s.IDs[metadata.KeyTMDB], 10, 64)
		ref.TvdbID, _ = strconv.ParseInt(s.IDs[metadata.KeyTVDB], 10, 64)
		d.Similar = append(d.Similar, ref)
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
