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
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/pkg/iptv/filter"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// The Channels page (the direct-tuners and ui design §5.4): the lineup's
// channels, edited in place and in bulk, with the provider's playlist
// beside them. Every write is ui/actions'.

// playlistPageSize is the playlist pane's page.
const playlistPageSize = 50

// epgChoicesMax bounds the EPG picker's guide channels.
const epgChoicesMax = 20

// parseChannelFilter reads the Channels table's filters from q.
func parseChannelFilter(q url.Values) views.ChannelFilter {
	f := views.ChannelFilter{
		Q: strings.TrimSpace(q.Get("q")), Provider: q.Get("provider"), Group: q.Get("group"),
		NoGuide: q.Get("noguide") == "1", NoLogo: q.Get("nologo") == "1",
	}
	switch s := q.Get("state"); s {
	case "active", "inactive":
		f.State = s
	}
	return f
}

// filterChannels is rows narrowed by f: q over the name and the number's
// prefix; "no guide" a channel on no guide or a dummy; "no logo" one with
// none.
func filterChannels(rows []projection.ChannelRow, f views.ChannelFilter) []projection.ChannelRow {
	q := strings.ToLower(f.Q)
	var out []projection.ChannelRow
	for _, r := range rows {
		switch {
		case q != "" && !strings.Contains(strings.ToLower(r.DisplayName), q) && !strings.HasPrefix(r.Number, f.Q):
		case f.Provider != "" && r.Namespace+"/"+r.Provider != f.Provider:
		case f.Group != "" && r.Group != f.Group:
		case f.State == "active" && !r.Active, f.State == "inactive" && r.Active:
		case f.NoGuide && r.Guide != "" && !strings.HasPrefix(r.Guide, "dummy/"):
		case f.NoLogo && r.Logo != "":
		default:
			out = append(out, r)
		}
	}
	return out
}

// channelsPage builds the page's view from the cluster and the store.
func (s *Server) channelsPage(ctx context.Context, f views.ChannelFilter, pane url.Values) views.ChannelsPage {
	in := s.liveTVInputs(ctx)
	rows := projection.LiveTVChannels(in)
	groups := map[string]bool{}
	for _, r := range rows {
		if r.Group != "" {
			groups[r.Group] = true
		}
	}
	p := views.ChannelsPage{Rows: filterChannels(rows, f), Filter: f}
	for g := range groups {
		p.Groups = append(p.Groups, g)
	}
	slices.Sort(p.Groups)
	for _, pr := range in.Providers {
		p.Providers = append(p.Providers, views.ProviderOption{Value: pr.Namespace + "/" + pr.Name, Label: pr.Name})
		for _, b := range pr.Status.Blocks {
			if b.URI == "" {
				continue
			}
			p.Links = append(p.Links, views.BlockLinks{Provider: pr.Name, Start: b.Start, HDHR: b.URI, M3U: b.URI + "/playlist.m3u", EPG: b.URI + "/xmltv.xml"})
		}
	}
	if pane.Get("provider") == "" {
		if f.Provider != "" {
			pane.Set("provider", f.Provider)
		} else if len(in.Providers) > 0 {
			pane.Set("provider", in.Providers[0].Namespace+"/"+in.Providers[0].Name)
		}
	}
	p.Pane = s.playlistPane(ctx, in, pane)
	return p
}

// handleLiveTVChannels renders the Channels page.
func (s *Server) handleLiveTVChannels(w http.ResponseWriter, r *http.Request) {
	p := s.channelsPage(r.Context(), parseChannelFilter(r.URL.Query()), url.Values{})
	preloadable(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.LiveTVChannels(p).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render the Channels page", "error", err)
	}
}

// handleLiveTVChannelsTable renders the table alone: a filter's change,
// and every channels-changed event.
func (s *Server) handleLiveTVChannelsTable(w http.ResponseWriter, r *http.Request) {
	f := parseChannelFilter(r.URL.Query())
	in := s.liveTVInputs(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ChannelsTable(filterChannels(projection.LiveTVChannels(in), f), f).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render the channels table", "error", err)
	}
}

