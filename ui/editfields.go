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

package ui

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/views"
)

// The Edit dialog's kind block (2026-11-10 design §9.2-§9.5): the
// non-video kinds' own fields are fetched as the dialog opens
// (views.editFieldsPlaceholder, GET .../edit/fields) and saved with the
// dialog's form (handleEditItem), so every page's dialog has them whatever
// its Detail carries. Reads go through Options.Reader; nothing here writes.

// handleEditFields serves GET /library/{namespace}/{kind}/{name}/edit/fields:
// the Edit dialog's kind block for the item, its current values and the
// choices its selects offer (views.EditKindFields). A kind without a block,
// or an item the reader does not hold, is not found.
func (s *Server) handleEditFields(w http.ResponseWriter, r *http.Request) {
	f, ok := s.editFields(r.Context(), r.PathValue("namespace"), commonv1.MediaKind(r.PathValue("kind")), r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.EditKindFields(f).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render edit fields", "error", err)
	}
}

// The CRD defaults the dialog shows for a value the object does not carry
// (an object created from Go sends an empty struct, which the apiserver
// defaults leaf by leaf; a fake in a test does not).
var (
	defaultPrimaryTypes    = []string{"album", "ep"}
	defaultSecondaryTypes  = []string{"studio"}
	defaultReleaseStatuses = []string{"official"}
)

// editFields gathers the kind block for ns/kind/name: an artist's
// monitor-new mode and metadata profile, an author's, a comic's kind,
// special version and new-issues flag, an audiobook's region and links
// with the Authors of its namespace and the linked author's Books to
// choose from, a book's profile and root folder when it is standalone, an
// album's profile override over the music profiles (its artist's named as
// the choice that clears it) and its release choice over the release
// group's releases (design §9.2). false for a kind without a block or an
// item the reader does not hold.
func (s *Server) editFields(ctx context.Context, ns string, kind commonv1.MediaKind, name string) (views.EditFields, bool) {
	f := views.EditFields{Kind: kind, Item: name}
	if s.opts.Reader == nil {
		return f, false
	}
	ref := types.NamespacedName{Namespace: ns, Name: name}
	switch kind {
	case commonv1.MediaKindArtist:
		var a catalogv1.Artist
		if !s.getObject(ctx, ref, &a) {
			return f, false
		}
		f.MonitorNewItems = string(a.Spec.MonitorNewItems)
		mp := a.Spec.MetadataProfile
		f.PrimaryTypes = orDefault(mp.PrimaryTypes, defaultPrimaryTypes)
		f.SecondaryTypes = orDefault(mp.SecondaryTypes, defaultSecondaryTypes)
		f.ReleaseStatuses = orDefault(mp.ReleaseStatuses, defaultReleaseStatuses)
	case commonv1.MediaKindAuthor:
		var a catalogv1.Author
		if !s.getObject(ctx, ref, &a) {
			return f, false
		}
		f.MonitorNewItems = string(a.Spec.MonitorNewItems)
		mp := a.Spec.MetadataProfile
		f.AllowedLanguages = mp.AllowedLanguages
		f.SkipMissingDate, f.SkipMissingISBN = mp.SkipMissingDate, mp.SkipMissingISBN
		f.SkipPartsAndSets, f.SkipSeriesSecondary = mp.SkipPartsAndSets, mp.SkipSeriesSecondary
		f.MinPages = mp.MinPages
	case commonv1.MediaKindComic:
		var c catalogv1.Comic
		if !s.getObject(ctx, ref, &c) {
			return f, false
		}
		f.ComicKind, f.SpecialVersion = string(c.Spec.Kind), string(c.Spec.SpecialVersion)
		f.MonitorNewIssues = c.Spec.MonitorNewIssues == nil || *c.Spec.MonitorNewIssues
	case commonv1.MediaKindAudiobook:
		var a catalogv1.Audiobook
		if !s.getObject(ctx, ref, &a) {
			return f, false
		}
		f.Region = string(a.Spec.Region)
		if a.Spec.AuthorRef != nil {
			f.AuthorRef = *a.Spec.AuthorRef
		}
		if a.Spec.BookRef != nil {
			f.BookRef = *a.Spec.BookRef
		}
		f.Authors = s.authorOptions(ctx, ns)
		f.Books = s.bookOptions(ctx, ns, f.AuthorRef, f.BookRef)
	case commonv1.MediaKindAlbum:
		var a catalogv1.Album
		if !s.getObject(ctx, ref, &a) {
			return f, false
		}
		if a.Spec.QualityProfileRef != nil {
			f.QualityProfileRef = *a.Spec.QualityProfileRef
		}
		f.AnyReleaseOk = a.Spec.AnyReleaseOk == nil || *a.Spec.AnyReleaseOk
		if a.Spec.ReleaseID != nil {
			f.ReleaseID = *a.Spec.ReleaseID
		}
		var artist catalogv1.Artist
		if s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: a.Spec.ArtistRef}, &artist) {
			f.ParentProfile = artist.Spec.QualityProfileRef
		}
		for _, qp := range s.listQualityProfiles(ctx) {
			if qp.Spec.MediaKind == catalogv1.ProfileMediaKindMusic {
				f.Profiles = append(f.Profiles, views.AddOption{Value: qp.Name, Label: qp.Name})
			}
		}
		if md := a.Status.Metadata; md != nil {
			for _, r := range md.Releases {
				f.Releases = append(f.Releases, views.AddOption{Value: r.ID, Label: releaseLabel(r, r.ID == md.SelectedReleaseID)})
			}
		}
	case commonv1.MediaKindBook:
		var b catalogv1.Book
		if !s.getObject(ctx, ref, &b) {
			return f, false
		}
		f.Standalone = b.Spec.AuthorRef == nil || *b.Spec.AuthorRef == ""
		if !f.Standalone {
			return f, true
		}
		if b.Spec.QualityProfileRef != nil {
			f.QualityProfileRef = *b.Spec.QualityProfileRef
		}
		if b.Spec.RootFolderRef != nil {
			f.RootFolderRef = *b.Spec.RootFolderRef
		}
		for _, qp := range s.listQualityProfiles(ctx) {
			if qp.Spec.MediaKind == catalogv1.ProfileMediaKindBook {
				f.Profiles = append(f.Profiles, views.AddOption{Value: qp.Name, Label: qp.Name})
			}
		}
		// A rootFolderRef names a RootFolder of the book's own namespace.
		for _, rf := range s.listRootFolders(ctx) {
			if rf.Namespace == ns && rf.Spec.Kind == catalogv1.RootFolderKindBook {
				f.RootFolders = append(f.RootFolders, views.AddOption{Value: rf.Name, Label: rf.Name + " (" + rf.Spec.Path + ")"})
			}
		}
	default:
		return f, false
	}
	return f, true
}

