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
	"sort"
	"strconv"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	indexv1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	subtitlev1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/views"
)

// This file is Task G3-4's Settings page (amendment §A3.4): "Root folders,
// quality profiles, indexers, download clients, providers and profiles,
// each rendered from the corresponding resources with an edit form that
// patches spec". Unlike every live page, Settings has "None" for live
// updates, so it reads directly through Options.Reader on every GET --
// listRootFolders (ui/routes.go) and listDownloads' own DownloadClient half
// already do this for the Library and Downloads pages' own config
// sections -- rather than riding ui/projection's shared tick. Every write
// goes through Options.Actions' Settings-page methods (ui/actions/
// settings.go) and ui/routes.go's existing finishAction, exactly like the
// Library page's own actions.

// handleSettings renders the Settings page from the current cluster state,
// read directly through Options.Reader for every kind the page shows.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, downloadClients := s.listDownloads(ctx)
	if err := views.Settings(
		s.listRootFolders(ctx),
		s.listQualityProfiles(ctx),
		s.listIndexers(ctx),
		downloadClients,
		s.listMetadataProviders(ctx),
		s.listSubtitleProviders(ctx),
		s.listSubtitleProfiles(ctx),
		s.listTranscodeProfiles(ctx),
	).Render(ctx, w); err != nil {
		logging.FromContext(ctx).Error("render settings page", "error", err)
	}
}

// listQualityProfiles lists every QualityProfile through Options.Reader,
// sorted by name, mirroring listRootFolders' own shape.
func (s *Server) listQualityProfiles(ctx context.Context) []catalogv1.QualityProfile {
	if s.opts.Reader == nil {
		return nil
	}
	var list catalogv1.QualityProfileList
	if err := s.opts.Reader.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Error("list quality profiles", "error", err)
		return nil
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

// listIndexers lists every Indexer through Options.Reader, sorted by
// namespace then name.
func (s *Server) listIndexers(ctx context.Context) []indexv1.Indexer {
	if s.opts.Reader == nil {
		return nil
	}
	var list indexv1.IndexerList
	if err := s.opts.Reader.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Error("list indexers", "error", err)
		return nil
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool {
		return namespacedLess(items[i].Namespace, items[i].Name, items[j].Namespace, items[j].Name)
	})
	return items
}

// listMetadataProviders lists every MetadataProvider through Options.Reader,
// sorted by namespace then name.
func (s *Server) listMetadataProviders(ctx context.Context) []catalogv1.MetadataProvider {
	if s.opts.Reader == nil {
		return nil
	}
	var list catalogv1.MetadataProviderList
	if err := s.opts.Reader.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Error("list metadata providers", "error", err)
		return nil
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool {
		return namespacedLess(items[i].Namespace, items[i].Name, items[j].Namespace, items[j].Name)
	})
	return items
}

// listSubtitleProviders lists every SubtitleProvider through Options.Reader,
// sorted by namespace then name.
func (s *Server) listSubtitleProviders(ctx context.Context) []subtitlev1.SubtitleProvider {
	if s.opts.Reader == nil {
		return nil
	}
	var list subtitlev1.SubtitleProviderList
	if err := s.opts.Reader.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Error("list subtitle providers", "error", err)
		return nil
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool {
		return namespacedLess(items[i].Namespace, items[i].Name, items[j].Namespace, items[j].Name)
	})
	return items
}