// answerInPlace answers an in-place write's error into #action-status: 200,
// since htmx swaps no other status, retargeted from the row the request
// aimed at.
func (s *Server) answerInPlace(w http.ResponseWriter, r *http.Request, err error) {
	code, _ := actionErrorCode(err)
	logging.FromContext(r.Context()).Error("ui action failed", "error", err, "code", code)
	w.Header().Set("HX-Retarget", "#action-status")
	w.Header().Set("HX-Reswap", "innerHTML")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if renderErr := views.ActionError(code, err.Error()).Render(r.Context(), w); renderErr != nil {
		logging.FromContext(r.Context()).Error("render action error", "error", renderErr)
	}
}

// handleEditChannel is one field of a channel edited in place: rv and one
// of active, number, name, group or epg. It answers the row.
func (s *Server) handleEditChannel(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.answerInPlace(w, r, fmt.Errorf("%w: %v", actions.ErrInvalid, err))
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	e := actions.ChannelEdit{Name: name, ResourceVersion: r.PostForm.Get("rv")}
	str := func(k string) *string {
		if !r.PostForm.Has(k) {
			return nil
		}
		v := strings.TrimSpace(r.PostForm.Get(k))
		return &v
	}
	e.Number, e.DisplayName, e.Group, e.EPG = str("number"), str("name"), str("group"), str("epg")
	if r.PostForm.Has("active") {
		switch r.PostForm.Get("active") {
		case "true", "false":
			v := r.PostForm.Get("active") == "true"
			e.Active = &v
		default:
			s.answerInPlace(w, r, fmt.Errorf("%w: active %q", actions.ErrInvalid, r.PostForm.Get("active")))
			return
		}
	}
	patched, err := s.opts.Actions.EditChannels(r.Context(), ns, []actions.ChannelEdit{e})
	if err != nil {
		s.answerInPlace(w, r, err)
		return
	}
	in := s.liveTVInputs(r.Context())
	for i := range in.Channels {
		if in.Channels[i].Namespace == ns && in.Channels[i].Name == name && len(patched) == 1 {
			in.Channels[i] = *patched[0]
		}
	}
	for _, row := range projection.LiveTVChannels(in) {
		if row.Namespace == ns && row.Name == name {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := views.ChannelRowView(row).Render(r.Context(), w); err != nil {
				logging.FromContext(r.Context()).Error("render a channel row", "error", err)
			}
			return
		}
	}
}