// releaseLabel names one release of an album's group for the Release
// select: its own title and disambiguation when it has them, the country,
// the date, the track count, the label and catalogue number, a status other
// than Official, each left out when unknown, and "(selected)" on the one
// the tracks were taken from.
func releaseLabel(r catalogv1.ReleaseSummary, selected bool) string {
	var parts []string
	add := func(p string) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	add(r.Title)
	add(r.Disambiguation)
	add(r.Country)
	if r.ReleaseDate != nil {
		add(r.ReleaseDate.UTC().Format(time.DateOnly))
	}
	if r.TrackCount > 0 {
		add(fmt.Sprintf("%d tracks", r.TrackCount))
	}
	add(r.Label)
	add(r.CatalogNumber)
	if r.Status != "" && !strings.EqualFold(r.Status, "official") {
		add(r.Status)
	}
	if len(parts) == 0 {
		add(r.ID)
	}
	label := strings.Join(parts, " · ")
	if selected {
		label += " (selected)"
	}
	return label
}

// orDefault is values, or def when there are none.
func orDefault(values, def []string) []string {
	if len(values) == 0 {
		return def
	}
	return values
}

// authorOptions lists the Authors of ns for an audiobook's author link,
// by their names: the metadata's when gathered, else the object's.
func (s *Server) authorOptions(ctx context.Context, ns string) []views.AddOption {
	var list catalogv1.AuthorList
	if err := s.opts.Reader.List(ctx, &list, client.InNamespace(ns)); err != nil {
		logging.FromContext(ctx).Error("list authors for the edit dialog", "namespace", ns, "error", err)
		return nil
	}
	opts := make([]views.AddOption, 0, len(list.Items))
	for i := range list.Items {
		a := &list.Items[i]
		label := a.Name
		if md := a.Status.Metadata; md != nil && md.Name != "" {
			label = md.Name
		}
		opts = append(opts, views.AddOption{Value: a.Name, Label: label})
	}
	sortOptions(opts)
	return opts
}

// bookOptions lists the Books an audiobook's book link may name: the
// linked author's, through the reader's author index, and the one it
// names now when that is another (a standalone book).
func (s *Server) bookOptions(ctx context.Context, ns, author, current string) []views.AddOption {
	var opts []views.AddOption
	if author != "" {
		var list catalogv1.BookList
		if err := s.opts.Reader.List(ctx, &list, client.InNamespace(ns), client.MatchingFields{BookAuthorRefField: author}); err != nil {
			logging.FromContext(ctx).Error("list an author's books for the edit dialog", "namespace", ns, "author", author, "error", err)
		}
		for i := range list.Items {
			opts = append(opts, bookOption(&list.Items[i]))
		}
	}
	if current != "" && !slices.ContainsFunc(opts, func(o views.AddOption) bool { return o.Value == current }) {
		var b catalogv1.Book
		if s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: current}, &b) {
			opts = append(opts, bookOption(&b))
		} else {
			opts = append(opts, views.AddOption{Value: current, Label: current})
		}
	}
	sortOptions(opts)
	return opts
}

