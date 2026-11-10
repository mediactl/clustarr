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

package musicbrainz

import (
	"cmp"
	"slices"
	"strings"
	"time"

	mb "go.uploadedlobster.com/musicbrainzws2"

	"github.com/mediactl/clustarr/pkg/metadata"
)

// memberOfBand is the artist-artist relation between a person and the
// group they are or were in (https://musicbrainz.org/relationship/
// 5be4c609-9afa-4ea0-910b-12ffb71e3821): its entity0 is the member, its
// entity1 the group, so a group's lookup sees it "backward" and a person's
// "forward". MusicBrainz records one relation per instrument or role the
// member had (Thom Yorke is five "member of band" relations to Radiohead:
// lead vocals, acoustic guitar, electric guitar, synthesizer, piano), and
// the "original" attribute marks a founding member.
const (
	memberOfBand      = "member of band"
	originalAttribute = "original"
	directionBackward = "backward"
)

// maxMembers bounds Artist.Members: an orchestra or a collective can carry
// hundreds of "member of band" relations, and the extended document the
// members reach is read whole.
const maxMembers = 100

// The roles a member is listed under (metadata.Person.Role), one of three
// by priority -- a relation that has ended outranks a founding one, so a
// member who left reads as former whatever else is true. A group's lookup
// lists its members; a person's lists the groups they are in, under the
// same words with "of" (2026-11-10 design §5.4: "for a Person the groups
// they are in go under Members with role member of").
const (
	roleMember         = "member"
	roleFoundingMember = "founding member"
	roleFormerMember   = "former member"
)

// member accumulates one artist's "member of band" relations (one per
// instrument) into a single metadata.Person.
type member struct {
	id          string
	name        string
	instruments []string
	founding    bool
	// ended is true while every relation of the member has ended; one
	// still open means a current member (rejoined, or a role that
	// outlived another).
	ended bool
	// begin is the earliest begin date of the member's relations, for
	// ordering and Person.Begin; end the latest end date, Person.End once
	// every relation has ended. Zero when none carries one.
	begin time.Time
	end   time.Time
	first int
}

// mapMembers reads an artist's "member of band" relations into
// metadata.Person entries, one per related artist whatever the number of
// instruments, each Kind metadata.PersonMember: Name the related artist's
// credited name, Job its instruments and roles in the order MusicBrainz
// listed them ("lead vocals, acoustic guitar, electric guitar"), Role one
// of member, founding member or former member -- with "of" appended when
// the looked-up artist is the member and the related artist the group --
// and Order the listing order: current members before former ones, then
// the earliest to join first (an unknown date last), then by name. The
// member's MBID is IDs[metadata.KeyMBArtist], Begin the earliest begin
// date across the member's relations and End the latest end date once
// every one of them has ended -- a member with a relation still open is
// current, so the end of one of their roles is no End (task C4a,
// 2026-11-10; until then the ids and dates were decoded for the ordering
// alone, since Person had no field for them).
//
// Artist.Similar stays empty on purpose: MusicBrainz has no "similar
// artist" relation -- its collaboration, supporting musician, subgroup,
// tribute and rename relations are not likeness -- and no other configured
// client (fanart.tv, Cover Art Archive, wikipedia) answers one; Last.fm's
// artist.getSimilar would, and is not a provider here.
func mapMembers(raw mb.Artist) []metadata.Person {
	byID := map[string]*member{}
	var order []*member
	for i, rel := range raw.Relations {
		if rel.Type != memberOfBand || rel.Artist == nil {
			continue
		}
		key := string(rel.Artist.ID)
		if key == "" {
			key = rel.Artist.Name
		}
		m, ok := byID[key]
		if !ok {
			m = &member{id: string(rel.Artist.ID), name: memberName(rel), ended: true, first: i}
			byID[key] = m
			order = append(order, m)
		}
		for _, attr := range rel.Attributes {
			if attr == originalAttribute {
				m.founding = true
				continue
			}
			if attr != "" && !slices.Contains(m.instruments, attr) {
				m.instruments = append(m.instruments, attr)
			}
		}
		if !rel.HasEnded {
			m.ended = false
		}
		if t, ok := partialDateToTime(rel.Begin); ok && (m.begin.IsZero() || t.Before(m.begin)) {
			m.begin = t
		}
		if t, ok := partialDateToTime(rel.End); ok && t.After(m.end) {
			m.end = t
		}
	}
	if len(order) == 0 {
		return nil
	}

	// A person's lookup sees the relation forward (the person is entity0,
	// the member), a group's backward; the direction of the first such
	// relation decides the wording for them all.
	suffix := ""
	for _, rel := range raw.Relations {
		if rel.Type == memberOfBand && rel.Artist != nil {
			if rel.Direction != directionBackward {
				suffix = " of"
			}
			break
		}
	}

	slices.SortStableFunc(order, func(a, b *member) int {
		if a.ended != b.ended {
			if a.ended {
				return 1
			}
			return -1
		}
		switch {
		case a.begin.IsZero() && !b.begin.IsZero():
			return 1
		case !a.begin.IsZero() && b.begin.IsZero():
			return -1
		case !a.begin.IsZero() && !b.begin.IsZero() && !a.begin.Equal(b.begin):
			return a.begin.Compare(b.begin)
		}
		return cmp.Or(strings.Compare(a.name, b.name), cmp.Compare(a.first, b.first))
	})
	if len(order) > maxMembers {
		order = order[:maxMembers]
	}

	people := make([]metadata.Person, 0, len(order))
	for i, m := range order {
		role := roleMember
		switch {
		case m.ended:
			role = roleFormerMember
		case m.founding:
			role = roleFoundingMember
		}
		p := metadata.Person{
			Name:  m.name,
			Kind:  metadata.PersonMember,
			Role:  role + suffix,
			Job:   strings.Join(m.instruments, ", "),
			Order: int32(i),
		}
		if m.id != "" {
			p.IDs = metadata.ExternalIDs{metadata.KeyMBArtist: m.id}
		}
		if !m.begin.IsZero() {
			begin := m.begin
			p.Begin = &begin
		}
		if m.ended && !m.end.IsZero() {
			end := m.end
			p.End = &end
		}
		people = append(people, p)
	}
	return people
}

// memberName is the related artist's name as the relation credits it: the
// source credit when the looked-up artist is the target (a group's
// lookup, direction backward), the target credit otherwise, else the
// artist's own name.
func memberName(rel mb.Relation) string {
	credit := rel.TargetCredit
	if rel.Direction == directionBackward {
		credit = rel.SourceCredit
	}
	if credit = strings.TrimSpace(credit); credit != "" {
		return credit
	}
	return rel.Artist.Name
}