// handleDeleteChannel deletes a channel; the empty answer removes its row.
func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	k, _ := actions.ConfigKindBySlug("iptvchannels")
	if err := s.opts.Actions.DeleteConfig(r.Context(), k, r.PathValue("namespace"), r.PathValue("name")); err != nil {
		s.answerInPlace(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// bulkItem is one selected channel: namespace, name and the version the
// page read.
type bulkItem struct {
	ch *clustarrv1.IPTVChannel
	rv string
}

// handleChannelsBulk applies one op to every selected channel, checking
// every item against the cluster before writing any.
func (s *Server) handleChannelsBulk(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || s.opts.Reader == nil {
		s.finishActionWith(w, r, fmt.Errorf("%w: unreadable form", actions.ErrInvalid), "")
		return
	}
	var items []bulkItem
	for _, it := range r.PostForm["item"] {
		ref, rv, _ := strings.Cut(it, "@")
		ns, name, ok := strings.Cut(ref, "/")
		var ch clustarrv1.IPTVChannel
		if !ok || rv == "" || s.opts.Reader.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &ch) != nil {
			s.finishActionWith(w, r, fmt.Errorf("%w: no channel %q", actions.ErrInvalid, it), "")
			return
		}
		items = append(items, bulkItem{ch: &ch, rv: rv})
	}
	if len(items) == 0 {
		s.finishActionWith(w, r, fmt.Errorf("%w: no channel selected", actions.ErrInvalid), "")
		return
	}
	op := r.PostForm.Get("op")
	var start int
	if op == "renumber" {
		n, err := strconv.Atoi(strings.TrimSpace(r.PostForm.Get("start")))
		if err != nil || n < 1 || n+len(items) > 99999 {
			s.finishActionWith(w, r, fmt.Errorf("%w: renumber from %q: a whole number", actions.ErrInvalid, r.PostForm.Get("start")), "")
			return
		}
		start = n
		slices.SortStableFunc(items, func(a, b bulkItem) int { return xmltv.CompareNumbers(a.ch.Spec.Number, b.ch.Spec.Number) })
	}
	byNS := map[string][]actions.ChannelEdit{}
	var nsOrder []string
	for i, it := range items {
		e := actions.ChannelEdit{Name: it.ch.Name, ResourceVersion: it.rv}
		switch op {
		case "enable", "disable":
			v := op == "enable"
			e.Active = &v
		case "group":
			v := strings.TrimSpace(r.PostForm.Get("value"))
			e.Group = &v
		case "renumber":
			v := strconv.Itoa(start + i)
			e.Number = &v
		case "delete":
		default:
			s.finishActionWith(w, r, fmt.Errorf("%w: no bulk op %q", actions.ErrInvalid, op), "")
			return
		}
		if _, seen := byNS[it.ch.Namespace]; !seen {
			nsOrder = append(nsOrder, it.ch.Namespace)
		}
		byNS[it.ch.Namespace] = append(byNS[it.ch.Namespace], e)
	}
	var err error
	if op == "delete" {
		k, _ := actions.ConfigKindBySlug("iptvchannels")
		for _, it := range items {
			if err = s.opts.Actions.DeleteConfig(r.Context(), k, it.ch.Namespace, it.ch.Name); err != nil {
				break
			}
		}
	} else {
		for _, ns := range nsOrder {
			if _, err = s.opts.Actions.EditChannels(r.Context(), ns, byNS[ns]); err != nil {
				break
			}
		}
	}
	if err == nil {
		w.Header().Set("HX-Trigger", "channels-changed")
	}
	s.finishActionWith(w, r, err, fmt.Sprintf("%d channels updated", len(items)))
}

// providerOf is the provider a "namespace/name" value names; false for none.
func providerOf(in projection.LiveTVInputs, value string) (*clustarrv1.IPTVProvider, bool) {
	for i := range in.Providers {
		p := &in.Providers[i]
		if p.Namespace+"/"+p.Name == value {
			return p, true
		}
	}
	return nil, false
}

// filterRules is a provider's filters as the tuner applies them
// (pkg/iptv/filter), so the pane marks the entries a channel can come from.
func filterRules(p *clustarrv1.IPTVProvider) []filter.Rule {
	var rules []filter.Rule
	for _, f := range p.Spec.Filters {
		rules = append(rules, filter.Rule{
			Name: f.Name, Type: string(f.Type), Match: f.Match, Include: f.Include,
			Exclude: f.Exclude, CaseSensitive: f.CaseSensitive, Enabled: f.EnabledOrDefault(),
		})
	}
	return rules
}

// paneEntryID is a playlist entry's row id.
func paneEntryID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "pl-" + hex.EncodeToString(sum[:])[:12]
}

