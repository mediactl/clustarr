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
// sets, read through the clustarr-mediux cache, composed slot by slot by
// the provider's policy and mapped to the catalog's override shapes
// (docs/superpowers/specs/2026-10-22-mediux-image-sets-automation-design.md).
package mediuxsets

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

// ImageSet implements metadata.ImageSetProvider: q's images composed from
// its sets, or ErrNotFound when MediUX has none (or cannot map a series'
// TheTVDB id).
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
	sets, err := s.showSets(ctx, q)
	if err != nil {
		return nil, err
	}
	if q.SetID != "" && !slices.ContainsFunc(sets, func(x mediux.ShowSet) bool { return x.ID == q.SetID }) {
		set, err := s.Client.ShowSet(ctx, q.SetID)
		if err != nil {
			return nil, err
		}
		sets = append([]mediux.ShowSet{*set}, sets...)
	}
	p := s.Policy
	p.Seasons, p.Episodes = q.Seasons, q.Episodes
	c := mediux.ComposeShow(sets, p, q.SetID)
	if c == nil {
		return nil, fmt.Errorf("mediux: none of the series' %d sets has an image: %w", len(sets), metadata.ErrNotFound)
	}
	return s.answer(c), nil
}

// showSets is the series' sets by its TMDB id, else its TheTVDB id mapped
// to one. A pinned set needs neither: with no id it is taken alone.
func (s *Source) showSets(ctx context.Context, q metadata.ImageSetQuery) ([]mediux.ShowSet, error) {
	tmdb := q.TMDBID
	if tmdb == "" && q.TVDBID != "" {
		var err error
		if tmdb, err = s.Lookup.ShowTMDB(ctx, q.TVDBID); err != nil && (q.SetID == "" || !errors.Is(err, metadata.ErrNotFound)) {
			return nil, err
		}
	}
	if tmdb == "" {
		if q.SetID != "" {
			return nil, nil
		}
		return nil, fmt.Errorf("mediux: a series needs a TMDB or TheTVDB id: %w", metadata.ErrNotFound)
	}
	show, err := s.Lookup.ShowSets(ctx, tmdb)
	switch {
	case q.SetID != "" && errors.Is(err, metadata.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return show.Sets, nil
}

func (s *Source) movie(ctx context.Context, q metadata.ImageSetQuery) (*metadata.ImageSet, error) {
	m := &mediux.Movie{}
	if q.TMDBID != "" {
		got, err := s.Lookup.MovieSets(ctx, q.TMDBID)
		switch {
		case q.SetID != "" && errors.Is(err, metadata.ErrNotFound):
		case err != nil:
			return nil, err
		default:
			m = got
		}
	} else if q.SetID == "" {
		return nil, fmt.Errorf("mediux: a movie needs a TMDB id: %w", metadata.ErrNotFound)
	}
	pinned := func(x mediux.MovieSet) bool { return x.ID == q.SetID }
	if q.SetID != "" && !slices.ContainsFunc(m.Sets, pinned) && !slices.ContainsFunc(m.CollectionSets, pinned) {
		set, err := s.Client.MovieSet(ctx, q.SetID)
		if err != nil {
			return nil, err
		}
		cp := *m
		cp.Sets = append([]mediux.MovieSet{*set}, m.Sets...)
		m = &cp
	}
	c := mediux.ComposeMovie(m, s.Policy, q.SetID)
	if c == nil {
		return nil, fmt.Errorf("mediux: none of the film's %d sets has an image: %w", len(m.Sets)+len(m.CollectionSets), metadata.ErrNotFound)
	}
	return s.answer(c), nil
}

// answer is a composition as the provider's answer.
func (s *Source) answer(c *mediux.Composition) *metadata.ImageSet {
	out := &metadata.ImageSet{
		Provider: "mediux", SetID: c.Anchor.ID, Title: c.Anchor.Title, Creator: c.Anchor.Creator,
		URL: mediux.SetURL(c.Anchor.Kind, c.Anchor.ID), UpdatedAt: c.Anchor.Updated, Reason: c.Reason,
	}
	for _, p := range c.Picks {
		img := metadata.ImageSetImage{URL: s.Client.ImageURL(p.File), SetID: p.Set, Season: p.Slot.Season, Episode: p.Slot.Episode}
		switch p.Slot.Kind {
		case mediux.SlotPoster:
			img.Type, img.Scope = string(catalogv1.ImageTypePoster), metadata.ImageScopeItem
		case mediux.SlotBackdrop:
			img.Type, img.Scope = string(catalogv1.ImageTypeFanart), metadata.ImageScopeItem
		case mediux.SlotSeasonPoster:
			img.Type, img.Scope = string(catalogv1.ImageTypePoster), metadata.ImageScopeSeason
		case mediux.SlotTitlecard:
			img.Type, img.Scope = string(catalogv1.ImageTypeScreenshot), metadata.ImageScopeEpisode
		default:
			continue
		}
		out.Images = append(out.Images, img)
	}
	for _, u := range c.Sets {
		out.Sets = append(out.Sets, metadata.ImageSetSource{
			SetID: u.ID, Title: u.Title, Creator: u.Creator, Kind: string(u.Kind), URL: mediux.SetURL(u.Kind, u.ID), Images: u.Images,
		})
	}
	return out
}

// Overrides maps an answer's images to the catalog's shapes by scope: the
// item's own to spec.artwork's, a season's and an episode's to
// spec.seasonArtwork's.
func Overrides(images []metadata.ImageSetImage) (artwork []catalogv1.ArtworkOverride, seasons []catalogv1.SeasonArtworkOverride) {
	for _, img := range images {
		t := catalogv1.ImageType(img.Type)
		switch img.Scope {
		case metadata.ImageScopeItem:
			artwork = append(artwork, catalogv1.ArtworkOverride{Type: t, URL: img.URL})
		case metadata.ImageScopeSeason:
			seasons = append(seasons, catalogv1.SeasonArtworkOverride{Season: img.Season, Type: t, URL: img.URL})
		case metadata.ImageScopeEpisode:
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
