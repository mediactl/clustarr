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
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	subtitlev1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	crdbases "github.com/mediactl/clustarr/config/crd/bases"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/forms"
	"github.com/mediactl/clustarr/ui/schema"
	"github.com/mediactl/clustarr/ui/views"
)

// This file is the Settings page's add, edit and delete (settings CRUD
// design, docs/superpowers/specs/2026-09-24-settings-crud-design.md). A
// form is derived from the kind's CRD (ui/schema over the embedded
// config/crd/bases) and arranged by its overlay (ui/forms); a submission is
// decoded by the same schema, completed by the overlay, its credentials
// written to Secrets, and sent as a create or a merge patch of spec
// (ui/actions). The apiserver validates; a rejection re-renders the form
// with what was typed and the message.
//
// Routes use a verb-first shape (/settings/new/{kind},
// /settings/edit/{kind}/{namespace}/{name}, /settings/delete/...), with
// "-" for a cluster-scoped kind's namespace, so they never collide with
// the cards' quick forms at /settings/{kind}/{namespace}/{name}.

// clusterScopedNamespace is the namespace segment of a cluster-scoped kind.
const clusterScopedNamespace = "-"

var configConstructors = map[string]func() client.Object{
	"rootfolders":       func() client.Object { return &catalogv1.RootFolder{} },
	"qualityprofiles":   func() client.Object { return &catalogv1.QualityProfile{} },
	"metadataproviders": func() client.Object { return &catalogv1.MetadataProvider{} },
	"importlists":       func() client.Object { return &catalogv1.ImportList{} },
	"indexers":          func() client.Object { return &indexv1.Indexer{} },
	"indexerproxies":    func() client.Object { return &indexv1.IndexerProxy{} },
	"downloadclients":   func() client.Object { return &downloadv1.DownloadClient{} },
	"subtitleproviders": func() client.Object { return &subtitlev1.SubtitleProvider{} },
	"subtitleprofiles":  func() client.Object { return &subtitlev1.SubtitleProfile{} },
	"transcodeprofiles": func() client.Object { return &transcodev1.TranscodeProfile{} },
	"iptvproviders":     func() client.Object { return &clustarrv1.IPTVProvider{} },
	"iptvchannels":      func() client.Object { return &clustarrv1.IPTVChannel{} },
}

// specSchemas caches each kind's spec field tree; the embedded CRDs never
// change while the process runs.
var specSchemas sync.Map

// formKind resolves a route's kind slug to its form definition and schema.
func formKind(slug string) (forms.Kind, *schema.Field, error) {
	k, ok := forms.Lookup(slug)
	if !ok {
		return forms.Kind{}, nil, fmt.Errorf("no settings kind %q", slug)
	}
	if root, ok := specSchemas.Load(slug); ok {
		return k, root.(*schema.Field), nil
	}
	crd, err := crdbases.Load(k.Group, k.Kind)
	if err != nil {
		return k, nil, err
	}
	spec := crdbases.SpecSchema(crd, k.Version)
	if spec == nil {
		return k, nil, fmt.Errorf("%s has no %s spec schema", k.Kind, k.Version)
	}
	root := schema.Walk(spec)
	specSchemas.Store(slug, root)
	return k, root, nil
}

// pathNamespace reads a route's namespace segment: "" for the
// cluster-scoped placeholder.
func pathNamespace(r *http.Request) string {
	ns := r.PathValue("namespace")
	if ns == clusterScopedNamespace {
		return ""
	}
	return ns
}

func formAction(k forms.Kind, namespace, name string) (action, deleteAction string) {
	ns := namespace
	if !k.Namespaced || ns == "" {
		ns = clusterScopedNamespace
	}
	return fmt.Sprintf("/settings/edit/%s/%s/%s", k.Slug, ns, name), fmt.Sprintf("/settings/delete/%s/%s/%s", k.Slug, ns, name)
}

// getConfigSpec reads one object through Options.Reader as the spec map
// the form and the merge patch work on.
func (s *Server) getConfigSpec(ctx context.Context, k forms.Kind, namespace, name string) (map[string]any, error) {
	if s.opts.Reader == nil {
		return nil, apierrors.NewNotFound(k.GroupVersionKind().GroupVersion().WithResource(k.Resource).GroupResource(), name)
	}
	obj := configConstructors[k.Slug]()
	if err := s.opts.Reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj); err != nil {
		return nil, err
	}
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	spec, _ := u["spec"].(map[string]any)
	if spec == nil {
		spec = map[string]any{}
	}
	return spec, nil
}