// playlistPane is the provider's playlist, filtered and paged: q over name,
// tvg-id and group; candidates of the provider's filters only unless all.
func (s *Server) playlistPane(ctx context.Context, in projection.LiveTVInputs, q url.Values) views.PlaylistPane {
	pane := views.PlaylistPane{Provider: q.Get("provider"), Q: strings.TrimSpace(q.Get("q")), Group: q.Get("group"), All: q.Get("all") == "1"}
	p, ok := providerOf(in, pane.Provider)
	if !ok {
		return pane
	}
	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
	for _, f := range p.Spec.Filters {
		pane.Filters = append(pane.Filters, cmp.Or(f.Name, f.Match))
	}
	pl, ok := s.liveTV.Playlist(ctx, key)
	if !ok {
		return pane
	}
	pane.Loaded, pane.Groups = true, pl.Groups
	numbers := map[string]string{}
	for _, ch := range in.Channels {
		if ch.Namespace == p.Namespace && ch.Spec.ProviderRef == p.Name {
			numbers[ch.Spec.Key] = ch.Spec.Number
		}
	}
	rules := filterRules(p)
	needle := strings.ToLower(pane.Q)
	var matched []views.PlaylistPaneEntry
	for _, e := range pl.Entries {
		if pane.Group != "" && e.Group != pane.Group {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(e.Name), needle) && !strings.Contains(strings.ToLower(e.TvgID), needle) &&
			!strings.Contains(strings.ToLower(e.Group), needle) {
			continue
		}
		candidate := slices.ContainsFunc(rules, func(r filter.Rule) bool {
			return filter.Matches(r, m3u.Entry{Key: e.Key, Name: e.Name, Group: e.Group, TvgID: e.TvgID, TvgName: e.TvgName})
		})
		if !candidate && !pane.All {
			continue
		}
		pe := views.PlaylistPaneEntry{ID: paneEntryID(e.Key), Key: e.Key, Name: e.Name, Group: e.Group, TvgID: e.TvgID,
			Provider: pane.Provider, Number: numbers[e.Key], Candidate: candidate}
		if e.Logo != "" {
			pe.LogoPath = projection.LogoPath(key, e.Logo)
		}
		matched = append(matched, pe)
	}
	pane.Total = len(matched)
	pane.Pages = max(1, (pane.Total+playlistPageSize-1)/playlistPageSize)
	pane.Page, _ = strconv.Atoi(q.Get("page"))
	pane.Page = min(max(pane.Page, 1), pane.Pages)
	from := (pane.Page - 1) * playlistPageSize
	pane.Entries = matched[from:min(from+playlistPageSize, len(matched))]
	return pane
}

// handlePlaylistPane renders the playlist pane alone.
func (s *Server) handlePlaylistPane(w http.ResponseWriter, r *http.Request) {
	pane := s.playlistPane(r.Context(), s.liveTVInputs(r.Context()), r.URL.Query())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.PlaylistPaneView(pane).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render the playlist pane", "error", err)
	}
}

// nextChannelNumber is one past the provider's greatest whole channel
// number, else its spec.channelNumberStart.
func nextChannelNumber(in projection.LiveTVInputs, p *clustarrv1.IPTVProvider) string {
	top := -1
	for _, ch := range in.Channels {
		if ch.Namespace != p.Namespace || ch.Spec.ProviderRef != p.Name {
			continue
		}
		major, _, _ := strings.Cut(ch.Spec.Number, ".")
		if n, err := strconv.Atoi(major); err == nil && n > top {
			top = n
		}
	}
	if top < 0 {
		return strconv.Itoa(int(p.Spec.ChannelNumberStartOrDefault()))
	}
	return strconv.Itoa(top + 1)
}

