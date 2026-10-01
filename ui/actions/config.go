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
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// This file is the Settings page's configuration writes (settings CRUD
// design, docs/superpowers/specs/2026-09-24-settings-crud-design.md): the
// UI creates, edits and deletes the eight kinds the page shows, and writes
// the Secrets their credentials live in. The spec of a created or edited
// object is whatever ui/schema decoded from the form -- an unstructured
// map, typed by the kind's own CRD schema and validated by the apiserver
// -- so this file names kinds, not fields. Every write carries
// [FieldManager]; every created object carries [LabelOrigin]. A Secret is
// created or patched, never read: the role grants create and patch on
// secrets and nothing else, so no page can ever read one back. Since that
// role reaches every Secret in the cluster, a patch proves the Secret is
// the UI's own inside the request itself (see [WriteSecret]).

// ErrSecretNotFromUI is returned by [WriteSecret] for an existing Secret
// that does not carry [LabelOrigin]=[OriginUI]: one kubectl, Helm or
// another app made, which the UI must not write into. It wraps
// [ErrInvalid], so a form shows it as a bad request.
var ErrSecretNotFromUI = fmt.Errorf("%w: the Secret was not created by the UI", ErrInvalid)

// ConfigKind is one kind the Settings page configures: the slug its routes
// use, its API coordinates, the resource RBAC names it by, and whether it
// is namespaced.
type ConfigKind struct {
	Slug       string
	Group      string
	Version    string
	Kind       string
	Resource   string
	Namespaced bool
}

// GroupVersionKind is the kind's coordinates.
func (k ConfigKind) GroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: k.Group, Version: k.Version, Kind: k.Kind}
}

var configKinds = []ConfigKind{
	{Slug: "rootfolders", Group: catalogv1alpha1.GroupVersion.Group, Version: catalogv1alpha1.GroupVersion.Version, Kind: "RootFolder", Resource: "rootfolders", Namespaced: true},
	{Slug: "qualityprofiles", Group: catalogv1alpha1.GroupVersion.Group, Version: catalogv1alpha1.GroupVersion.Version, Kind: "QualityProfile", Resource: "qualityprofiles"},
	{Slug: "metadataproviders", Group: catalogv1alpha1.GroupVersion.Group, Version: catalogv1alpha1.GroupVersion.Version, Kind: "MetadataProvider", Resource: "metadataproviders", Namespaced: true},
	{Slug: "importlists", Group: catalogv1alpha1.GroupVersion.Group, Version: catalogv1alpha1.GroupVersion.Version, Kind: "ImportList", Resource: "importlists", Namespaced: true},
	{Slug: "indexers", Group: indexv1alpha1.GroupVersion.Group, Version: indexv1alpha1.GroupVersion.Version, Kind: "Indexer", Resource: "indexers", Namespaced: true},
	{Slug: "indexerproxies", Group: indexv1alpha1.GroupVersion.Group, Version: indexv1alpha1.GroupVersion.Version, Kind: "IndexerProxy", Resource: "indexerproxies", Namespaced: true},
	{Slug: "downloadclients", Group: downloadv1alpha1.GroupVersion.Group, Version: downloadv1alpha1.GroupVersion.Version, Kind: "DownloadClient", Resource: "downloadclients", Namespaced: true},
	{Slug: "subtitleproviders", Group: subtitlev1alpha1.GroupVersion.Group, Version: subtitlev1alpha1.GroupVersion.Version, Kind: "SubtitleProvider", Resource: "subtitleproviders", Namespaced: true},
	{Slug: "subtitleprofiles", Group: subtitlev1alpha1.GroupVersion.Group, Version: subtitlev1alpha1.GroupVersion.Version, Kind: "SubtitleProfile", Resource: "subtitleprofiles"},
	{Slug: "transcodeprofiles", Group: transcodev1alpha1.GroupVersion.Group, Version: transcodev1alpha1.GroupVersion.Version, Kind: "TranscodeProfile", Resource: "transcodeprofiles"},
}

// ConfigKinds returns the kinds the Settings page configures, in the order
// the page shows them.
func ConfigKinds() []ConfigKind {
	out := make([]ConfigKind, len(configKinds))
	copy(out, configKinds)
	return out
}

