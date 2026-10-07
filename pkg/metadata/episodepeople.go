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

package metadata

import "context"

// EpisodePeopleProvider fetches one episode's guest cast and crew by the
// provider's episode id: TheTVDB's /episodes/{id}/extended, filed as the
// Plex provider's episode Role, Director, Writer and Producer. A
// SeriesProvider that is one is asked for each episode the library holds
// a file of (app/catalog/metadata, refreshEpisodePeople).
type EpisodePeopleProvider interface {
	EpisodePeople(ctx context.Context, episodeID string) ([]Person, error)
}

// EpisodePeopleOf is the registry's first series provider that is an
// EpisodePeopleProvider, or nil.
func EpisodePeopleOf(r *Registry) EpisodePeopleProvider {
	if r == nil {
		return nil
	}
	for _, p := range r.Series {
		if ep, ok := p.(EpisodePeopleProvider); ok {
			return ep
		}
	}
	return nil
}
