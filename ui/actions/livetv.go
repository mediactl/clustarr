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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clustarrv1alpha1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// This file is the Live TV pages' writes (the direct-tuners and ui design
// 2026-10-27 §5.2, §5.4): a channel edited in place or in bulk, a playlist
// entry added as a channel, and a provider's "Refresh now". IPTVProviders
// and IPTVChannels are also settings kinds (config.go), whose forms create,
// edit and delete them whole; these are the Channels page's quick writes.

// channelNumber is IPTVChannelSpec.Number's CRD pattern.
var channelNumber = regexp.MustCompile(`^[0-9]{1,5}(\.[0-9]{1,3})?$`)

// ChannelEdit is one IPTVChannel's edit from the Channels page; a nil
// field is left alone.
type ChannelEdit struct {
	// Name is the IPTVChannel's object name; ResourceVersion the version
	// the page read, which the patch carries, so an edit that lost a race
	// is refused (Conflict), never written over the other.
	Name, ResourceVersion string
	Active                *bool
	// Number must match the CRD's pattern ("1001", "5.1").
	Number *string
	// DisplayName is spec.name; "" removes the override, as do Group and
	// Logo's.
	DisplayName, Group, Logo *string
	// EPG is the guide mapping: "" automatic (spec.epg removed, mapped by
	// tvg-id), "dummy" a placeholder guide, else a guide channel id.
	EPG *string
}

// channelSpecPatch is the spec half of a ChannelEdit's merge patch: a
// field is sent when the edit names it, null to remove an override.
func (e ChannelEdit) channelSpecPatch() map[string]any {
	spec := map[string]any{}
	if e.Active != nil {
		spec["active"] = *e.Active
	}
	if e.Number != nil {
		spec["number"] = *e.Number
	}
	for field, v := range map[string]*string{"name": e.DisplayName, "group": e.Group, "logo": e.Logo} {
		switch {
		case v == nil:
		case *v == "":
			spec[field] = nil
		default:
			spec[field] = *v
		}
	}
	if e.EPG != nil {
		switch *e.EPG {
		case "":
			spec["epg"] = nil
		case "dummy":
			spec["epg"] = map[string]any{"dummy": true}
		default:
			spec["epg"] = map[string]any{"channelID": *e.EPG}
		}
	}
	return spec
}

func (e ChannelEdit) validate() error {
	if errs := validation.IsDNS1123Subdomain(e.Name); len(errs) > 0 {
		return fmt.Errorf("%w: channel %q: %v", ErrInvalid, e.Name, errs)
	}
	if e.ResourceVersion == "" {
		return fmt.Errorf("%w: channel %s: the edit names no resourceVersion", ErrInvalid, e.Name)
	}
	if e.Number != nil && !channelNumber.MatchString(*e.Number) {
		return fmt.Errorf("%w: channel %s: number %q is not a channel number like 1001 or 5.1", ErrInvalid, e.Name, *e.Number)
	}
	if e.EPG != nil && len(*e.EPG) > 256 {
		return fmt.Errorf("%w: channel %s: guide channel id over 256 characters", ErrInvalid, e.Name)
	}
	return nil
}

// EditChannels sends each edit as one merge patch of the channel's spec
// under the read resourceVersion and [FieldManager], after checking every
// edit (none is written when any is refused), in order, stopping at the
// first refusal. It returns the patched objects.
func EditChannels(ctx context.Context, p Patcher, namespace string, edits []ChannelEdit) ([]*clustarrv1alpha1.IPTVChannel, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.EditChannels")
	defer span.End()

	if errs := validation.IsDNS1123Label(namespace); len(errs) > 0 {
		err := fmt.Errorf("%w: namespace %q: %v", ErrInvalid, namespace, errs)
		tracing.RecordError(span, err)
		return nil, err
	}
	for _, e := range edits {
		if err := e.validate(); err != nil {
			tracing.RecordError(span, err)
			return nil, err
		}
	}
	out := make([]*clustarrv1alpha1.IPTVChannel, 0, len(edits))
	for _, e := range edits {
		body := map[string]any{"metadata": map[string]any{"resourceVersion": e.ResourceVersion}, "spec": e.channelSpecPatch()}
		raw, err := json.Marshal(body)
		if err != nil {
			return out, fmt.Errorf("actions: encode channel patch: %w", err)
		}
		ch := &clustarrv1alpha1.IPTVChannel{}
		ch.SetNamespace(namespace)
		ch.SetName(e.Name)
		if err := p.Patch(ctx, ch, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
			err = fmt.Errorf("actions: edit channel %s/%s: %w", namespace, e.Name, err)
			tracing.RecordError(span, err)
			return out, err
		}
		out = append(out, ch)
	}
	logging.FromContext(ctx).Info("ui action: channels edited", "namespace", namespace, "count", len(out))
	return out, nil
}