// ConfigKindBySlug finds a kind by its route slug.
func ConfigKindBySlug(slug string) (ConfigKind, bool) {
	for _, k := range configKinds {
		if k.Slug == slug {
			return k, true
		}
	}
	return ConfigKind{}, false
}

// configGrants is [Grants]'s share from this file: create, patch and delete
// on every settings kind, and create and patch on secrets.
func configGrants() []Grant {
	var out []Grant
	for _, k := range configKinds {
		for _, verb := range []string{"create", "patch", "delete"} {
			out = append(out, Grant{Group: k.Group, Resource: k.Resource, Verb: verb})
		}
	}
	return append(out,
		Grant{Group: "", Resource: "secrets", Verb: "create"},
		Grant{Group: "", Resource: "secrets", Verb: "patch"},
	)
}

// Deleter is the one method [DeleteConfig] needs.
type Deleter interface {
	Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error
}

// validateConfigTarget checks the namespace against the kind's scope and
// the name against what the apiserver accepts, before any request.
func validateConfigTarget(k ConfigKind, namespace, name string) error {
	if k.Kind == "" {
		return fmt.Errorf("%w: unknown settings kind", ErrInvalid)
	}
	if k.Namespaced && namespace == "" {
		return fmt.Errorf("%w: %s is namespaced; need a namespace", ErrInvalid, k.Kind)
	}
	if !k.Namespaced && namespace != "" {
		return fmt.Errorf("%w: %s is cluster-scoped; got namespace %q", ErrInvalid, k.Kind, namespace)
	}
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return fmt.Errorf("%w: name %q: %s", ErrInvalid, name, strings.Join(errs, "; "))
	}
	return nil
}

func configObject(k ConfigKind, namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(k.GroupVersionKind())
	u.SetNamespace(namespace)
	u.SetName(name)
	return u
}

// CreateConfig creates one object of kind k with the given spec (a map as
// ui/schema decodes it), labelled UI-made, under [FieldManager]. The
// apiserver validates the spec; its error is wrapped so errors.As still
// reaches it.
func CreateConfig(
	ctx context.Context, c Creator, k ConfigKind, namespace, name string, spec map[string]any,
) (*unstructured.Unstructured, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.CreateConfig")
	defer span.End()

	u, err := createConfig(ctx, c, k, namespace, name, spec)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	logging.FromContext(ctx).Info("ui action: settings object created", "kind", k.Kind, "namespace", namespace, "name", name)
	return u, nil
}

// CheckCreateConfig is [CreateConfig] as a server-side dry run: the
// apiserver admits or refuses the object -- its name free, its spec valid
// -- exactly as it would the create, and stores nothing. The form runs it
// before writing any Secret, so a create that would fail writes none.
func CheckCreateConfig(ctx context.Context, c Creator, k ConfigKind, namespace, name string, spec map[string]any) error {
	ctx, span := tracing.Start(ctx, "ui.actions.CheckCreateConfig")
	defer span.End()

	if _, err := createConfig(ctx, c, k, namespace, name, spec, client.DryRunAll); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	return nil
}

func createConfig(
	ctx context.Context, c Creator, k ConfigKind, namespace, name string, spec map[string]any, opts ...client.CreateOption,
) (*unstructured.Unstructured, error) {
	if err := validateConfigTarget(k, namespace, name); err != nil {
		return nil, err
	}
	u := configObject(k, namespace, name)
	u.SetLabels(map[string]string{LabelOrigin: OriginUI})
	if spec == nil {
		spec = map[string]any{}
	}
	u.Object["spec"] = spec
	if err := c.Create(ctx, u, append(opts, client.FieldOwner(FieldManager))...); err != nil {
		return nil, fmt.Errorf("actions: create %s %s/%s: %w", k.Kind, namespace, name, err)
	}
	return u, nil
}

type specPatch struct {
	Spec map[string]any `json:"spec"`
}

// UpdateConfig sends patch -- the merge patch of spec ui/schema.Patch
// computed, nulls included -- to the object, under [FieldManager]. An empty
// patch sends nothing.
func UpdateConfig(
	ctx context.Context, p Patcher, k ConfigKind, namespace, name string, patch map[string]any,
) (*unstructured.Unstructured, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.UpdateConfig")
	defer span.End()

	u, sent, err := updateConfig(ctx, p, k, namespace, name, patch)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if sent {
		logging.FromContext(ctx).Info("ui action: settings object updated", "kind", k.Kind, "namespace", namespace, "name", name)
	}
	return u, nil
}