// choices lists the live objects the kind's reference fields choose from.
func (s *Server) choices(ctx context.Context, k forms.Kind) forms.Choices {
	if s.opts.Reader == nil || len(k.Refs) == 0 {
		return nil
	}
	out := forms.Choices{}
	for _, ref := range k.Refs {
		if _, done := out[ref]; done {
			continue
		}
		out[ref] = s.listChoices(ctx, ref)
	}
	return out
}

func (s *Server) listChoices(ctx context.Context, ref forms.Ref) []forms.Option {
	var opts []forms.Option
	names := func(items []string) {
		for _, n := range items {
			opts = append(opts, forms.Option{Value: n, Label: n})
		}
	}
	// An option's Class is what a kind's RefOptions rule offers it by: a
	// profile's media kind, a client's protocol, a root folder's kind.
	switch ref {
	case forms.RefQualityProfiles:
		for _, qp := range s.listQualityProfiles(ctx) {
			opts = append(opts, forms.Option{Value: qp.Name, Label: qp.Name, Class: string(qp.Spec.MediaKind)})
		}
	case forms.RefTranscodeProfiles:
		for _, tp := range s.listTranscodeProfiles(ctx) {
			names([]string{tp.Name})
		}
	case forms.RefSubtitleProfiles:
		for _, sp := range s.listSubtitleProfiles(ctx) {
			names([]string{sp.Name})
		}
	case forms.RefDownloadClients:
		clients := s.downloadClients(ctx)
		for _, dc := range clients {
			opts = append(opts, forms.Option{Value: dc.Name, Label: dc.Name + " (" + string(dc.Spec.Protocol) + ")", Class: string(dc.Spec.Protocol)})
		}
	case forms.RefRootFolders:
		var list catalogv1.RootFolderList
		if err := s.opts.Reader.List(ctx, &list); err == nil {
			for _, rf := range list.Items {
				opts = append(opts, forms.Option{Value: rf.Name, Label: rf.Name + " (" + rf.Spec.Path + ")", Class: string(rf.Spec.Kind)})
			}
		}
	case forms.RefDelayProfiles:
		var list catalogv1.DelayProfileList
		if err := s.opts.Reader.List(ctx, &list); err == nil {
			for _, dp := range list.Items {
				names([]string{dp.Name})
			}
		}
	case forms.RefIndexerProxies:
		var list indexv1.IndexerProxyList
		if err := s.opts.Reader.List(ctx, &list); err == nil {
			for _, p := range list.Items {
				names([]string{p.Name})
			}
		}
	case forms.RefIndexerDefinitions:
		var list indexv1.IndexerDefinitionList
		if err := s.opts.Reader.List(ctx, &list); err == nil {
			for _, d := range list.Items {
				id := d.Status.ID
				if id == "" {
					id = d.Name
				}
				label := d.Status.Name
				if label == "" {
					label = d.Name
				}
				if d.Status.Type != "" {
					label += " (" + string(d.Status.Type) + ")"
				}
				opts = append(opts, forms.Option{Value: id, Label: label})
			}
		}
	}
	sort.Slice(opts, func(i, j int) bool { return strings.ToLower(opts[i].Label) < strings.ToLower(opts[j].Label) })
	return opts
}

// formSubmission is what a posted form decoded to, kept for a re-render.
type formSubmission struct {
	namespace, name string
	spec            map[string]any
}