func bookOption(b *catalogv1.Book) views.AddOption {
	label := b.Name
	if md := b.Status.Metadata; md != nil && md.Title != "" {
		label = md.Title
	}
	return views.AddOption{Value: b.Name, Label: label}
}

func sortOptions(opts []views.AddOption) {
	sort.SliceStable(opts, func(i, j int) bool {
		if opts[i].Label != opts[j].Label {
			return strings.ToLower(opts[i].Label) < strings.ToLower(opts[j].Label)
		}
		return opts[i].Value < opts[j].Value
	})
}

// editRequest reads the Edit dialog's form for kind: the profile (an
// album's "Artist's" choice, views.EditFieldsNone, clears its override), the
// monitored switch (its "true" after the form's "false"), a series' type
// and a movie's availability as before, and the kind block's fields only
// when the block posted its "fields" marker (views.EditKindFields) -- an
// unloaded block, an unticked checkbox and an emptied list all post
// nothing, and only the marker tells them apart, so without it every kind
// field is left as it is. A number the form cannot mean is ErrInvalid.
func editRequest(r *http.Request, kind commonv1.MediaKind) (actions.EditRequest, error) {
	monitored := slices.Contains(r.Form["monitored"], "true")
	req := actions.EditRequest{
		QualityProfileRef:   r.FormValue("qualityProfile"),
		Monitored:           &monitored,
		SeriesType:          r.FormValue("seriesType"),
		MinimumAvailability: r.FormValue("minimumAvailability"),
	}
	if kind == commonv1.MediaKindAlbum && req.QualityProfileRef == views.EditFieldsNone {
		req.QualityProfileRef, req.InheritProfile = "", true
	}
	if r.FormValue("fields") != string(kind) {
		return req, nil
	}
	switch kind {
	case commonv1.MediaKindArtist:
		req.MonitorNewItems = r.FormValue("monitorNewItems")
		req.MusicProfile = &actions.MusicProfile{
			PrimaryTypes: r.Form["primaryTypes"], SecondaryTypes: r.Form["secondaryTypes"], ReleaseStatuses: r.Form["releaseStatuses"],
		}
	case commonv1.MediaKindAuthor:
		req.MonitorNewItems = r.FormValue("monitorNewItems")
		pages := int64(0)
		if raw := strings.TrimSpace(r.FormValue("minPages")); raw != "" {
			var err error
			if pages, err = strconv.ParseInt(raw, 10, 32); err != nil {
				return req, errInvalidf("minimum pages %q is not a number", raw)
			}
		}
		req.BookProfile = &actions.BookProfile{
			AllowedLanguages:    strings.Split(r.FormValue("allowedLanguages"), ","),
			SkipMissingDate:     formTrue(r, "skipMissingDate"),
			SkipMissingISBN:     formTrue(r, "skipMissingISBN"),
			SkipPartsAndSets:    formTrue(r, "skipPartsAndSets"),
			SkipSeriesSecondary: formTrue(r, "skipSeriesSecondary"),
			MinPages:            int32(pages), //nolint:gosec // parsed with a 32-bit bound
		}
	case commonv1.MediaKindComic:
		req.ComicKind, req.SpecialVersion = r.FormValue("comicKind"), r.FormValue("specialVersion")
		monitorNew := formTrue(r, "monitorNewIssues")
		req.MonitorNewIssues = &monitorNew
	case commonv1.MediaKindAudiobook:
		req.Region = r.FormValue("region")
		req.AuthorRef, req.BookRef = linkValue(r.FormValue("authorRef")), linkValue(r.FormValue("bookRef"))
	case commonv1.MediaKindBook:
		req.RootFolderRef = r.FormValue("rootFolder")
	case commonv1.MediaKindAlbum:
		anyRelease := formTrue(r, "anyReleaseOk")
		req.AnyReleaseOk = &anyRelease
		req.ReleaseID = linkValue(r.FormValue("releaseID"))
	}
	return req, nil
}

// errInvalidf is an actions.ErrInvalid with the form's own words.
func errInvalidf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{actions.ErrInvalid}, args...)...)
}

// formTrue is a switch's state: the form posts "false" and, when on, a
// "true" after it.
func formTrue(r *http.Request, name string) bool { return slices.Contains(r.Form[name], "true") }

// linkValue is a link select's value: "None" clears the link (an empty
// string, which the action sends as null), a name sets it.
func linkValue(v string) *string {
	if v == views.EditFieldsNone {
		v = ""
	}
	return &v
}