// handleAddChannel adds a playlist entry as a channel at the next free
// number, answering the pane's row naming it.
func (s *Server) handleAddChannel(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.answerInPlace(w, r, fmt.Errorf("%w: %v", actions.ErrInvalid, err))
		return
	}
	in := s.liveTVInputs(r.Context())
	p, ok := providerOf(in, r.PostForm.Get("provider"))
	if !ok {
		s.answerInPlace(w, r, fmt.Errorf("%w: no provider %q", actions.ErrInvalid, r.PostForm.Get("provider")))
		return
	}
	key, name := r.PostForm.Get("key"), strings.TrimSpace(r.PostForm.Get("name"))
	entry := views.PlaylistPaneEntry{ID: paneEntryID(key), Key: key, Name: name, Provider: p.Namespace + "/" + p.Name, Candidate: true}
	ch, err := s.opts.Actions.AddChannel(r.Context(), p.Namespace, p.Name, key, nextChannelNumber(in, p), name)
	switch {
	case err == nil:
		entry.Number = ch.Spec.Number
		w.Header().Set("HX-Trigger", "channels-changed")
	case !apierrors.IsAlreadyExists(err):
		s.answerInPlace(w, r, err)
		return
	default: // added before: the row names the channel it is
		var existing clustarrv1.IPTVChannel
		if s.opts.Reader != nil && s.opts.Reader.Get(r.Context(), types.NamespacedName{Namespace: p.Namespace, Name: actions.ChannelObjectName(p.Name, key)}, &existing) == nil {
			entry.Number = existing.Spec.Number
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.PlaylistEntryRow(entry).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render a playlist entry", "error", err)
	}
}

// handleChannelEPG renders the EPG picker's dialog: automatic, dummy, then
// up to epgChoicesMax of the provider's guide channels matching q (exact,
// prefix, then substring, over id and name); q starts at the channel's
// name.
func (s *Server) handleChannelEPG(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	var ch clustarrv1.IPTVChannel
	if s.opts.Reader == nil || s.opts.Reader.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &ch) != nil {
		http.NotFound(w, r)
		return
	}
	pkey := types.NamespacedName{Namespace: ns, Name: ch.Spec.ProviderRef}
	q := r.URL.Query()
	query := strings.TrimSpace(q.Get("q"))
	if !q.Has("q") {
		query = ch.Spec.Name
		if pl, ok := s.liveTV.Playlist(r.Context(), pkey); ok && query == "" {
			if i, ok := pl.ByKey[ch.Spec.Key]; ok {
				query = pl.Entries[i].Name
			}
		}
	}
	picker := views.EPGPicker{Namespace: ns, Name: name, RV: ch.ResourceVersion, Q: query, Choices: []views.EPGChoice{
		{Value: "", Label: "Automatic", Hint: "by the playlist's tvg-id or name"},
		{Value: "dummy", Label: "Dummy guide", Hint: "hourly blocks titled by the channel"},
	}}
	if gc, ok := s.liveTV.GuideChannels(r.Context(), pkey); ok {
		picker.Choices = append(picker.Choices, rankGuideChannels(gc, query)...)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.EPGPickerDialog(picker).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render the EPG picker", "error", err)
	}
}

// rankGuideChannels is the guide channels matching q, exact before prefix
// before substring, at most epgChoicesMax.
func rankGuideChannels(gc *guideChannelsView, q string) []views.EPGChoice {
	needle := strings.ToLower(q)
	type scored struct {
		score int
		c     views.EPGChoice
	}
	var out []scored
	for _, c := range gc.Channels {
		id, label := strings.ToLower(c.ID), strings.ToLower(c.DisplayName)
		score := -1
		switch {
		case needle == "":
			score = 2
		case id == needle || label == needle:
			score = 0
		case strings.HasPrefix(id, needle) || strings.HasPrefix(label, needle):
			score = 1
		case strings.Contains(id, needle) || strings.Contains(label, needle):
			score = 2
		}
		if score >= 0 {
			out = append(out, scored{score, views.EPGChoice{Value: c.ID, Label: cmp.Or(c.DisplayName, c.ID), Hint: c.ID}})
		}
	}
	slices.SortStableFunc(out, func(a, b scored) int {
		if a.score != b.score {
			return a.score - b.score
		}
		return strings.Compare(a.c.Label, b.c.Label)
	})
	choices := make([]views.EPGChoice, 0, min(len(out), epgChoicesMax))
	for _, s := range out[:min(len(out), epgChoicesMax)] {
		choices = append(choices, s.c)
	}
	return choices
}