func (s *Server) renderForm(w http.ResponseWriter, r *http.Request, k forms.Kind, root *schema.Field, mode forms.Mode, sub formSubmission, err error) {
	form := forms.Build(k, root, sub.spec, s.choices(r.Context(), k), mode)
	s.decorateForm(r.Context(), k, form, sub.spec)
	page := views.FormPage{
		Form:      form,
		Namespace: sub.namespace,
		Name:      sub.name,
		ReturnTo:  returnPath(r),
	}
	if mode == forms.ModeNew {
		page.Action = "/settings/new/" + k.Slug
	} else {
		page.Action, page.DeleteAction = formAction(k, sub.namespace, sub.name)
	}
	status := http.StatusOK
	if err != nil {
		page.ErrorCode, status = actionErrorCode(err)
		page.Error = err.Error()
		logging.FromContext(r.Context()).Error("settings form rejected", "kind", k.Kind, "err", err, "code", page.ErrorCode)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if renderErr := views.SettingsForm(page).Render(r.Context(), w); renderErr != nil {
		logging.FromContext(r.Context()).Error("render settings form", "err", renderErr)
	}
}

// handleSettingsNew renders an empty form for the kind.
func (s *Server) handleSettingsNew(w http.ResponseWriter, r *http.Request) {
	k, root, err := formKind(r.PathValue("kind"))
	if err != nil {
		notFound(w, r)
		return
	}
	ns := ""
	if k.Namespaced {
		ns = s.opts.Namespace
	}
	s.renderForm(w, r, k, root, forms.ModeNew, formSubmission{namespace: ns}, nil)
}

// handleSettingsEdit renders the form filled from the object.
func (s *Server) handleSettingsEdit(w http.ResponseWriter, r *http.Request) {
	k, root, err := formKind(r.PathValue("kind"))
	if err != nil {
		notFound(w, r)
		return
	}
	ns, name := pathNamespace(r), r.PathValue("name")
	spec, err := s.getConfigSpec(r.Context(), k, ns, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			notFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderForm(w, r, k, root, forms.ModeEdit, formSubmission{namespace: ns, name: name, spec: spec}, nil)
}

// decodeSubmission turns the posted form into the object's coordinates
// and spec: the schema types it, the overlay completes it.
func decodeSubmission(r *http.Request, k forms.Kind, root *schema.Field, mode forms.Mode, pathNS, pathName string) (formSubmission, error) {
	if err := r.ParseForm(); err != nil {
		return formSubmission{}, fmt.Errorf("%w: %w", actions.ErrInvalid, err)
	}
	sub := formSubmission{namespace: pathNS, name: pathName}
	if mode == forms.ModeNew {
		sub.name = strings.TrimSpace(r.PostForm.Get("__name"))
		if k.Namespaced {
			sub.namespace = strings.TrimSpace(r.PostForm.Get("__namespace"))
		}
	}
	spec, err := schema.Decode(root, r.PostForm)
	if err != nil {
		sub.spec = map[string]any{}
		return sub, fmt.Errorf("%w: %w", actions.ErrInvalid, err)
	}
	if k.Choice != nil && k.Choice.Shape != nil {
		k.Choice.Shape(spec, r.PostForm.Get(k.Choice.ControlName()))
	}
	if k.Ensure != nil {
		k.Ensure(spec)
	}
	sub.spec = spec
	return sub, nil
}

// indexersSlug is the one kind whose form has a section the server fills
// from another object: an Indexer's settings are its definition's.
const indexersSlug = "indexers"

// definitionOf finds the Cardigann definition an Indexer spec names -- by
// spec.definition (a definition's id, or an id it replaces) or by
// spec.definitionRef (an IndexerDefinition's object name) -- and parses its
// YAML. nil when none is named, none is found or the YAML does not load: the
// form then draws the settings as a free table.
func (s *Server) definitionOf(ctx context.Context, id, ref string) (*cardigann.Definition, forms.DefinitionInfo) {
	if s.opts.Reader == nil || (id == "" && ref == "") {
		return nil, forms.DefinitionInfo{}
	}
	var list indexv1.IndexerDefinitionList
	if err := s.opts.Reader.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Error("list indexer definitions", "err", err)
		return nil, forms.DefinitionInfo{}
	}
	// A definition's own id wins over an id another one replaces.
	var found, replaced *indexv1.IndexerDefinition
	for i := range list.Items {
		d := &list.Items[i]
		if id == "" {
			if d.Name == ref {
				found = d
				break
			}
			continue
		}
		if d.Status.ID == id || (d.Status.ID == "" && d.Name == id) {
			found = d
			break
		}
		if replaced == nil && slices.Contains(d.Status.Replaces, id) {
			replaced = d
		}
	}
	if found == nil {
		found = replaced
	}
	if found == nil {
		return nil, forms.DefinitionInfo{}
	}
	def, err := cardigann.Load([]byte(found.Spec.YAML))
	if err != nil {
		logging.FromContext(ctx).Warn("indexer definition does not load", "definition", found.Name, "err", err)
		return nil, forms.DefinitionInfo{}
	}
	info := forms.DefinitionInfo{Protocol: string(found.Status.Protocol), Privacy: string(found.Status.Type)}
	if info.Protocol == "" {
		info.Protocol = "torrent" // every Cardigann definition is a torrent site's
	}
	return def, info
}

// specSettings reads a spec map's settings as strings.
func specSettings(spec map[string]any) map[string]string {
	m, _ := spec["settings"].(map[string]any)
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fmt.Sprint(v)
	}
	return out
}

