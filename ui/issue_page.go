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
	"net/http"
	"path/filepath"
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/views"
)

// An issue's page (2026-11-10 design §9.4; plan W4b.3) is filled as the
// episode page is, from what the cluster already holds: the Issue, its
// Comic's library card and metadata, and its MediaFile. Nothing here
// fetches from a provider -- the cover is the provider's URL through
// /art/search, signed as episode stills are -- and nothing here writes.

// handleIssuePage serves GET /library/{namespace}/issue/{name}, the page an
// issue's title opens (and preloads).
func (s *Server) handleIssuePage(w http.ResponseWriter, r *http.Request) {
	s.renderIssuePage(w, r, true)
}

// renderIssuePage answers the issue's page, preloadable when the route is
// one a link preloads; not found when the issue, its comic's card or the
// reader is missing.
func (s *Server) renderIssuePage(w http.ResponseWriter, r *http.Request, preload bool) {
	v, ok := s.issuePage(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if preload {
		preloadable(w)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.IssuePage(v).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render issue page", "err", err)
	}
}

// issuePage builds an issue's page: its own title, number, dates, overview,
// cover, credits, status and file, its comic's poster, background, quality
// profile, publisher, language, genres and provider links, and the issues
// either side of it.
func (s *Server) issuePage(ctx context.Context, ns, name string) (views.IssueView, bool) {
	var iss catalogv1.Issue
	if !s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: name}, &iss) {
		return views.IssueView{}, false
	}
	item, ok := findLibraryItem(s.opts.Library(ctx), ns, string(commonv1.MediaKindComic), iss.Spec.ComicRef)
	if !ok {
		return views.IssueView{}, false
	}
	v := views.IssueView{
		Detail:       views.Detail{Item: item, ShowFiles: true},
		Row:          issueRow(&iss, time.Now()),
		CutoffReason: iss.Status.CutoffReason,
		Credits:      issueCredits(iss.Status.Credits),
		Cover:        s.searchArt.URL(iss.Status.CoverURL),
	}
	folder := ""
	var co catalogv1.Comic
	if s.getObject(ctx, item.Ref, &co) {
		folder = co.Status.Path
		v.Path = folder
		v.Backdrop = backdropArt(commonv1.MediaKindComic, co.GetUID(), co.Status.Artwork)
		v.ComicLinks = captionedLinks(comicLinks(&co), "comic")
		if md := co.Status.Metadata; md != nil {
			v.Publisher, v.Genres, v.Language = md.Publisher, md.Genres, languageName(md.OriginalLanguage)
		}
	}
	// The issue's own over its comic's: the overview is the issue's.
	v.Overview, v.Links = iss.Status.Overview, issueLinks(&iss, &co)
	if f := s.issueFile(ctx, &iss); f != nil {
		v.Files, v.Extras = fileRows(folder, f)
		v.Path = filepath.Dir(f.Spec.Path)
	}
	v.Prev, v.Next = s.issueNeighbours(ctx, &iss)
	return v, true
}

// issueLinks are the issue's own provider pages: its ComicVine issue page
// (status.sourceIDs' comicvine id, else the source id of a ComicVine comic)
// and its MangaDex chapter.
func issueLinks(iss *catalogv1.Issue, co *catalogv1.Comic) []views.Link {
	var links []views.Link
	cv := iss.Status.SourceIDs["comicvine"]
	if cv == "" && co.Spec.Source == catalogv1.ComicSourceComicVine {
		cv = iss.Status.SourceID
	}
	if cv != "" {
		links = append(links, views.Link{Label: "ComicVine", Href: "https://comicvine.gamespot.com/issue/" + comicVineGUID("4000", cv) + "/", Provider: views.ProviderComicVine})
	}
	md := iss.Status.SourceIDs["mangadex"]
	if md == "" && co.Spec.Source == catalogv1.ComicSourceMangaDex {
		md = iss.Status.SourceID
	}
	if md != "" {
		links = append(links, views.Link{Label: "MangaDex", Href: "https://mangadex.org/chapter/" + md, Provider: views.ProviderMangaDex})
	}
	return links
}

// captionedLinks are a parent's provider pages as a child's page names
// them, "ComicVine (comic)", so a link's title says whose page it opens,
// as the episode page's "TVDB (series)" does.
func captionedLinks(parent []views.Link, caption string) []views.Link {
	links := make([]views.Link, 0, len(parent))
	for _, l := range parent {
		links = append(links, views.Link{Label: l.Label + " (" + caption + ")", Href: l.Href, Provider: l.Provider})
	}
	return links
}

// issueNeighbours are the pages of the issues either side of iss in its
// comic, in the number order the comic's page lists them.
func (s *Server) issueNeighbours(ctx context.Context, iss *catalogv1.Issue) (prev, next string) {
	issues, err := s.listIssues(ctx, iss.Namespace, iss.Spec.ComicRef)
	if err != nil {
		logging.FromContext(ctx).Error("list issues for an issue's neighbours", "err", err)
		return "", ""
	}
	sortIssues(issues)
	url := func(i *catalogv1.Issue) string { return views.IssueRow{Namespace: i.Namespace, Name: i.Name}.URL() }
	for i := range issues {
		if issues[i].Name != iss.Name {
			continue
		}
		if i > 0 {
			prev = url(&issues[i-1])
		}
		if i+1 < len(issues) {
			next = url(&issues[i+1])
		}
		break
	}
	return prev, next
}
