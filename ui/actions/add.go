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

package actions

import (
	"context"
	"fmt"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/names"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// AddRequest is one Add New submission (docs/superpowers/specs/
// 2026-09-29-add-new-design.md). Title only names the object; the
// provider fills the item's metadata once it exists. Each option field is
// the kind's own spec field; one a kind does not have is ignored.
type AddRequest struct {
	Kind                commonv1.MediaKind
	Namespace           string
	Title               string
	ProviderID          string
	RootFolderRef       string
	QualityProfileRef   string
	Monitored           bool
	Monitor             string
	SearchOnAdd         bool
	MinimumAvailability string
	SeriesType          string
	SeasonFolder        bool
	SearchCutoffUnmet   bool
	MonitorNewItems     string
}

// AddableKinds are the kinds AddItem creates: the four library types.
var AddableKinds = []commonv1.MediaKind{
	commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindArtist, commonv1.MediaKindAuthor,
}

// AddItem creates the catalog item req describes, with spec only and under
// FieldManager, named as importarr names it (pkg/names), so an item the UI
// adds and one a list or the rescan finds are one object. An item that
// already exists under that name is not an error: existed is true. The
// apiserver validates every enum; a request with no namespace, id, root
// folder or profile, a kind Add New does not add, or an id that cannot key
// the kind is ErrInvalid and writes nothing.
func AddItem(ctx context.Context, c Creator, req AddRequest) (name string, existed bool, err error) {
	ctx, span := tracing.Start(ctx, "ui.actions.AddItem")
	defer span.End()

	obj, err := buildItem(req)
	if err != nil {
		tracing.RecordError(span, err)
		return "", false, err
	}
	name = obj.GetName()
	if err := c.Create(ctx, obj, client.FieldOwner(FieldManager)); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return name, true, nil
		}
		err = fmt.Errorf("actions: add %s %s/%s: %w", req.Kind, req.Namespace, name, err)
		tracing.RecordError(span, err)
		return "", false, err
	}
	logging.FromContext(ctx).Info("ui action: item added", "kind", req.Kind, "namespace", req.Namespace, "name", name)
	return name, false, nil
}

// buildItem renders req as the object AddItem creates.
func buildItem(req AddRequest) (client.Object, error) {
	if req.Namespace == "" || req.ProviderID == "" || req.RootFolderRef == "" || req.QualityProfileRef == "" {
		return nil, fmt.Errorf("%w: an add needs a namespace, a provider id, a root folder and a quality profile", ErrInvalid)
	}
	source := &commonv1.AddSource{} // an empty importListRef: added by hand
	switch req.Kind {
	case commonv1.MediaKindMovie:
		id, err := positiveID(req.ProviderID)
		if err != nil {
			return nil, err
		}
		return &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: names.Movie(req.Title, id), Namespace: req.Namespace},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: id, Monitored: new(req.Monitored),
				QualityProfileRef: req.QualityProfileRef, RootFolderRef: req.RootFolderRef,
				MinimumAvailability: catalogv1alpha1.MinimumAvailability(req.MinimumAvailability),
				AddOptions: catalogv1alpha1.MovieAddOptions{
					Monitor: catalogv1alpha1.MovieMonitorMode(req.Monitor), SearchForMovie: new(req.SearchOnAdd),
				},
				Source: source,
			},
		}, nil
	case commonv1.MediaKindSeries:
		id, err := positiveID(req.ProviderID)
		if err != nil {
			return nil, err
		}
		return &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: names.Series(req.Title, id), Namespace: req.Namespace},
			Spec: catalogv1alpha1.SeriesSpec{
				TvdbID: id, Monitored: new(req.Monitored),
				SeriesType:        catalogv1alpha1.SeriesType(req.SeriesType),
				MonitorNewItems:   catalogv1alpha1.MonitorNewChildrenMode(req.MonitorNewItems),
				SeasonFolder:      new(req.SeasonFolder),
				QualityProfileRef: req.QualityProfileRef, RootFolderRef: req.RootFolderRef,
				AddOptions: catalogv1alpha1.SeriesAddOptions{
					Monitor:              catalogv1alpha1.SeriesMonitorMode(req.Monitor),
					SearchForMissing:     new(req.SearchOnAdd),
					SearchForCutoffUnmet: new(req.SearchCutoffUnmet),
				},
				Source: source,
			},
		}, nil
	case commonv1.MediaKindArtist:
		return &catalogv1alpha1.Artist{
			ObjectMeta: metav1.ObjectMeta{Name: names.Artist(req.Title, req.ProviderID), Namespace: req.Namespace},
			Spec: catalogv1alpha1.ArtistSpec{
				MusicBrainzID: req.ProviderID, Monitored: new(req.Monitored),
				MonitorNewItems:   catalogv1alpha1.MonitorNewItemsMode(req.MonitorNewItems),
				QualityProfileRef: req.QualityProfileRef, RootFolderRef: req.RootFolderRef,
				AddOptions: catalogv1alpha1.ArtistAddOptions{
					Monitor: catalogv1alpha1.ArtistMonitorMode(req.Monitor), SearchForMissing: req.SearchOnAdd,
				},
				Source: source,
			},
		}, nil
	case commonv1.MediaKindAuthor:
		return &catalogv1alpha1.Author{
			ObjectMeta: metav1.ObjectMeta{Name: names.Author(req.Title, req.ProviderID), Namespace: req.Namespace},
			Spec: catalogv1alpha1.AuthorSpec{
				OpenLibraryID: req.ProviderID, Monitored: new(req.Monitored),
				MonitorNewItems:   catalogv1alpha1.MonitorNewChildrenMode(req.MonitorNewItems),
				QualityProfileRef: req.QualityProfileRef, RootFolderRef: req.RootFolderRef,
				AddOptions: catalogv1alpha1.AuthorAddOptions{
					Monitor: catalogv1alpha1.AuthorMonitorMode(req.Monitor), SearchForMissing: req.SearchOnAdd,
				},
				MetadataProfile: catalogv1alpha1.BookMetadataProfile{AllowedLanguages: []string{addedAuthorLanguage}},
				Source:          source,
			},
		}, nil
	}
	return nil, fmt.Errorf("%w: %q is not a kind Add New adds", ErrInvalid, req.Kind)
}

// addedAuthorLanguage is the one language an author added here lists works
// in (spec.metadataProfile.allowedLanguages): the MetadataProvider's
// default language, the one titles are fetched in. Open Library catalogues
// translations as works of their own -- Dostoevsky came in eleven languages
// -- and without it every one became a Book. An owner reading another
// language edits the author.
const addedAuthorLanguage = "en"

// positiveID parses a TMDB or TVDB id, which is a positive integer.
func positiveID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%w: %q is not a provider id", ErrInvalid, s)
	}
	return id, nil
}

// AddItem is [AddItem] over the Actions' writer.
func (a *Actions) AddItem(ctx context.Context, req AddRequest) (string, bool, error) {
	if a == nil || a.w == nil {
		return "", false, ErrNoWriter
	}
	return AddItem(ctx, a.w, req)
}
