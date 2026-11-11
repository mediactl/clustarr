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
	"regexp"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/names"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// AddRequest is one Add New submission (docs/superpowers/specs/
// 2026-09-29-add-new-design.md; the non-video kinds since the 2026-11-10
// design §9.1). Title only names the object; the provider fills the item's
// metadata once it exists. Each option field is the kind's own spec field;
// one a kind does not have is ignored.
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
	// Source is a Comic's metadata source (comicvine, mangadex), which with
	// ProviderID -- the ComicVine volume id or the MangaDex manga UUID --
	// identifies the volume; every other kind ignores it.
	Source string
	// ComicKind is a Comic's spec.kind (comic, manga); empty takes the CRD
	// default.
	ComicKind string
	// Region is an Audiobook's Audible marketplace (spec.region); empty
	// takes the CRD default, us.
	Region string
	// AuthorRef is an Audiobook's spec.authorRef: the Author in Namespace
	// the hit's first author resolved to (2026-11-10 design D11, §5.6;
	// ui/add.go's audiobookAuthorRef, through the bound MetadataLookup and
	// the namespace's Authors by name). Empty links none; every other kind
	// ignores it.
	AuthorRef string
	// Folder pins an Artist's, an Author's or an Audiobook's spec.folder:
	// the item's folder under its root, relative to the root and taken as
	// written, as a root scan pins the folder it found the item in (the
	// 2026-11-10 non-video libraries design's D16). The Unmatched page's
	// confirmation of a folder the scan could not identify carries it (D20),
	// so the added item takes the folder its files are already in. Empty
	// pins nothing (the root's naming preset decides); an absolute path or
	// a "." or ".." component is ErrInvalid; every other kind ignores it.
	Folder string
}

// AddableKinds are the kinds AddItem creates: the four library types of
// the 2026-09-29 design, and since the 2026-11-10 design (§9.1) a Comic,
// an Audiobook and a standalone Book.
var AddableKinds = []commonv1.MediaKind{
	commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindArtist, commonv1.MediaKindAuthor,
	commonv1.MediaKindComic, commonv1.MediaKindAudiobook, commonv1.MediaKindBook,
}

// AddItem creates the catalog item req describes, with spec only and under
// FieldManager, named as import names it (pkg/names), so an item the UI
// adds and one a list or the rescan finds are one object. An item that
// already exists under that name is not an error: existed is true. The
// apiserver validates every enum; a request with no namespace, id, root
// folder or profile, a kind Add New does not add, an id that cannot key
// the kind, or a folder pin that cannot lie under the root is ErrInvalid
// and writes nothing.
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

// Validate is AddItem's own check of req without the write: the ErrInvalid
// buildItem raises for a request with no namespace, id, root folder or
// profile, a kind Add New does not add, an id that cannot key the kind or
// a folder pin that cannot lie under the root, else nil. ui/add.go asks it before the work an add spends ahead of the
// write (an audiobook's author lookup, a gateway round trip), so a form
// AddItem would refuse costs none of it.
func Validate(req AddRequest) error {
	_, err := buildItem(req)
	return err
}

