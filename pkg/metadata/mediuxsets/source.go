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

// Package mediuxsets is MediUX as a metadata.ImageSetProvider: the client's
// sets, read through the clustarr-mediux cache, chosen by the provider's
// policy and mapped to the catalog's override shapes
// (docs/superpowers/specs/2026-10-22-mediux-image-sets-automation-design.md).
package mediuxsets

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/mediux"
	"github.com/mediactl/clustarr/pkg/metadata/mediuxcache"
)

// Lookup is what the source asks for sets: the cache, or the client
// itself through Direct when there is no bucket.
type Lookup interface {
	ShowTMDB(ctx context.Context, tvdbID string) (string, error)
	ShowSets(ctx context.Context, tmdbID string) (*mediux.Show, error)
	MovieSets(ctx context.Context, tmdbID string) (*mediux.Movie, error)
}

var _ Lookup = (*mediuxcache.Cache)(nil)

// Direct is the client as a Lookup, with no cache between.
type Direct struct{ *mediux.Client }

// ShowTMDB implements Lookup.
func (d Direct) ShowTMDB(ctx context.Context, tvdbID string) (string, error) {
	return d.ShowByTVDB(ctx, tvdbID)
}

// Source is a mediux MetadataProvider as the registry holds it.
type Source struct {
	// Client renders a set's image URLs at its quality.
	Client *mediux.Client
	// Lookup answers the set lists; the cache in production.
	Lookup Lookup
	// Policy is the provider's: preferred creators and languages. The
	// series' seasons and episodes are the query's.
	Policy mediux.Policy
}

var _ metadata.ImageSetProvider = (*Source)(nil)

// Name implements metadata.Provider.
func (s *Source) Name() string { return "mediux" }

// Capabilities implements metadata.Provider.
func (s *Source) Capabilities() metadata.Capabilities { return s.Client.Capabilities() }

// ImageSet implements metadata.ImageSetProvider: the best set for q, or
// ErrNotFound when MediUX has none (or cannot map a series' TheTVDB id).
func (s *Source) ImageSet(ctx context.Context, q metadata.ImageSetQuery) (*metadata.ImageSet, error) {
	switch q.Kind {
	case commonv1.MediaKindMovie:
		return s.movie(ctx, q)
	case commonv1.MediaKindSeries:
		return s.series(ctx, q)
	default:
		return nil, fmt.Errorf("mediux: no sets for a %s: %w", q.Kind, metadata.ErrNotFound)
	}
}

func (s *Source) series(ctx context.Context, q metadata.ImageSetQuery) (*metadata.ImageSet, error) {
	tmdb := q.TMDBID
	if tmdb == "" {
		if q.TVDBID == "" {
			return nil, fmt.Errorf("mediux: a series needs a TMDB or TheTVDB id: %w", metadata.ErrNotFound)
		}
		var err error
		if tmdb, err = s.Lookup.ShowTMDB(ctx, q.TVDBID); err != nil {
			return nil, err
		}
	}
	show, err := s.Lookup.ShowSets(ctx, tmdb)
	if err != nil {
		return nil, err
	}
	p := s.Policy
	p.Seasons, p.Episodes = q.Seasons, q.Episodes
	ch := mediux.Choose(show.Sets, p)
	if ch == nil {
		return nil, fmt.Errorf("mediux: none of %s's %d sets has a poster: %w", show.Title, len(show.Sets), metadata.ErrNotFound)
	}
	e := s.Client.Overrides(ch.Set)
	out := &metadata.ImageSet{
		Provider: "mediux", SetID: ch.Set.ID, Title: ch.Set.Title, Creator: ch.Set.Creator.Username,
		URL: mediux.SetURL(ch.Set.ID), UpdatedAt: ch.Set.Updated, Reason: ch.Reason,
	}
	for _, a := range e.Artwork {
		out.Images = append(out.Images, metadata.ImageSetImage{Type: string(a.Type), URL: a.URL})
	}
	for _, a := range e.SeasonArtwork {
		img := metadata.ImageSetImage{Type: string(a.Type), Season: a.Season, URL: a.URL}
		if a.Episode != nil {
			img.Episode = *a.Episode
		}
		out.Images = append(out.Images, img)
	}
	return out, nil
}

func (s *Source) movie(ctx context.Context, q metadata.ImageSetQuery) (*metadata.ImageSet, error) {
	if q.TMDBID == "" {
		return nil, fmt.Errorf("mediux: a movie needs a TMDB id: %w", metadata.ErrNotFound)
	}
	m, err := s.Lookup.MovieSets(ctx, q.TMDBID)
	if err != nil {
		return nil, err
	}
	ch := mediux.ChooseMovie(m.Sets, s.Policy)
	if ch == nil {
		return nil, fmt.Errorf("mediux: none of %s's %d sets has a poster: %w", m.Title, len(m.Sets), metadata.ErrNotFound)
	}
	e := s.Client.MovieOverrides(ch.Set)
	out := &metadata.ImageSet{
		Provider: "mediux", SetID: ch.Set.ID, Title: ch.Set.Title, Creator: ch.Set.Creator.Username,
		URL: mediux.SetURL(ch.Set.ID), UpdatedAt: ch.Set.Updated, Reason: ch.Reason,
	}
	for _, a := range e.Artwork {
		out.Images = append(out.Images, metadata.ImageSetImage{Type: string(a.Type), URL: a.URL})
	}
	return out, nil
}

// Overrides maps a chosen set's images to the catalog's shapes: the item's
// own to spec.artwork's, a season's and an episode's to
// spec.seasonArtwork's.
func Overrides(images []metadata.ImageSetImage) (artwork []catalogv1.ArtworkOverride, seasons []catalogv1.SeasonArtworkOverride) {
	for _, img := range images {
		t := catalogv1.ImageType(img.Type)
		switch {
		case img.Season == 0 && img.Episode == 0:
			artwork = append(artwork, catalogv1.ArtworkOverride{Type: t, URL: img.URL})
		case img.Episode == 0:
			seasons = append(seasons, catalogv1.SeasonArtworkOverride{Season: img.Season, Type: t, URL: img.URL})
		default:
			n := img.Episode
			seasons = append(seasons, catalogv1.SeasonArtworkOverride{Season: img.Season, Episode: &n, Type: t, URL: img.URL})
		}
	}
	return artwork, seasons
}

// ErrNoSet reports a query the provider answered NotFound for.
var ErrNoSet = errors.New("mediux: no set")

// EpisodesOf turns a record's text-keyed episode counts into the query's.
func EpisodesOf(m map[string]int32) map[int32]int32 {
	if len(m) == 0 {
		return nil
	}
	out := make(map[int32]int32, len(m))
	for k, v := range m {
		n, err := strconv.ParseInt(k, 10, 32)
		if err != nil {
			continue
		}
		out[int32(n)] = v
	}
	return out
}
