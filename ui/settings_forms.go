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
	"sort"
	"strconv"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	subtitlev1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	crdbases "github.com/mediactl/clustarr/config/crd/bases"
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
	"indexers":          func() client.Object { return &indexv1.Indexer{} },
	"downloadclients":   func() client.Object { return &downloadv1.DownloadClient{} },
	"subtitleproviders": func() client.Object { return &subtitlev1.SubtitleProvider{} },
	"subtitleprofiles":  func() client.Object { return &subtitlev1.SubtitleProfile{} },
	"transcodeprofiles": func() client.Object { return &transcodev1.TranscodeProfile{} },
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
	switch ref {
	case forms.RefQualityProfiles:
		for _, qp := range s.listQualityProfiles(ctx) {
			names([]string{qp.Name})
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
		_, clients := s.listDownloads(ctx)
		for _, dc := range clients {
			names([]string{dc.Name})
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
	page := views.FormPage{
		Form:      forms.Build(k, root, sub.spec, s.choices(r.Context(), k), mode),
		Namespace: sub.namespace,
		Name:      sub.name,
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
		logging.FromContext(r.Context()).Error("settings form rejected", "kind", k.Kind, "error", err, "code", page.ErrorCode)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if renderErr := views.SettingsForm(page).Render(r.Context(), w); renderErr != nil {
		logging.FromContext(r.Context()).Error("render settings form", "error", renderErr)
	}
}

// handleSettingsNew renders an empty form for the kind.
func (s *Server) handleSettingsNew(w http.ResponseWriter, r *http.Request) {
	k, root, err := formKind(r.PathValue("kind"))
	if err != nil {
		http.NotFound(w, r)
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
		http.NotFound(w, r)
		return
	}
	ns, name := pathNamespace(r), r.PathValue("name")
	spec, err := s.getConfigSpec(r.Context(), k, ns, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			http.NotFound(w, r)
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
	if k.Ensure != nil {
		k.Ensure(spec)
	}
	sub.spec = spec
	return sub, nil
}

// writeSecrets stores every credential the form carried ("__secret.<ref
// path>.<key>", non-blank only) in the Secret the matching secretRef
// names -- named after the object when the form left it blank -- and
// points the spec's secretRef at it. Nothing is read back.
func (s *Server) writeSecrets(ctx context.Context, k forms.Kind, values url.Values, spec map[string]any, namespace, name string) error {
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
	for _, refName := range refs {
		secretName := strings.TrimSpace(values.Get(refName + ".name"))
		if secretName == "" {
			secretName = defaultSecretName(name, refName, values)
		}
		setPath(spec, refName+".name", secretName)
		if err := s.opts.Actions.WriteSecret(ctx, namespace, secretName, groups[refName]); err != nil {
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
	returnTo := r.FormValue("return")
	if returnTo == "" || returnTo[0] != '/' {
		returnTo = "/settings"
	}
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

// handleSettingsCreate creates the object from the form.
func (s *Server) handleSettingsCreate(w http.ResponseWriter, r *http.Request) {
	k, root, err := formKind(r.PathValue("kind"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sub, err := decodeSubmission(r, k, root, forms.ModeNew, "", "")
	if err != nil {
		s.renderForm(w, r, k, root, forms.ModeNew, sub, err)
		return
	}
	if err := s.writeSecrets(r.Context(), k, r.PostForm, sub.spec, sub.namespace, sub.name); err != nil {
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
		http.NotFound(w, r)
		return
	}
	ns, name := pathNamespace(r), r.PathValue("name")
	old, err := s.getConfigSpec(r.Context(), k, ns, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			http.NotFound(w, r)
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
	if err := s.writeSecrets(r.Context(), k, r.PostForm, sub.spec, ns, name); err != nil {
		s.renderForm(w, r, k, root, forms.ModeEdit, sub, err)
		return
	}
	if _, err := s.opts.Actions.UpdateConfig(r.Context(), k.ConfigKind, ns, name, schema.Diff(old, sub.spec)); err != nil {
		s.renderForm(w, r, k, root, forms.ModeEdit, sub, err)
		return
	}
	s.finishSettings(w, r, nil)
}

// handleSettingsDelete deletes the object.
func (s *Server) handleSettingsDelete(w http.ResponseWriter, r *http.Request) {
	k, _, err := formKind(r.PathValue("kind"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	err = s.opts.Actions.DeleteConfig(r.Context(), k.ConfigKind, pathNamespace(r), r.PathValue("name"))
	s.finishSettings(w, r, err)
}