// listSubtitleProfiles lists every SubtitleProfile (cluster-scoped) through
// Options.Reader, sorted by name.
func (s *Server) listSubtitleProfiles(ctx context.Context) []subtitlev1.SubtitleProfile {
	if s.opts.Reader == nil {
		return nil
	}
	var list subtitlev1.SubtitleProfileList
	if err := s.opts.Reader.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Error("list subtitle profiles", "error", err)
		return nil
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

// listTranscodeProfiles lists every TranscodeProfile (cluster-scoped)
// through Options.Reader, sorted by name.
func (s *Server) listTranscodeProfiles(ctx context.Context) []transcodev1.TranscodeProfile {
	if s.opts.Reader == nil {
		return nil
	}
	var list transcodev1.TranscodeProfileList
	if err := s.opts.Reader.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Error("list transcode profiles", "error", err)
		return nil
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

// namespacedLess orders two (namespace, name) pairs by namespace then name,
// the same stable-render rule listRootFolders and listDownloads apply to
// their own single-namespace lists, generalised here because several
// Settings-page kinds are namespaced and this file lists them all the same
// way.
func namespacedLess(ns1, name1, ns2, name2 string) bool {
	if ns1 != ns2 {
		return ns1 < ns2
	}
	return name1 < name2
}

// handleSetRootFolderScanSchedule is the RootFolder edit form's action: POST
// /settings/rootfolders/{namespace}/{name} with a "scanSchedule" field.
func (s *Server) handleSetRootFolderScanSchedule(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	_, err := s.opts.Actions.SetRootFolderScanSchedule(r.Context(),
		r.PathValue("namespace"), r.PathValue("name"), r.FormValue("scanSchedule"))
	s.finishAction(w, r, err)
}

// handleSetQualityProfileUpgradeAllowed is the QualityProfile edit form's
// action: POST /settings/qualityprofiles/{name} with an "upgradeAllowed"
// field. QualityProfile is cluster-scoped, so the path carries no namespace.
func (s *Server) handleSetQualityProfileUpgradeAllowed(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	_, err := s.opts.Actions.SetQualityProfileUpgradeAllowed(r.Context(),
		r.PathValue("name"), r.FormValue("upgradeAllowed") == "true")
	s.finishAction(w, r, err)
}

// handleSetIndexerSettings is the Indexer edit form's action: POST
// /settings/indexers/{namespace}/{name} with "enabled" and "priority"
// fields.
func (s *Server) handleSetIndexerSettings(w http.ResponseWriter, r *http.Request) {
	enabled, priority, err := parseEnabledPriorityForm(r)
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	_, err = s.opts.Actions.SetIndexerSettings(r.Context(),
		r.PathValue("namespace"), r.PathValue("name"), enabled, priority)
	s.finishAction(w, r, err)
}

// handleSetDownloadClientSettings is the DownloadClient edit form's action:
// POST /settings/downloadclients/{namespace}/{name} with "enabled" and
// "priority" fields.
func (s *Server) handleSetDownloadClientSettings(w http.ResponseWriter, r *http.Request) {
	enabled, priority, err := parseEnabledPriorityForm(r)
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	_, err = s.opts.Actions.SetDownloadClientSettings(r.Context(),
		r.PathValue("namespace"), r.PathValue("name"), enabled, priority)
	s.finishAction(w, r, err)
}

// handleSetMetadataProviderSettings is the MetadataProvider edit form's
// action: POST /settings/metadataproviders/{namespace}/{name} with
// "enabled" and "priority" fields.
func (s *Server) handleSetMetadataProviderSettings(w http.ResponseWriter, r *http.Request) {
	enabled, priority, err := parseEnabledPriorityForm(r)
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	_, err = s.opts.Actions.SetMetadataProviderSettings(r.Context(),
		r.PathValue("namespace"), r.PathValue("name"), enabled, priority)
	s.finishAction(w, r, err)
}

// handleSetSubtitleProviderSettings is the SubtitleProvider edit form's
// action: POST /settings/subtitleproviders/{namespace}/{name} with
// "enabled" and "priority" fields.
func (s *Server) handleSetSubtitleProviderSettings(w http.ResponseWriter, r *http.Request) {
	enabled, priority, err := parseEnabledPriorityForm(r)
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	_, err = s.opts.Actions.SetSubtitleProviderSettings(r.Context(),
		r.PathValue("namespace"), r.PathValue("name"), enabled, priority)
	s.finishAction(w, r, err)
}

// handleSetSubtitleProfileDefault is the SubtitleProfile edit form's action:
// POST /settings/subtitleprofiles/{name} with a "default" field.
// SubtitleProfile is cluster-scoped, so the path carries no namespace.
func (s *Server) handleSetSubtitleProfileDefault(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	_, err := s.opts.Actions.SetSubtitleProfileDefault(r.Context(),
		r.PathValue("name"), r.FormValue("default") == "true")
	s.finishAction(w, r, err)
}

// handleSetTranscodeProfilePriority is the TranscodeProfile edit form's
// action: POST /settings/transcodeprofiles/{name} with a "priority" field.
// TranscodeProfile is cluster-scoped, so the path carries no namespace.
func (s *Server) handleSetTranscodeProfilePriority(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	priority, err := strconv.ParseInt(r.FormValue("priority"), 10, 32)
	if err != nil {
		http.Error(w, "invalid priority", http.StatusBadRequest)
		return
	}
	_, err = s.opts.Actions.SetTranscodeProfilePriority(r.Context(), r.PathValue("name"), int32(priority))
	s.finishAction(w, r, err)
}

// parseEnabledPriorityForm parses the shared "enabled"/"priority" form body
// (ui/views/settings.templ's enabledPriorityForm) that Indexer,
// DownloadClient, MetadataProvider and SubtitleProvider's edit forms all
// submit, so the four handlers above share one parse instead of repeating
// it.
func parseEnabledPriorityForm(r *http.Request) (enabled bool, priority int32, err error) {
	if err := r.ParseForm(); err != nil {
		return false, 0, err
	}
	p, err := strconv.ParseInt(r.FormValue("priority"), 10, 32)
	if err != nil {
		return false, 0, err
	}
	return r.FormValue("enabled") == "true", int32(p), nil
}

// handleImportLists renders the Import Lists page (amendment §A3.4, Task
// G3-4) from the current import-list projection returned by
// Options.ImportLists. Read-only: no action handler here reaches
// Options.Actions.
func (s *Server) handleImportLists(w http.ResponseWriter, r *http.Request) {
	entries := s.opts.ImportLists(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ImportLists(entries).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render import lists page", "error", err)
	}
}