func specString(spec map[string]any, key string) string {
	v, _ := spec[key].(string)
	return v
}

// decorateForm adds what the overlay cannot derive from the schema: the
// Indexer form's Definition section, after the sections that choose it.
func (s *Server) decorateForm(ctx context.Context, k forms.Kind, form *forms.Form, spec map[string]any) {
	if k.Slug != indexersSlug {
		return
	}
	id := specString(spec, "definition")
	def, info := s.definitionOf(ctx, id, specString(spec, "definitionRef"))
	section := forms.DefinitionSection(def, info, specSettings(spec))
	section.Controls = withMissingNote(section.Controls, id, def)
	at := 0
	for i, sec := range form.Sections {
		if sec.Title == "Indexer" || sec.Title == "Generic Newznab/Torznab" {
			at = i + 1
		}
	}
	form.Sections = slices.Insert(form.Sections, at, section)
}

// withMissingNote says so when a definition is named and none was found.
func withMissingNote(controls []forms.Control, id string, def *cardigann.Definition) []forms.Control {
	if id == "" || def != nil {
		return controls
	}
	return append(controls, forms.MissingDefinitionNote(id))
}

// handleSettingsDefinition answers the Indexer form's Definition section
// for the definition its select now names: the section's content alone,
// which the page swaps in (forms.Dynamic). An existing indexer's stored
// settings fill the fields the new definition also declares.
func (s *Server) handleSettingsDefinition(w http.ResponseWriter, r *http.Request) {
	k, _, err := formKind(indexersSlug)
	if err != nil {
		notFound(w, r)
		return
	}
	q := r.URL.Query()
	settings := map[string]string{}
	if name := strings.TrimSpace(q.Get("__name")); name != "" {
		if spec, err := s.getConfigSpec(r.Context(), k, strings.TrimSpace(q.Get("__namespace")), name); err == nil {
			settings = specSettings(spec)
		}
	}
	id := strings.TrimSpace(q.Get("definition"))
	def, info := s.definitionOf(r.Context(), id, "")
	controls := withMissingNote(forms.DefinitionControls(def, info, settings), id, def)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.FormControls(controls).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render definition section", "err", err)
	}
}

// pruneDefaults drops from a decoded Indexer spec the settings that only
// restate its definition's defaults (forms.PruneDefinitionDefaults).
func (s *Server) pruneDefaults(ctx context.Context, k forms.Kind, spec map[string]any) {
	if k.Slug != indexersSlug {
		return
	}
	settings, _ := spec["settings"].(map[string]any)
	if len(settings) == 0 {
		return
	}
	def, _ := s.definitionOf(ctx, specString(spec, "definition"), specString(spec, "definitionRef"))
	forms.PruneDefinitionDefaults(def, settings)
	if len(settings) == 0 {
		delete(spec, "settings")
	}
}

// secretWrite is one Secret a form fills: its name and the entries typed
// into it.
type secretWrite struct {
	name string
	data map[string]string
}

