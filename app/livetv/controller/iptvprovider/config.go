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

package iptvprovider

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/iptv/filter"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// AnnotationRefresh forces a fetch of every source: the ui's "Refresh now"
// sets it to a new value (spec §5.1).
const AnnotationRefresh = "clustarr.io/livetv-refresh"

// resolved is a provider's URLs with its Secrets read.
type resolved struct {
	playlist string
	guides   map[string]string
}

// resolveURLs reads every urlFrom the provider names. A missing Secret or
// key is a Valid=False condition naming it, never its value.
func (r *Reconciler) resolveURLs(ctx context.Context, p *clustarrv1.IPTVProvider) (resolved, metav1.Condition) {
	ok := metav1.Condition{Type: clustarrv1.ConditionValid, Status: metav1.ConditionTrue, Reason: "Valid", Message: "every Secret the provider names exists"}
	read := func(sel *corev1.SecretKeySelector) (string, *metav1.Condition) {
		var s corev1.Secret
		err := r.SecretReader.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: sel.Name}, &s)
		switch {
		case apierrors.IsNotFound(err):
			return "", &metav1.Condition{
				Type: clustarrv1.ConditionValid, Status: metav1.ConditionFalse, Reason: clustarrv1.ReasonSecretNotFound,
				Message: fmt.Sprintf("Secret %s/%s does not exist", p.Namespace, sel.Name),
			}
		case err != nil:
			return "", &metav1.Condition{
				Type: clustarrv1.ConditionValid, Status: metav1.ConditionFalse, Reason: clustarrv1.ReasonSecretNotFound,
				Message: fmt.Sprintf("Secret %s/%s cannot be read: %v", p.Namespace, sel.Name, err),
			}
		}
		v, has := s.Data[sel.Key]
		if !has || len(v) == 0 {
			return "", &metav1.Condition{
				Type: clustarrv1.ConditionValid, Status: metav1.ConditionFalse, Reason: clustarrv1.ReasonSecretKeyNotFound,
				Message: fmt.Sprintf("Secret %s/%s has no key %q", p.Namespace, sel.Name, sel.Key),
			}
		}
		return string(v), nil
	}
	out := resolved{playlist: p.Spec.Playlist.URL, guides: map[string]string{}}
	if sel := p.Spec.Playlist.URLFrom; sel != nil {
		v, bad := read(sel)
		if bad != nil {
			return out, *bad
		}
		out.playlist = v
	}
	for _, g := range p.Spec.Guides {
		out.guides[g.Name] = g.URL
		if g.URLFrom != nil {
			v, bad := read(g.URLFrom)
			if bad != nil {
				return out, *bad
			}
			out.guides[g.Name] = v
		}
	}
	return out, ok
}

// buildConfig is the provider as the tuner sees it: every spec field read
// through its accessor, its URLs resolved, its channels mapped, its blocks
// as planned.
func buildConfig(p *clustarrv1.IPTVProvider, blocks []tuner.BlockConfig, urls resolved, channels []clustarrv1.IPTVChannel) tuner.Config {
	c := tuner.Config{
		Key:             types.NamespacedName{Namespace: p.Namespace, Name: p.Name},
		Blocks:          blocks,
		Tuners:          int(p.Spec.Tuners),
		UserAgent:       p.Spec.Playlist.UserAgentOrDefault(),
		PlaylistURL:     urls.playlist,
		PlaylistRefresh: p.Spec.Playlist.RefreshOrDefault(),
		EPGSource:       string(p.Spec.EPGSourceOrDefault()),
		Buffer:          int(p.Spec.Stream.BufferOrDefault()),
		Linger:          p.Spec.Stream.LingerOrDefault(),
		Refresh:         p.Annotations[AnnotationRefresh],
	}
	for _, g := range p.Spec.Guides {
		c.Guides = append(c.Guides, tuner.GuideConfig{Name: g.Name, URL: urls.guides[g.Name], Refresh: g.RefreshOrDefault()})
	}
	for _, f := range p.Spec.Filters {
		c.Filters = append(c.Filters, filter.Rule{
			Name: f.Name, Type: string(f.Type), Match: f.Match, Include: f.Include,
			Exclude: f.Exclude, CaseSensitive: f.CaseSensitive, Enabled: f.EnabledOrDefault(),
		})
	}
	for i := range channels {
		ch := &channels[i]
		cc := tuner.ChannelConfig{
			Object: ch.Name, Key: ch.Spec.Key, Number: ch.Spec.Number, Name: ch.Spec.Name,
			Group: ch.Spec.Group, Logo: ch.Spec.Logo, Active: ch.Spec.Active, TimeshiftMinutes: int(ch.Spec.TimeshiftMinutes),
		}
		if e := ch.Spec.EPG; e != nil {
			if e.Dummy {
				cc.DummyMinutes = int(e.DummyMinutesOrDefault())
			} else if e.Guide != "" {
				cc.Map = &xmltv.Mapping{Guide: e.Guide, ChannelID: e.ChannelID}
			}
		}
		c.Channels = append(c.Channels, cc)
	}
	return c
}