// CheckUpdateConfig is [UpdateConfig] as a server-side dry run: the
// apiserver validates the patched object and stores nothing. The form runs
// it before writing any Secret, as [CheckCreateConfig] for a create.
func CheckUpdateConfig(ctx context.Context, p Patcher, k ConfigKind, namespace, name string, patch map[string]any) error {
	ctx, span := tracing.Start(ctx, "ui.actions.CheckUpdateConfig")
	defer span.End()

	if _, _, err := updateConfig(ctx, p, k, namespace, name, patch, client.DryRunAll); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	return nil
}

// updateConfig sends the patch, reporting whether it sent one.
func updateConfig(
	ctx context.Context, p Patcher, k ConfigKind, namespace, name string, patch map[string]any, opts ...client.PatchOption,
) (*unstructured.Unstructured, bool, error) {
	if err := validateConfigTarget(k, namespace, name); err != nil {
		return nil, false, err
	}
	u := configObject(k, namespace, name)
	if len(patch) == 0 {
		return u, false, nil
	}
	raw, err := json.Marshal(specPatch{Spec: patch})
	if err != nil {
		return nil, false, fmt.Errorf("actions: encode %s patch: %w", k.Kind, err)
	}
	if err := p.Patch(ctx, u, client.RawPatch(types.MergePatchType, raw), append(opts, client.FieldOwner(FieldManager))...); err != nil {
		return nil, false, fmt.Errorf("actions: update %s %s/%s: %w", k.Kind, namespace, name, err)
	}
	return u, true, nil
}

// DeleteConfig deletes one object of kind k. The controllers' finalizers,
// where a kind has any, do their work as for any delete.
func DeleteConfig(ctx context.Context, d Deleter, k ConfigKind, namespace, name string) error {
	ctx, span := tracing.Start(ctx, "ui.actions.DeleteConfig")
	defer span.End()

	if err := validateConfigTarget(k, namespace, name); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	if err := d.Delete(ctx, configObject(k, namespace, name)); err != nil {
		err = fmt.Errorf("actions: delete %s %s/%s: %w", k.Kind, namespace, name, err)
		tracing.RecordError(span, err)
		return err
	}
	logging.FromContext(ctx).Info("ui action: settings object deleted", "kind", k.Kind, "namespace", namespace, "name", name)
	return nil
}

// jsonPatchOp is one RFC 6902 operation.
type jsonPatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// originLabelPointer is [LabelOrigin] as an RFC 6901 JSON Pointer into
// metadata.labels: its "/" is escaped "~1" ("~" would be "~0", first).
var originLabelPointer = "/metadata/labels/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(LabelOrigin)