// planSecrets collects every credential the form carried ("__secret.<ref
// path>.<key>", non-blank only) by the secretRef it belongs to, names each
// Secret -- the secretRef's own name, else one after the object -- and
// points the spec's secretRef at it. It writes nothing; writeSecrets does,
// once the object's write has been checked.
func planSecrets(k forms.Kind, values url.Values, spec map[string]any, name string) []secretWrite {
	groups := map[string]map[string]string{}
	for key, vs := range values {
		rest, ok := strings.CutPrefix(key, "__secret.")
		if !ok {
			continue
		}
		i := strings.LastIndex(rest, ".")
		if i <= 0 {
			continue
		}
		refName, entry := rest[:i], rest[i+1:]
		if !secretPathKnown(k, refName) {
			continue
		}
		v := ""
		for j := len(vs) - 1; j >= 0 && v == ""; j-- {
			v = strings.TrimSpace(vs[j])
		}
		if v == "" {
			continue
		}
		if groups[refName] == nil {
			groups[refName] = map[string]string{}
		}
		groups[refName][entry] = v
	}
	refs := make([]string, 0, len(groups))
	for refName := range groups {
		refs = append(refs, refName)
	}
	sort.Strings(refs)
	out := make([]secretWrite, 0, len(refs))
	for _, refName := range refs {
		secretName := strings.TrimSpace(values.Get(refName + ".name"))
		if secretName == "" {
			secretName = defaultSecretName(name, refName, values)
		}
		setPath(spec, refName+".name", secretName)
		out = append(out, secretWrite{name: secretName, data: groups[refName]})
	}
	return out
}

// writeSecrets stores each planned Secret in namespace. Nothing is read
// back, and a Secret the UI did not create is refused
// (actions.ErrSecretNotFromUI).
func (s *Server) writeSecrets(ctx context.Context, namespace string, writes []secretWrite) error {
	for _, sw := range writes {
		if err := s.opts.Actions.WriteSecret(ctx, namespace, sw.name, sw.data); err != nil {
			return err
		}
	}
	return nil
}

// secretPathKnown reports whether refName (indices filled in) is one of
// the kind's declared secretRef paths.
func secretPathKnown(k forms.Kind, refName string) bool {
	var segs []string
	for _, seg := range strings.Split(refName, ".") {
		if _, err := strconv.Atoi(seg); err == nil && len(segs) > 0 {
			segs[len(segs)-1] += "[]"
			continue
		}
		segs = append(segs, seg)
	}
	path := strings.Join(segs, ".")
	for _, sec := range k.Secrets {
		if sec.Path == path {
			return true
		}
	}
	return false
}

// defaultSecretName names a Secret after the object and, for a secretRef
// inside a row, the row's own name (or its index).
func defaultSecretName(object, refName string, values url.Values) string {
	base := object
	segs := strings.Split(refName, ".")
	for i, seg := range segs {
		if _, err := strconv.Atoi(seg); err != nil {
			continue
		}
		rowName := strings.TrimSpace(values.Get(strings.Join(segs[:i+1], ".") + ".name"))
		if rowName == "" {
			rowName = seg
		}
		base += "-" + rowName
	}
	return sanitizeName(base) + "-credentials"
}

func sanitizeName(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			return r
		}
		return '-'
	}, s)
	return strings.Trim(s, "-.")
}

// setPath writes value at a dotted path, creating objects and extending
// arrays as needed.
func setPath(spec map[string]any, path string, value any) {
	segs := strings.Split(path, ".")
	var cur any = spec
	for i, seg := range segs {
		last := i == len(segs)-1
		switch node := cur.(type) {
		case map[string]any:
			if last {
				node[seg] = value
				return
			}
			next, ok := node[seg]
			if !ok || next == nil {
				if _, err := strconv.Atoi(segs[i+1]); err == nil {
					next = []any{}
				} else {
					next = map[string]any{}
				}
				node[seg] = next
			}
			if arr, isArr := next.([]any); isArr {
				idx, _ := strconv.Atoi(segs[i+1])
				for len(arr) <= idx {
					arr = append(arr, map[string]any{})
				}
				node[seg] = arr
				next = arr
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx >= len(node) {
				return
			}
			if last {
				node[idx] = value
				return
			}
			if node[idx] == nil {
				node[idx] = map[string]any{}
			}
			cur = node[idx]
		default:
			return
		}
	}
}

// getPath reads a dotted path, nil when absent.
func getPath(spec map[string]any, path string) any {
	var cur any = spec
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[seg]
		if !ok {
			return nil
		}
	}
	return cur
}

// finishSettings ends a settings write: an error renders as every other
// action's does, success goes back to the Settings page (or the form's
// "return" path).
func (s *Server) finishSettings(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		s.finishAction(w, r, err)
		return
	}
	returnTo := returnPath(r)
	if returnTo == "" {
		returnTo = "/settings"
	}
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