// ChannelObjectName is the name an IPTVChannel the ui adds takes: its
// provider's name and a hash of its key, so a key is added once (a second
// add is AlreadyExists).
func ChannelObjectName(provider, key string) string {
	sum := sha256.Sum256([]byte(key))
	if len(provider) > 52 {
		provider = provider[:52]
	}
	return provider + "-" + hex.EncodeToString(sum[:])[:10]
}

// AddChannel creates an active IPTVChannel for the provider's playlist
// entry key at number, named name, labelled UI-made, under [FieldManager].
func AddChannel(ctx context.Context, c Creator, namespace, provider, key, number, name string) (*clustarrv1alpha1.IPTVChannel, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.AddChannel")
	defer span.End()

	var err error
	switch {
	case validation.IsDNS1123Label(namespace) != nil:
		err = fmt.Errorf("%w: namespace %q", ErrInvalid, namespace)
	case validation.IsDNS1123Subdomain(provider) != nil:
		err = fmt.Errorf("%w: provider %q", ErrInvalid, provider)
	case key == "" || len(key) > 64:
		err = fmt.Errorf("%w: playlist key %q", ErrInvalid, key)
	case !channelNumber.MatchString(number):
		err = fmt.Errorf("%w: number %q is not a channel number like 1001 or 5.1", ErrInvalid, number)
	}
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	ch := &clustarrv1alpha1.IPTVChannel{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: ChannelObjectName(provider, key), Labels: map[string]string{LabelOrigin: OriginUI}},
		Spec:       clustarrv1alpha1.IPTVChannelSpec{ProviderRef: provider, Key: key, Number: number, Name: name, Active: true},
	}
	if err := c.Create(ctx, ch, client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: add channel %s for %s/%s: %w", number, namespace, provider, err)
		tracing.RecordError(span, err)
		return nil, err
	}
	logging.FromContext(ctx).Info("ui action: channel added", "namespace", namespace, "provider", provider, "number", number)
	return ch, nil
}

// RefreshLiveTV writes [clustarrv1alpha1.AnnotationLiveTVRefresh] with at's
// Unix time on the provider: the controller hands the new value to the
// tuner, which fetches every source now.
func RefreshLiveTV(ctx context.Context, p Patcher, namespace, name string, at time.Time) error {
	ctx, span := tracing.Start(ctx, "ui.actions.RefreshLiveTV")
	defer span.End()

	if validation.IsDNS1123Label(namespace) != nil || validation.IsDNS1123Subdomain(name) != nil {
		err := fmt.Errorf("%w: provider %s/%s", ErrInvalid, namespace, name)
		tracing.RecordError(span, err)
		return err
	}
	raw, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{
		clustarrv1alpha1.AnnotationLiveTVRefresh: strconv.FormatInt(at.Unix(), 10),
	}}})
	if err != nil {
		return fmt.Errorf("actions: encode refresh patch: %w", err)
	}
	pr := &clustarrv1alpha1.IPTVProvider{}
	pr.SetNamespace(namespace)
	pr.SetName(name)
	if err := p.Patch(ctx, pr, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: refresh provider %s/%s: %w", namespace, name, err)
		tracing.RecordError(span, err)
		return err
	}
	logging.FromContext(ctx).Info("ui action: Live TV refresh requested", "namespace", namespace, "provider", name)
	return nil
}

// EditChannels is [EditChannels] over the Actions' writer.
func (a *Actions) EditChannels(ctx context.Context, namespace string, edits []ChannelEdit) ([]*clustarrv1alpha1.IPTVChannel, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return EditChannels(ctx, a.w, namespace, edits)
}

// AddChannel is [AddChannel] over the Actions' writer.
func (a *Actions) AddChannel(ctx context.Context, namespace, provider, key, number, name string) (*clustarrv1alpha1.IPTVChannel, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return AddChannel(ctx, a.w, namespace, provider, key, number, name)
}

// RefreshLiveTV is [RefreshLiveTV] over the Actions' writer, at the current
// time.
func (a *Actions) RefreshLiveTV(ctx context.Context, namespace, name string) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return RefreshLiveTV(ctx, a.w, namespace, name, time.Now())
}
