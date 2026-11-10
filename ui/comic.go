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
	"strings"
	"time"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// comicVolumeNumber is the one season a Comic has, its volume: the
// spec.season the volume Searches name and the season of status.volumeSearch
// the badge reads (actions.VolumeNumber, which restates
// catalogv1alpha1.VolumeSearchSeason; the issue page reads it too).
const comicVolumeNumber = actions.VolumeNumber

// A comic's page (2026-11-10 design §9.4; plan W4b.3) is the series page
// for a volume: the hero on the cover with the publisher, years, volume
// number, issue count, publication status, kind, age rating, genres and
// logos, the action bar with "Search Volume", and Mylar's issues table,
// read at request time through Options.Reader as the series page's seasons
// are. Nothing here writes but the volume search, which goes through
// ui/actions as every action does.

// handleComicPage serves GET /library/{namespace}/comic/{name}: the comic's
// page, which beats the shared item route by its literal segment. A comic
// the projection does not list is not found, unless Add New just created
// it, when the page says so and reloads.
func (s *Server) handleComicPage(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	item, ok := findLibraryItem(s.opts.Library(r.Context()), ns, string(commonv1.MediaKindComic), name)
	if !ok {
		s.notFoundUnlessJustAdded(w, r, ns, string(commonv1.MediaKindComic), name)
		return
	}
	v, err := s.comicPage(r.Context(), item)
	if err != nil {
		logging.FromContext(r.Context()).Error("list issues for comic page", "comic", item.Ref.String(), "error", err)
		http.Error(w, "could not list issues", http.StatusInternalServerError)
		return
	}
	preloadable(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ComicPage(v).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render comic page", "error", err)
	}
}

// comicPage builds a comic's page: the shared detail (the folder, the
// neighbours on the Comics tab, the Edit dialog's profiles), the Comic's
// metadata and rollup, its issues in number order and its volume search's
// badge. With no reader (no cluster) it renders the projection's item
// alone.
func (s *Server) comicPage(ctx context.Context, item projection.LibraryItem) (views.ComicView, error) {
	v := views.ComicView{Detail: s.itemDetail(ctx, item)}
	var co catalogv1.Comic
	if !s.getObject(ctx, item.Ref, &co) {
		return v, nil
	}
	fillComic(&v, &co)
	issues, err := s.issueRows(ctx, item.Ref.Namespace, item.Ref.Name)
	if err != nil {
		return v, err
	}
	v.Issues = issues
	v.Volume = comicVolumeBadge(co.Status.VolumeSearch, time.Now())
	return v, nil
}

// fillComic reads the Comic into the view: the detail's path, overview,
// genres, language, background, alternative titles and provider links, and
// the comic's own facts.
func fillComic(v *views.ComicView, co *catalogv1.Comic) {
	v.Path = co.Status.Path
	v.Kind = string(co.Spec.Kind)
	v.IssueFiles = co.Status.IssueFileCount
	if t := co.Status.NextPullDate; t != nil {
		v.NextPull = t.UTC().Format(time.DateOnly)
	}
	v.Backdrop = backdropArt(commonv1.MediaKindComic, co.GetUID(), co.Status.Artwork)
	v.Links = comicLinks(co)
	md := co.Status.Metadata
	if md == nil {
		return
	}
	v.Overview, v.Genres, v.Language, v.AltTitles = md.Overview, md.Genres, languageName(md.OriginalLanguage), md.AlternateTitles
	v.Publisher, v.StartYear, v.EndYear = md.Publisher, md.Year, md.EndYear
	v.VolumeNumber, v.IssueCount = md.VolumeNumber, md.IssueCount
	v.Manga, v.RunStatus, v.AgeRating = string(md.Manga), string(md.Status), md.AgeRating
}

// comicLinks are the comic's provider pages, each with its logo (design
// §9.4: ComicVine, Metron, MangaDex, AniList, MAL "as the externalIDs/links
// allow"): its source's page -- the ComicVine volume or the MangaDex title
// -- then Metron's series page, which its site addresses by a slug the ids
// do not carry, so it is the metadata's links entry of type "metron" (the
// metron client records it for a Metron-sourced comic, and the gateway's
// page ask for a ComicVine-sourced one by the id its crosswalk added; a
// Metron id alone makes no link here), then the ids the metadata's
// externalIDs carry for the other providers.
func comicLinks(co *catalogv1.Comic) []views.Link {
	ids := map[string]string{}
	var metron string
	if md := co.Status.Metadata; md != nil {
		for k, v := range md.ExternalIDs {
			ids[k] = v
		}
		for _, l := range md.Links {
			if l.Type == views.ProviderMetron && l.URL != "" {
				metron = l.URL
			}
		}
	}
	if co.Spec.SourceID != "" {
		if _, ok := ids[string(co.Spec.Source)]; !ok {
			ids[string(co.Spec.Source)] = co.Spec.SourceID
		}
	}
	var links []views.Link
	if id := ids["comicvine"]; id != "" {
		links = append(links, views.Link{Label: "ComicVine", Href: "https://comicvine.gamespot.com/volume/" + comicVineGUID("4050", id) + "/", Provider: views.ProviderComicVine})
	}
	if metron != "" {
		links = append(links, views.Link{Label: "Metron", Href: metron, Provider: views.ProviderMetron})
	}
	if id := ids["mangadex"]; id != "" {
		links = append(links, views.Link{Label: "MangaDex", Href: "https://mangadex.org/title/" + id, Provider: views.ProviderMangaDex})
	}
	if id := ids["anilist"]; id != "" {
		links = append(links, views.Link{Label: "AniList", Href: "https://anilist.co/manga/" + id, Provider: views.ProviderAniList})
	}
	if id := ids["mal"]; id != "" {
		links = append(links, views.Link{Label: "MyAnimeList", Href: "https://myanimelist.net/manga/" + id, Provider: views.ProviderMAL})
	}
	return links
}

