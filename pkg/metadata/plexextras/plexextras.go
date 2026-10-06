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

// Package plexextras is the stored form of an item's extras -- the
// trailers and clips Plex's metadata service holds for a movie, show or
// season -- in the clustarr-plex-extras KV bucket, keyed by Plex id. The
// metadata gateway is its only writer: it answers
// rpc.catalogarr.metadata.extras from the bucket and fetches from Plex on a
// miss or a stale entry (app/catalog/metadata/extras.go).
package plexextras

import (
	"encoding/json"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// Entry is one Plex id's extras and when Plex was asked for them.
type Entry struct {
	FetchedAt time.Time         `json:"fetchedAt"`
	Extras    []json.RawMessage `json:"extras"`
}

// Key is the Plex id's key in the bucket, escaped for the KV key grammar.
func Key(plexID string) string {
	return events.KVKeyToken("plex/" + plexID)
}

// Encode serialises an entry; no extras encode as an empty list.
func Encode(e Entry) ([]byte, error) {
	if e.Extras == nil {
		e.Extras = []json.RawMessage{}
	}
	return json.Marshal(e)
}

// Decode parses an entry; no extras decode as an empty, non-nil list.
func Decode(b []byte) (Entry, error) {
	var e Entry
	if err := json.Unmarshal(b, &e); err != nil {
		return Entry{}, err
	}
	if e.Extras == nil {
		e.Extras = []json.RawMessage{}
	}
	return e, nil
}
