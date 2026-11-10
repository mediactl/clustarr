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
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// The Collections page's writes
// (docs/superpowers/specs/2026-11-10-plex-collections-design.md): a
// PlexCollection's spec is created, replaced whole and deleted here, never
// its status, which cluster-plex alone writes. PlexCollection is no
// Settings kind: its filter nests, which the Settings page's schema forms
// do not draw, so it has these typed writes rather than CreateConfig's.

// plexCollectionGrants are the role grants these writes need.
func plexCollectionGrants() []Grant {
	group := catalogv1alpha1.GroupVersion.Group
	return []Grant{
		{Group: group, Resource: "plexcollections", Verb: "create"},
		{Group: group, Resource: "plexcollections", Verb: "patch"},
		{Group: group, Resource: "plexcollections", Verb: "delete"},
	}
}

func validPlexCollectionTarget(namespace, name string) error {
	switch {
	case len(validation.IsDNS1123Label(namespace)) > 0:
		return fmt.Errorf("%w: namespace %q", ErrInvalid, namespace)
	case len(validation.IsDNS1123Subdomain(name)) > 0:
		return fmt.Errorf("%w: name %q", ErrInvalid, name)
	}
	return nil
}

// CreatePlexCollection creates a PlexCollection with its spec alone. The
// Collections page checks the spec first (pkg/plexfilter.Validate, in ui/,
// since ui/actions imports nothing of the module's but api/, pkg/obs/ and
// pkg/names -- TestUINeverWrites); the CRD's CEL rules check it again.
func CreatePlexCollection(
	ctx context.Context, c Creator, namespace, name string, spec catalogv1alpha1.PlexCollectionSpec,
) (*catalogv1alpha1.PlexCollection, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.CreatePlexCollection")
	defer span.End()
	if err := validPlexCollectionTarget(namespace, name); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	pc := &catalogv1alpha1.PlexCollection{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       spec,
	}
	if err := c.Create(ctx, pc, client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: create collection %s/%s: %w", namespace, name, err)
		tracing.RecordError(span, err)
		return nil, err
	}
	logging.FromContext(ctx).Info("ui action: collection created", "namespace", namespace, "name", name)
	return pc, nil
}

// UpdatePlexCollection replaces a PlexCollection's whole spec, as long as
// the resource's spec is still at generation, the one the editor read: a
// JSON patch testing it, then replacing /spec. A merge patch of the spec
// would leave a field the editor cleared (a poster, the summary) standing,
// and a spec edited since the read fails the test instead of being
// overwritten.
func UpdatePlexCollection(
	ctx context.Context, p Patcher, namespace, name string, generation int64, spec catalogv1alpha1.PlexCollectionSpec,
) error {
	ctx, span := tracing.Start(ctx, "ui.actions.UpdatePlexCollection")
	defer span.End()
	if err := validPlexCollectionTarget(namespace, name); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	if generation < 1 {
		err := fmt.Errorf("%w: no generation read", ErrInvalid)
		tracing.RecordError(span, err)
		return err
	}
	// The generation, not the resourceVersion: cluster-plex writes the
	// status within seconds of every save and every 10 minutes, which
	// moves the resourceVersion alone (as actions.EditChannels found).
	raw, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/generation", "value": generation},
		{"op": "replace", "path": "/spec", "value": spec},
	})
	if err != nil {
		return fmt.Errorf("actions: encode collection patch: %w", err)
	}
	obj := &catalogv1alpha1.PlexCollection{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	if err := p.Patch(ctx, obj, client.RawPatch(types.JSONPatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: update collection %s/%s: %w", namespace, name, err)
		tracing.RecordError(span, err)
		return err
	}
	logging.FromContext(ctx).Info("ui action: collection updated", "namespace", namespace, "name", name)
	return nil
}

// DeletePlexCollection deletes a PlexCollection; cluster-plex's finalizer
// deletes the Plex collection first.
func DeletePlexCollection(ctx context.Context, d Deleter, namespace, name string) error {
	ctx, span := tracing.Start(ctx, "ui.actions.DeletePlexCollection")
	defer span.End()
	if err := validPlexCollectionTarget(namespace, name); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	obj := &catalogv1alpha1.PlexCollection{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	if err := d.Delete(ctx, obj); err != nil {
		err = fmt.Errorf("actions: delete collection %s/%s: %w", namespace, name, err)
		tracing.RecordError(span, err)
		return err
	}
	logging.FromContext(ctx).Info("ui action: collection deleted", "namespace", namespace, "name", name)
	return nil
}

// PlexCollectionName is a new collection's resource name from its title:
// lower case, accents dropped, every other run of characters one dash, at
// most 63 characters. "" when the title has no letter or digit.
func PlexCollectionName(title string) string {
	folded, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), title)
	if err != nil {
		folded = title
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(folded) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	name := b.String()
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

// CreatePlexCollection is [CreatePlexCollection] over the Actions' writer.
func (a *Actions) CreatePlexCollection(
	ctx context.Context, namespace, name string, spec catalogv1alpha1.PlexCollectionSpec,
) (*catalogv1alpha1.PlexCollection, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return CreatePlexCollection(ctx, a.w, namespace, name, spec)
}

// UpdatePlexCollection is [UpdatePlexCollection] over the Actions' writer.
func (a *Actions) UpdatePlexCollection(
	ctx context.Context, namespace, name string, generation int64, spec catalogv1alpha1.PlexCollectionSpec,
) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return UpdatePlexCollection(ctx, a.w, namespace, name, generation, spec)
}

// DeletePlexCollection is [DeletePlexCollection] over the Actions' writer.
func (a *Actions) DeletePlexCollection(ctx context.Context, namespace, name string) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return DeletePlexCollection(ctx, a.w, namespace, name)
}