// returnPath is where a form goes when it is done: the "return" it
// carries (a query on GET, a field on POST), which the Import Lists page
// sets to itself so its lists are managed without a detour through
// /settings. Anything but an absolute local path is "" -- the caller's
// default -- so a form can never send the reader off the site.
func returnPath(r *http.Request) string {
	v := r.FormValue("return")
	if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") {
		return ""
	}
	return v
}

// handleSettingsCreate creates the object from the form.
func (s *Server) handleSettingsCreate(w http.ResponseWriter, r *http.Request) {
	k, root, err := formKind(r.PathValue("kind"))
	if err != nil {
		notFound(w, r)
		return
	}
	sub, err := decodeSubmission(r, k, root, forms.ModeNew, "", "")
	if err != nil {
		s.renderForm(w, r, k, root, forms.ModeNew, sub, err)
		return
	}
	s.pruneDefaults(r.Context(), k, sub.spec)
	// Secrets are written before the object, so its controller's first
	// reconcile finds them (they read a Secret by name, with no watch), but
	// only once a dry run of the create has passed: a create the apiserver
	// refuses -- above all a name already taken -- must not first rewrite
	// the credentials the existing object uses. Only a create that loses a
	// race after its dry run can leave Secrets written, and only Secrets
	// the UI made.
	writes := planSecrets(k, r.PostForm, sub.spec, sub.name)
	if len(writes) > 0 {
		if err := s.opts.Actions.CheckCreateConfig(r.Context(), k.ConfigKind, sub.namespace, sub.name, sub.spec); err != nil {
			s.renderForm(w, r, k, root, forms.ModeNew, sub, err)
			return
		}
	}
	if err := s.writeSecrets(r.Context(), sub.namespace, writes); err != nil {
		s.renderForm(w, r, k, root, forms.ModeNew, sub, err)
		return
	}
	if _, err := s.opts.Actions.CreateConfig(r.Context(), k.ConfigKind, sub.namespace, sub.name, sub.spec); err != nil {
		s.renderForm(w, r, k, root, forms.ModeNew, sub, err)
		return
	}
	s.finishSettings(w, r, nil)
}

// handleSettingsUpdate patches the object with what the form changed: the
// merge patch of the stored spec to the decoded one, hidden subtrees kept
// as they were.
func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	k, root, err := formKind(r.PathValue("kind"))
	if err != nil {
		notFound(w, r)
		return
	}
	ns, name := pathNamespace(r), r.PathValue("name")
	old, err := s.getConfigSpec(r.Context(), k, ns, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			notFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sub, err := decodeSubmission(r, k, root, forms.ModeEdit, ns, name)
	if err != nil {
		s.renderForm(w, r, k, root, forms.ModeEdit, sub, err)
		return
	}
	for _, h := range k.Hidden {
		if v := getPath(old, h); v != nil {
			setPath(sub.spec, h, v)
		}
	}
	s.pruneDefaults(r.Context(), k, sub.spec)
	// As for a create: the update's dry run passes before any Secret is
	// written, so an edit the apiserver refuses changes no credential.
	writes := planSecrets(k, r.PostForm, sub.spec, name)
	patch := schema.Diff(old, sub.spec)
	if len(writes) > 0 {
		if err := s.opts.Actions.CheckUpdateConfig(r.Context(), k.ConfigKind, ns, name, patch); err != nil {
			s.renderForm(w, r, k, root, forms.ModeEdit, sub, err)
			return
		}
	}
	if err := s.writeSecrets(r.Context(), ns, writes); err != nil {
		s.renderForm(w, r, k, root, forms.ModeEdit, sub, err)
		return
	}
	if _, err := s.opts.Actions.UpdateConfig(r.Context(), k.ConfigKind, ns, name, patch); err != nil {
		s.renderForm(w, r, k, root, forms.ModeEdit, sub, err)
		return
	}
	s.finishSettings(w, r, nil)
}

// handleSettingsDelete deletes the object.
func (s *Server) handleSettingsDelete(w http.ResponseWriter, r *http.Request) {
	k, _, err := formKind(r.PathValue("kind"))
	if err != nil {
		notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	err = s.opts.Actions.DeleteConfig(r.Context(), k.ConfigKind, pathNamespace(r), r.PathValue("name"))
	s.finishSettings(w, r, err)
}