// WriteSecret stores the given entries in the Secret namespace/name: it
// creates the Secret, labelled [LabelOrigin]=[OriginUI], and when one
// exists sends exactly those entries -- so an entry the form left blank,
// and so never passed here, keeps its stored value. It never reads the
// Secret, and the role lets it neither. Values travel as stringData, never
// base64 in this package: the apiserver folds stringData into data when it
// decodes the patched v1 Secret, as it does for a create.
//
// The role's create and patch reach every Secret in the cluster and the UI
// is anonymous, so the existing-Secret path is a JSON Patch whose first op
// tests the origin label: the apiserver applies the ops atomically, so a
// Secret the UI did not create (no label, or another origin) fails the
// test and is not written at all -- [ErrSecretNotFromUI]. That proves
// ownership without a get the role must never grant.
func WriteSecret(ctx context.Context, w Writer, namespace, name string, data map[string]string) error {
	ctx, span := tracing.Start(ctx, "ui.actions.WriteSecret")
	defer span.End()

	if namespace == "" || name == "" {
		err := fmt.Errorf("%w: need a namespace and a name (got %q/%q)", ErrInvalid, namespace, name)
		tracing.RecordError(span, err)
		return err
	}
	if len(data) == 0 {
		err := fmt.Errorf("%w: nothing to write into Secret %s/%s", ErrInvalid, namespace, name)
		tracing.RecordError(span, err)
		return err
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		// A bad key would be the apiserver's Invalid too, but checked here
		// it can never be mistaken for the failed ownership test below.
		if errs := validation.IsConfigMapKey(k); len(errs) > 0 {
			err := fmt.Errorf("%w: Secret key %q: %s", ErrInvalid, k, strings.Join(errs, "; "))
			tracing.RecordError(span, err)
			return err
		}
		keys = append(keys, k)
	}
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: map[string]string{LabelOrigin: OriginUI}},
		StringData: data,
	}
	err := w.Create(ctx, s, client.FieldOwner(FieldManager))
	if apierrors.IsAlreadyExists(err) {
		// stringData is write-only, never stored, so an existing Secret has
		// none and "add" sets the whole object.
		raw, mErr := json.Marshal([]jsonPatchOp{
			{Op: "test", Path: originLabelPointer, Value: OriginUI},
			{Op: "add", Path: "/stringData", Value: data},
		})
		if mErr != nil {
			return fmt.Errorf("actions: encode Secret patch: %w", mErr)
		}
		obj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
		err = w.Patch(ctx, obj, client.RawPatch(types.JSONPatchType, raw), client.FieldOwner(FieldManager))
		if failedPatchTest(err) {
			err = fmt.Errorf("%w: Secret %s/%s has no %s=%s label, so the UI did not create it and will not "+
				"write into it; edit it with kubectl, or name a new Secret", ErrSecretNotFromUI, namespace, name, LabelOrigin, OriginUI)
			tracing.RecordError(span, err)
			return err
		}
	}
	if err != nil {
		err = fmt.Errorf("actions: write Secret %s/%s: %w", namespace, name, err)
		tracing.RecordError(span, err)
		return err
	}
	logging.FromContext(ctx).Info("ui action: secret written", "namespace", namespace, "secret", name, "keys", keys)
	return nil
}

// failedPatchTest reports whether err is the apiserver failing to apply a
// JSON Patch: a 422 Invalid naming no field (a 1.37 apiserver sends empty
// details and a generic message), as against a validation error of the
// patched object, whose every cause names the field it rejects (an
// immutable Secret's data, say). WriteSecret's only op that can fail to
// apply is its "test", and its keys are checked before it is sent.
func failedPatchTest(err error) bool {
	var status *apierrors.StatusError
	if !apierrors.IsInvalid(err) || !errors.As(err, &status) {
		return false
	}
	if details := status.ErrStatus.Details; details != nil {
		for _, cause := range details.Causes {
			if cause.Field != "" {
				return false
			}
		}
	}
	return true
}

// --- Actions methods ---------------------------------------------------------

// CreateConfig is [CreateConfig] over the Actions' writer.
func (a *Actions) CreateConfig(ctx context.Context, k ConfigKind, namespace, name string, spec map[string]any) (*unstructured.Unstructured, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return CreateConfig(ctx, a.w, k, namespace, name, spec)
}

// UpdateConfig is [UpdateConfig] over the Actions' writer.
func (a *Actions) UpdateConfig(ctx context.Context, k ConfigKind, namespace, name string, patch map[string]any) (*unstructured.Unstructured, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return UpdateConfig(ctx, a.w, k, namespace, name, patch)
}

// DeleteConfig is [DeleteConfig] over the Actions' writer.
func (a *Actions) DeleteConfig(ctx context.Context, k ConfigKind, namespace, name string) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return DeleteConfig(ctx, a.w, k, namespace, name)
}

// CheckCreateConfig is [CheckCreateConfig] over the Actions' writer.
func (a *Actions) CheckCreateConfig(ctx context.Context, k ConfigKind, namespace, name string, spec map[string]any) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return CheckCreateConfig(ctx, a.w, k, namespace, name, spec)
}

// CheckUpdateConfig is [CheckUpdateConfig] over the Actions' writer.
func (a *Actions) CheckUpdateConfig(ctx context.Context, k ConfigKind, namespace, name string, patch map[string]any) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return CheckUpdateConfig(ctx, a.w, k, namespace, name, patch)
}

// WriteSecret is [WriteSecret] over the Actions' writer.
func (a *Actions) WriteSecret(ctx context.Context, namespace, name string, data map[string]string) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return WriteSecret(ctx, a.w, namespace, name, data)
}