// comicVineGUID is a ComicVine resource id in the site's guid form,
// "<resource type>-<number>" (4050 a volume, 4000 an issue): a bare number
// takes the prefix, a guid is kept as it is.
func comicVineGUID(resourceType, id string) string {
	if strings.Contains(id, "-") {
		return id
	}
	return resourceType + "-" + id
}

// handleComicVolumeSearch serves POST
// /library/{namespace}/comic/{name}/volume/search, the comic page's two
// volume searches (2026-11-10 design §9.4, D10): "Search Volume" --
// actions.SearchVolume, a Search naming the Comic with spec.season its
// volume and grabBest, which the Comic key's volume planner serves as one
// pack search of every missing issue (mirrored in status.volumeSearch, the
// badge) -- answering in place for htmx and redirecting a form post; and,
// with the form's "interactive" field, the interactive volume search --
// actions.InteractiveVolumeSearch, the same Search without grabBest --
// whose ranked results open in the volume's modal (#issue-modal, the comic
// page's) and are polled through the shared /searches routes, every result
// listed (a volume release is no season pack). The one route carries both,
// since the comic's routes are the plan's (routes_comics.go).
func (s *Server) handleComicVolumeSearch(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	item, ok := findLibraryItem(s.opts.Library(r.Context()), ns, string(commonv1.MediaKindComic), name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.FormValue("interactive") != "true" {
		_, err := s.opts.Actions.SearchVolume(r.Context(), ns, name)
		s.finishActionWith(w, r, err, "Searching the volume")
		return
	}
	search, aerr := s.opts.Actions.InteractiveVolumeSearch(r.Context(), ns, name)
	if !isHTMX(r) {
		if aerr != nil {
			s.finishAction(w, r, aerr)
			return
		}
		http.Redirect(w, r, searchPanelURL(search)+"?filter=all", http.StatusSeeOther)
		return
	}
	var p views.SearchPanel
	if aerr != nil {
		p = views.SearchPanel{Phase: string(catalogv1.SearchPhaseFailed), Failed: "the search was not started", Error: failureOf(r, aerr), Season: true}
	} else {
		p = searchPanel(search, time.Now())
	}
	p = withFilter(p, "all")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if rerr := views.VolumeSearchModal(s.itemDetail(r.Context(), item), p).Render(r.Context(), w); rerr != nil {
		logging.FromContext(r.Context()).Error("render volume search modal", "error", rerr)
	}
}

// comicVolumeBadge is the issues card's volume search badge, read from
// status.volumeSearch as the series page reads a season's from
// status.seasonSearch: Queued, Searching, or "Searched: N approved". A state
// serving a person's planned volume Search (the state names it) says so in
// the tooltip; the automatic volume search names none.
func comicVolumeBadge(st *catalogv1.SeasonSearchState, now time.Time) views.SeasonSearchBadge {
	if st == nil {
		return views.SeasonSearchBadge{}
	}
	in := ""
	if st.Request.UID != "" {
		in = fmt.Sprintf(" (the search %s)", st.Request.Name)
	}
	for _, se := range st.Seasons {
		if se.Number != comicVolumeNumber {
			continue
		}
		switch se.Phase {
		case catalogv1.SeasonSearchQueued:
			return views.SeasonSearchBadge{Phase: string(se.Phase), Label: "Queued", Title: "The volume waits its turn to be searched" + in}
		case catalogv1.SeasonSearchSearching:
			return views.SeasonSearchBadge{Phase: string(se.Phase), Label: "Searching", Title: "The volume is being searched for a pack" + in}
		case catalogv1.SeasonSearchAnswered:
			b := views.SeasonSearchBadge{Phase: string(se.Phase), Label: fmt.Sprintf("Searched: %d approved", se.Approved)}
			if se.AnsweredAt != nil {
				b.Title = fmt.Sprintf("%d releases, %d approved, %s ago%s", se.Candidates, se.Approved, ageLabel(now.Sub(se.AnsweredAt.Time)), in)
			}
			return b
		}
	}
	return views.SeasonSearchBadge{}
}
