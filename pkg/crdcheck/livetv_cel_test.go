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

package crdcheck

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestLiveTVCEL holds the clustarr.io kinds' CEL rules as built. An
// IPTVProvider is an Xtream account by default (e98589e7, 2026-10-20:
// spec.server and spec.credentialsRef, the panel's playlist, guide and
// account fetched from them) or, since 51b02260 (2026-10-27, the
// direct-tuners and ui design §5.7), a plain M3U playlist (spec.type M3U,
// spec.playlistURL; a credentialsRef only when its URLs take one); the
// 3771faa9 playlist url/urlFrom pair is gone. An IPTVChannel's epg maps by
// channelID or draws a dummy guide, at most one of the two, neither being
// by tvg-id (1e4244ae, 2026-11-03, which added epg.plex: a station or off,
// exactly one); providerRef and key are immutable. It writes through a
// dynamic client so an absent key stays absent on the wire.
func TestLiveTVCEL(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test` to install the CRDs")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })
	dyn, err := dynamic.NewForConfig(cfg)
	require.NoError(t, err)
	ctx := context.Background()
	providers := dyn.Resource(schema.GroupVersionResource{Group: "clustarr.io", Version: "v1alpha1", Resource: "iptvproviders"}).Namespace("default")
	channels := dyn.Resource(schema.GroupVersionResource{Group: "clustarr.io", Version: "v1alpha1", Resource: "iptvchannels"}).Namespace("default")

	provider := func(name string, fields map[string]any) *unstructured.Unstructured {
		spec := map[string]any{"tuners": int64(2)}
		for k, v := range fields {
			spec[k] = v
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "clustarr.io/v1alpha1", "kind": "IPTVProvider",
			"metadata": map[string]any{"name": name},
			"spec":     spec,
		}}
	}
	creds := map[string]any{"name": "argon-tv"}
	const typeRule = "an Xtream provider needs server and credentialsRef; an M3U provider needs playlistURL"
	_, err = providers.Create(ctx, provider("xtream", map[string]any{"server": "tvload.win", "credentialsRef": creds}), metav1.CreateOptions{})
	require.NoError(t, err, "an Xtream provider is the default type: server and credentialsRef")
	_, err = providers.Create(ctx, provider("xtream-typed", map[string]any{"type": "Xtream", "server": "http://panel.example:8080", "credentialsRef": creds}), metav1.CreateOptions{})
	require.NoError(t, err, "typed Xtream, a URL server")
	_, err = providers.Create(ctx, provider("m3u", map[string]any{"type": "M3U", "playlistURL": "https://p.example/list.m3u"}), metav1.CreateOptions{})
	require.NoError(t, err, "an M3U provider needs its playlistURL alone")
	_, err = providers.Create(ctx, provider("m3u-creds", map[string]any{"type": "M3U", "playlistURL": "https://p.example/get.php?username={username}&password={password}", "credentialsRef": creds}), metav1.CreateOptions{})
	require.NoError(t, err, "an M3U provider may name a credentialsRef for its URLs' placeholders")
	for name, fields := range map[string]map[string]any{
		"xtream-no-creds":  {"server": "tvload.win"},
		"xtream-no-server": {"credentialsRef": creds},
		"xtream-empty-ref": {"server": "tvload.win", "credentialsRef": map[string]any{"name": ""}},
		"m3u-no-url":       {"type": "M3U"},
		"m3u-account-only": {"type": "M3U", "server": "tvload.win", "credentialsRef": creds},
		"url-untyped":      {"playlistURL": "https://p.example/list.m3u"},
		"neither":          {},
	} {
		_, err := providers.Create(ctx, provider(name, fields), metav1.CreateOptions{})
		require.ErrorContains(t, err, typeRule, name)
	}
	// The enum case sits on an otherwise-valid Xtream account, so the type
	// rule's else-branch holds and only the enum can refuse it; a Plain
	// provider naming a playlistURL alone failed the type rule instead.
	_, err = providers.Create(ctx, provider("bad-type", map[string]any{"type": "Plain", "server": "tvload.win", "credentialsRef": creds}), metav1.CreateOptions{})
	require.ErrorContains(t, err, `Unsupported value: "Plain"`, "type is Xtream or M3U")
	_, err = providers.Create(ctx, provider("bad-guide", map[string]any{"server": "tvload.win", "credentialsRef": creds, "guideURL": "ftp://p.example/epg.xml"}), metav1.CreateOptions{})
	require.Error(t, err, "guideURL is http or https")

	// spec.device.id is eight uppercase hex digits; spec.dvr.maxChannels
	// is 1-450 (PMS takes ~454 channels in one device's map, recorded).
	with := func(name string, path []string, v any) *unstructured.Unstructured {
		p := provider(name, map[string]any{"server": "tvload.win", "credentialsRef": creds})
		require.NoError(t, unstructured.SetNestedField(p.Object, v, append([]string{"spec"}, path...)...))
		return p
	}
	for _, c := range []struct {
		name string
		path []string
		v    any
		ok   bool
	}{
		{"id-ok", []string{"device", "id"}, "CAFE0001", true},
		{"id-lower", []string{"device", "id"}, "cafe0001", false},
		{"id-short", []string{"device", "id"}, "CAFE001", false},
		{"id-long", []string{"device", "id"}, "CAFE00011", false},
		{"id-not-hex", []string{"device", "id"}, "CAFEXYZ1", false},
		{"max-1", []string{"dvr", "maxChannels"}, int64(1), true},
		{"max-450", []string{"dvr", "maxChannels"}, int64(450), true},
		{"max-0", []string{"dvr", "maxChannels"}, int64(0), false},
		{"max-451", []string{"dvr", "maxChannels"}, int64(451), false},
	} {
		_, err := providers.Create(ctx, with(c.name, c.path, c.v), metav1.CreateOptions{})
		if c.ok {
			require.NoError(t, err, c.name)
		} else {
			require.Error(t, err, c.name)
		}
	}

	channel := func(name string, epg map[string]any) *unstructured.Unstructured {
		spec := map[string]any{"providerRef": "xtream", "key": "t:bbc1.uk", "number": "1001", "active": true}
		if epg != nil {
			spec["epg"] = epg
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "clustarr.io/v1alpha1", "kind": "IPTVChannel",
			"metadata": map[string]any{"name": name}, "spec": spec,
		}}
	}
	_, err = channels.Create(ctx, channel("guide", map[string]any{"channelID": "bbc1.uk"}), metav1.CreateOptions{})
	require.NoError(t, err, "channelID maps the channel to the provider's guide")
	_, err = channels.Create(ctx, channel("dummy", map[string]any{"dummy": true, "dummyMinutes": int64(120)}), metav1.CreateOptions{})
	require.NoError(t, err, "dummy draws a placeholder guide")
	_, err = channels.Create(ctx, channel("both-epg", map[string]any{"channelID": "x", "dummy": true}), metav1.CreateOptions{})
	require.ErrorContains(t, err, "set at most one of channelID and dummy")
	_, err = channels.Create(ctx, channel("empty-epg", map[string]any{}), metav1.CreateOptions{})
	require.NoError(t, err, "an epg naming neither maps by tvg-id, as an absent one does")
	_, err = channels.Create(ctx, channel("no-epg", nil), metav1.CreateOptions{})
	require.NoError(t, err, "no epg means by tvg-id, else dummy")
	_, err = channels.Create(ctx, channel("bad-minutes", map[string]any{"dummy": true, "dummyMinutes": int64(45)}), metav1.CreateOptions{})
	require.Error(t, err, "dummyMinutes is 30, 60, 120 or 180")
	_, err = channels.Create(ctx, channel("plex-station", map[string]any{"plex": map[string]any{"station": "657c8183afd0f02cd353633f"}}), metav1.CreateOptions{})
	require.NoError(t, err, "a hand-picked station beside a tvg-id mapping")
	_, err = channels.Create(ctx, channel("plex-off", map[string]any{"channelID": "bbc1.uk", "plex": map[string]any{"off": true}}), metav1.CreateOptions{})
	require.NoError(t, err, "off Plex beside a guide mapping")
	_, err = channels.Create(ctx, channel("plex-both", map[string]any{"plex": map[string]any{"station": "657c8183afd0f02cd353633f", "off": true}}), metav1.CreateOptions{})
	require.ErrorContains(t, err, "set exactly one of station and off")
	_, err = channels.Create(ctx, channel("plex-neither", map[string]any{"plex": map[string]any{}}), metav1.CreateOptions{})
	require.ErrorContains(t, err, "set exactly one of station and off")
	_, err = channels.Create(ctx, channel("plex-bad-station", map[string]any{"plex": map[string]any{"station": "WABC"}}), metav1.CreateOptions{})
	require.Error(t, err, "a station is a 24-hex gridKey")

	got, err := channels.Get(ctx, "guide", metav1.GetOptions{})
	require.NoError(t, err)
	require.NoError(t, unstructured.SetNestedField(got.Object, "t:other", "spec", "key"))
	_, err = channels.Update(ctx, got, metav1.UpdateOptions{})
	require.ErrorContains(t, err, "key is immutable")
	got, err = channels.Get(ctx, "guide", metav1.GetOptions{})
	require.NoError(t, err)
	require.NoError(t, unstructured.SetNestedField(got.Object, "other", "spec", "providerRef"))
	_, err = channels.Update(ctx, got, metav1.UpdateOptions{})
	require.ErrorContains(t, err, "providerRef is immutable")

	bad := channel("bad-number", nil)
	require.NoError(t, unstructured.SetNestedField(bad.Object, "10-01", "spec", "number"))
	_, err = channels.Create(ctx, bad, metav1.CreateOptions{})
	require.Error(t, err, "a number is digits with an optional .minor")
}

// TestLineupCEL holds the custom guides design's §5.1 rules: category bases
// are unique, the first rule of a name carries a base, the Plex source's
// rate is 1-10 and its lineups 24 hex, and a channel's spec.category of ""
// (off every DVR) survives the round trip as the empty string.
func TestLineupCEL(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test` to install the CRDs")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })
	dyn, err := dynamic.NewForConfig(cfg)
	require.NoError(t, err)
	ctx := context.Background()
	providers := dyn.Resource(schema.GroupVersionResource{Group: "clustarr.io", Version: "v1alpha1", Resource: "iptvproviders"}).Namespace("default")
	channels := dyn.Resource(schema.GroupVersionResource{Group: "clustarr.io", Version: "v1alpha1", Resource: "iptvchannels"}).Namespace("default")

	provider := func(name string, fields map[string]any) *unstructured.Unstructured {
		spec := map[string]any{"server": "tvload.win", "credentialsRef": map[string]any{"name": "argon-tv"}, "tuners": int64(2)}
		for k, v := range fields {
			spec[k] = v
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "clustarr.io/v1alpha1", "kind": "IPTVProvider",
			"metadata": map[string]any{"name": name}, "spec": spec,
		}}
	}
	lineup := func(cats ...any) map[string]any {
		return map[string]any{"lineup": map[string]any{"enabled": true, "categories": cats}}
	}
	cat := func(name string, base int64) map[string]any {
		c := map[string]any{"name": name, "groups": []any{name}}
		if base > 0 {
			c["base"] = base
		}
		return c
	}
	_, err = providers.Create(ctx, provider("dup-base", lineup(cat("News", 1000), cat("Sports", 1000))), metav1.CreateOptions{})
	require.ErrorContains(t, err, "category bases must be unique")
	_, err = providers.Create(ctx, provider("no-base", lineup(cat("News", 0))), metav1.CreateOptions{})
	require.ErrorContains(t, err, "a category's first rule must carry base")
	_, err = providers.Create(ctx, provider("second-rule", lineup(cat("News", 1000), cat("News", 0))), metav1.CreateOptions{})
	require.NoError(t, err, "a second rule of a name needs no base beside a first that has one")

	_, err = providers.Create(ctx, provider("base-max", lineup(cat("News", 99000))), metav1.CreateOptions{})
	require.NoError(t, err, "base 99000 is the highest")
	_, err = providers.Create(ctx, provider("base-over", lineup(cat("News", 99001))), metav1.CreateOptions{})
	require.Error(t, err, "base 99001 would number channels past five digits")

	plex := func(extra map[string]any) map[string]any {
		p := map[string]any{"credentialsRef": map[string]any{"name": "plex", "key": "token"}, "lineups": []any{"657c8183afd0f02cd353633f"}}
		for k, v := range extra {
			p[k] = v
		}
		return map[string]any{"guideSources": map[string]any{"plex": p}}
	}
	_, err = providers.Create(ctx, provider("plex-ok", plex(map[string]any{"rate": int64(10), "refresh": "12h"})), metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = providers.Create(ctx, provider("plex-rate", plex(map[string]any{"rate": int64(11)})), metav1.CreateOptions{})
	require.Error(t, err, "rate is 1-10")
	_, err = providers.Create(ctx, provider("plex-id", plex(map[string]any{"lineups": []any{"657c8183afd0f02cd353633"}})), metav1.CreateOptions{})
	require.Error(t, err, "a lineup id is 24 hex")

	ch := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "clustarr.io/v1alpha1", "kind": "IPTVChannel",
		"metadata": map[string]any{"name": "off-dvr"},
		"spec":     map[string]any{"providerRef": "plex-ok", "key": "t:x", "number": "1001", "category": ""},
	}}
	_, err = channels.Create(ctx, ch, metav1.CreateOptions{})
	require.NoError(t, err)
	got, err := channels.Get(ctx, "off-dvr", metav1.GetOptions{})
	require.NoError(t, err)
	v, found, err := unstructured.NestedString(got.Object, "spec", "category")
	require.NoError(t, err)
	require.True(t, found, "an empty category is kept, not pruned")
	require.Equal(t, "", v)
}