// buildItem renders req as the object AddItem creates.
func buildItem(req AddRequest) (client.Object, error) {
	if req.Namespace == "" || req.ProviderID == "" || req.RootFolderRef == "" || req.QualityProfileRef == "" {
		return nil, fmt.Errorf("%w: an add needs a namespace, a provider id, a root folder and a quality profile", ErrInvalid)
	}
	folder, err := folderPin(req.Folder)
	if err != nil {
		return nil, err
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
				QualityProfileRef: req.QualityProfileRef, RootFolderRef: req.RootFolderRef, Folder: folder,
				AddOptions: catalogv1alpha1.ArtistAddOptions{
					Monitor: catalogv1alpha1.ArtistMonitorMode(req.Monitor), SearchForMissing: ptr.To(req.SearchOnAdd),
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
				QualityProfileRef: req.QualityProfileRef, RootFolderRef: req.RootFolderRef, Folder: folder,
				AddOptions: catalogv1alpha1.AuthorAddOptions{
					Monitor: catalogv1alpha1.AuthorMonitorMode(req.Monitor), SearchForMissing: ptr.To(req.SearchOnAdd),
				},
				MetadataProfile: catalogv1alpha1.BookMetadataProfile{AllowedLanguages: []string{addedAuthorLanguage}},
				Source:          source,
			},
		}, nil
	case commonv1.MediaKindComic:
		// The source is the volume's identity with the id (spec.source and
		// spec.sourceID, both immutable), so it is never guessed from the
		// id's shape.
		src := catalogv1alpha1.ComicSourceProvider(req.Source)
		if src != catalogv1alpha1.ComicSourceComicVine && src != catalogv1alpha1.ComicSourceMangaDex {
			return nil, fmt.Errorf("%w: %q is not a comic source (comicvine, mangadex)", ErrInvalid, req.Source)
		}
		spec := catalogv1alpha1.ComicSpec{
			Source: src, SourceID: req.ProviderID, Kind: catalogv1alpha1.ComicKind(req.ComicKind),
			Monitored:         new(req.Monitored),
			QualityProfileRef: req.QualityProfileRef, RootFolderRef: req.RootFolderRef,
			AddOptions: catalogv1alpha1.ComicAddOptions{
				Monitor: catalogv1alpha1.ComicMonitorMode(req.Monitor), SearchForMissing: ptr.To(req.SearchOnAdd),
			},
			AddSource: source,
		}
		// A comic has no monitor-new mode, only the flag: the form's
		// "all"/"none" is spec.monitorNewIssues; unsaid, the CRD default.
		switch req.MonitorNewItems {
		case string(catalogv1alpha1.MonitorNewChildrenAll):
			spec.MonitorNewIssues = ptr.To(true)
		case string(catalogv1alpha1.MonitorNewChildrenNone):
			spec.MonitorNewIssues = ptr.To(false)
		}
		return &catalogv1alpha1.Comic{
			ObjectMeta: metav1.ObjectMeta{Name: names.Comic(req.Title, req.Source, req.ProviderID), Namespace: req.Namespace},
			Spec:       spec,
		}, nil
	case commonv1.MediaKindAudiobook:
		spec := catalogv1alpha1.AudiobookSpec{
			ASIN: req.ProviderID, Region: catalogv1alpha1.AudiobookRegion(req.Region), Monitored: new(req.Monitored),
			QualityProfileRef: req.QualityProfileRef, RootFolderRef: req.RootFolderRef, Folder: folder,
			AddOptions: catalogv1alpha1.AudiobookAddOptions{SearchForMissing: ptr.To(req.SearchOnAdd)},
			Source:     source,
		}
		// The author link, when the hit's author resolved to an Author of
		// the namespace (D11); unset, not "", so an unlinked audiobook
		// carries no authorRef at all.
		if req.AuthorRef != "" {
			spec.AuthorRef = ptr.To(req.AuthorRef)
		}
		return &catalogv1alpha1.Audiobook{
			ObjectMeta: metav1.ObjectMeta{Name: names.Audiobook(req.Title, req.ProviderID), Namespace: req.Namespace},
			Spec:       spec,
		}, nil
	case commonv1.MediaKindBook:
		// A standalone book: no author, so its profile and root folder are
		// its own (the CRD's rule for a book without an authorRef).
		if !openLibraryWorkID.MatchString(req.ProviderID) {
			return nil, fmt.Errorf("%w: %q is not an Open Library work key", ErrInvalid, req.ProviderID)
		}
		return &catalogv1alpha1.Book{
			ObjectMeta: metav1.ObjectMeta{Name: names.Book(req.Title, req.ProviderID), Namespace: req.Namespace},
			Spec: catalogv1alpha1.BookSpec{
				WorkID: req.ProviderID, Monitored: new(req.Monitored),
				QualityProfileRef: ptr.To(req.QualityProfileRef), RootFolderRef: ptr.To(req.RootFolderRef),
			},
		}, nil
	}
	return nil, fmt.Errorf("%w: %q is not a kind Add New adds", ErrInvalid, req.Kind)
}

// folderPin is AddRequest.Folder as the spec.folder it pins: nil for an
// empty or blank one, else its value with the surrounding whitespace and
// any trailing "/" removed, every component kept as written (the pin names
// a folder on disk, which pkg/naming/catalogctx joins under the root
// without sanitizing). An absolute path, or a component that is blank or
// walks the tree ("." or ".."), is ErrInvalid, as catalogctx's join would
// refuse it: a pin outside the root names no folder of the item's.
func folderPin(folder string) (*string, error) {
	pin := strings.TrimRight(strings.TrimSpace(folder), "/")
	if pin == "" {
		return nil, nil
	}
	if strings.HasPrefix(pin, "/") {
		return nil, fmt.Errorf("%w: folder %q is absolute; it is relative to the root folder", ErrInvalid, folder)
	}
	for comp := range strings.SplitSeq(pin, "/") {
		if strings.TrimSpace(comp) == "" || comp == "." || comp == ".." {
			return nil, fmt.Errorf("%w: folder %q has a blank, \".\" or \"..\" component", ErrInvalid, folder)
		}
	}
	return &pin, nil
}

// openLibraryWorkID is BookSpec.workID's pattern, checked here so a hit
// that cannot key a Book is ErrInvalid before any request.
var openLibraryWorkID = regexp.MustCompile(`^OL[0-9]+W$`)

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
