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

package actions_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/mediactl/clustarr/ui/actions"
)

// configField is one top-level spec field a fixture sets or changes, and the
// value it sends. Values are JSON-shaped ([]any, map[string]any, int64,
// string) so what comes back from the apiserver compares equal to what went
// in.
type configField struct {
	key   string
	value any
}

// configFixture is what one Settings kind needs from this test: the least
// spec its CRD accepts (required fields and CEL rules, read from
// config/crd/bases/<group>_<resource>.yaml); one field with NO CRD default
// that is set at create and nulled by the update -- a nulled defaulted field
// comes back defaulted, so it could never show the null landing; the field
// the update changes; and a spec the CRD rejects, with what rejects it.
type configFixture struct {
	minimal  map[string]any
	nullable configField
	change   configField
	invalid  map[string]any
	rejects  string
}

func (f configFixture) createSpec() map[string]any {
	spec := make(map[string]any, len(f.minimal)+1)
	for k, v := range f.minimal {
		spec[k] = v
	}
	spec[f.nullable.key] = f.nullable.value
	return spec
}

func (f configFixture) updatePatch() map[string]any {
	return map[string]any{f.change.key: f.change.value, f.nullable.key: nil}
}

// configFixtures is keyed by ConfigKind.Slug; the test fails on a kind with
// no fixture and on a fixture with no kind.
var configFixtures = map[string]configFixture{
	"rootfolders": {
		minimal:  map[string]any{"path": "/data/media/movies", "kind": "movie"},
		nullable: configField{"scanSchedule", "0 3 * * *"},
		change:   configField{"minFreeBytes", int64(1 << 30)},
		invalid:  map[string]any{"path": "/tmp/x", "kind": "movie"},
		rejects:  "the CEL rule 'path must start with /data/media/'",
	},
	"qualityprofiles": {
		minimal: map[string]any{
			"mediaKind": "video",
			"tiers":     []any{map[string]any{"name": "HD", "qualities": []any{"Bluray-1080p"}}},
			"cutoff":    "HD",
		},
		nullable: configField{"enabledFormatGroups", []any{"audio"}},
		change:   configField{"minFormatScore", int64(100)},
		invalid: map[string]any{
			"mediaKind": "video",
			"tiers":     []any{map[string]any{"name": "HD", "qualities": []any{"Bluray-1080p"}}},
			"cutoff":    "SD",
		},
		rejects: "the CEL rule 'cutoff must be the name of one of the tiers'",
	},
	"importlists": {
		// exactly one provider sub-object (Plex is the empty one), the kinds
		// it yields, and the defaults an added item gets
		minimal: map[string]any{
			"kinds":    []any{"movie"},
			"plex":     map[string]any{},
			"defaults": map[string]any{"qualityProfileRef": "hd", "rootFolderRef": "movies"},
		},
		nullable: configField{"refreshInterval", "12h"},
		change:   configField{"syncLevel", "logOnly"},
	},
	"metadataproviders": {
		minimal:  map[string]any{"type": "tmdb"},
		nullable: configField{"baseURL", "https://tmdb.example"},
		change:   configField{"priority", int64(10)},
		invalid:  map[string]any{"type": "musicbrainz"},
		rejects:  "the CEL rule 'contactUserAgent is required for musicbrainz and openlibrary'",
	},
	"indexers": {
		minimal:  map[string]any{"baseURL": "https://example.org", "generic": map[string]any{"protocol": "torrent"}},
		nullable: configField{"tags", []any{"anime"}},
		change:   configField{"priority", int64(10)},
		invalid:  map[string]any{"baseURL": "https://example.org"},
		rejects:  "the CEL rule 'exactly one of definition, definitionRef or generic must be set'",
	},
	"downloadclients": {
		minimal:  map[string]any{"protocol": "torrent", "torrent": map[string]any{}},
		nullable: configField{"categories", map[string]any{"movie": "films"}},
		change:   configField{"priority", int64(5)},
		invalid:  map[string]any{"protocol": "usenet"},
		rejects:  "the CEL rule 'torrent must be set for protocol torrent and usenet for protocol usenet'",
	},
	"subtitleproviders": {
		minimal:  map[string]any{"type": "gestdown"},
		nullable: configField{"endpoint", "https://gestdown.example"},
		change:   configField{"priority", int64(90)},
		invalid:  map[string]any{"type": "nope"},
		rejects:  "the enum on spec.type",
	},
	"subtitleprofiles": {
		minimal:  map[string]any{"languages": []any{map[string]any{"key": "en", "language": "en"}}},
		nullable: configField{"cutoff", "en"},
		change:   configField{"hiExtension", "hi"},
		invalid:  map[string]any{"languages": []any{map[string]any{"key": "en", "language": "en"}}, "cutoff": "fr"},
		rejects:  "the CEL rule 'cutoff must be one of spec.languages[].key'",
	},
	"transcodeprofiles": {
		minimal:  map[string]any{},
		nullable: configField{"maxConcurrent", int64(2)},
		change:   configField{"priority", int64(10)},
		invalid:  map[string]any{"chunking": map[string]any{"enabled": true}},
		rejects:  "the CEL rule 'chunking is not supported in v1alpha1'",
	},
}

// TestConfigActionsAgainstARealAPIServer proves the Settings page's
// configuration writes (config.go) against a real apiserver, for every kind
// in actions.ConfigKinds() and for the Secrets their credentials live in.
// config_test.go's fakeWriter shows what the actions send; only an apiserver
// can show what the CRDs make of it: the required fields and CEL rules a
// minimal spec must satisfy, a merge patch's null actually removing a field,
// a rejected spec surfacing as Invalid through the wrapper, and
// metadata.managedFields -- read through the direct client, since every
// cache strips them -- recording clustarr-ui on spec and never on status.
//
// Every write goes through a recording Writer so the last subtest can map
// each (group, resource, verb) the actions hit to actions.Grants(), which
// cmd/clustarr/ui_rbac_test.go holds config/rbac/ui_role.yaml to: envtest
// enforces no RBAC, so this is where an undeclared grant would show.
func TestConfigActionsAgainstARealAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}

	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	ctx := t.Context()
	rec := &recordingConfigWriter{c: c}
	kinds := actions.ConfigKinds()
	require.Len(t, configFixtures, len(kinds), "one fixture per Settings kind, and none for a kind that is gone")

	for _, k := range kinds {
		t.Run(k.Slug, func(t *testing.T) {
			fx, ok := configFixtures[k.Slug]
			require.True(t, ok, "no fixture for %s; add one", k.Slug)
			ns := ""
			if k.Namespaced {
				ns = "default"
			}
			name := "cfg-" + k.Slug
			gvk := k.GroupVersionKind()

			t.Run("create labels the object and owns spec, never status", func(t *testing.T) {
				created, err := actions.CreateConfig(ctx, rec, k, ns, name, fx.createSpec())
				require.NoError(t, err)
				require.Equal(t, name, created.GetName())

				got := getUnstructured(ctx, t, c, gvk, name, ns)
				require.Equal(t, actions.OriginUI, got.GetLabels()[actions.LabelOrigin])
				requireSpecField(t, got, fx.nullable.key, fx.nullable.value)
				// The minimal fields are asserted present, not equal: the
				// apiserver defaults inside them (torrent: {} comes back
				// with eight defaulted leaves; a languages[] item with four).
				for key := range fx.minimal {
					requireSpecFieldPresent(t, got, key)
				}

				entry := requireOneUIEntry(t, got)
				requireFieldsContain(t, entry, "f:metadata", "f:labels", "f:"+actions.LabelOrigin)
				requireFieldsContain(t, entry, "f:spec", "f:"+fx.nullable.key)
				for key := range fx.minimal {
					requireFieldsContain(t, entry, "f:spec", "f:"+key)
				}
				requireNeverOnStatus(t, got)
			})

			t.Run("update changes one field and a null removes another", func(t *testing.T) {
				_, err := actions.UpdateConfig(ctx, rec, k, ns, name, fx.updatePatch())
				require.NoError(t, err)

				got := getUnstructured(ctx, t, c, gvk, name, ns)
				requireSpecField(t, got, fx.change.key, fx.change.value)
				_, found, err := unstructured.NestedFieldNoCopy(got.Object, "spec", fx.nullable.key)
				require.NoError(t, err)
				require.False(t, found, "spec.%s was nulled by the merge patch and must be gone", fx.nullable.key)
				for key := range fx.minimal {
					requireSpecFieldPresent(t, got, key)
				}

				entry := requireOneUIEntry(t, got)
				requireFieldsContain(t, entry, "f:spec", "f:"+fx.change.key)
				requireNeverOnStatus(t, got)
			})

			t.Run("an empty patch sends nothing and succeeds", func(t *testing.T) {
				before := getUnstructured(ctx, t, c, gvk, name, ns)
				patches := rec.count("patch")
				for _, patch := range []map[string]any{nil, {}} {
					u, err := actions.UpdateConfig(ctx, rec, k, ns, name, patch)
					require.NoError(t, err)
					require.Equal(t, name, u.GetName())
				}
				require.Equal(t, patches, rec.count("patch"), "an empty patch must send no request")
				after := getUnstructured(ctx, t, c, gvk, name, ns)
				require.Equal(t, before.GetResourceVersion(), after.GetResourceVersion(), "nothing may have been written")
			})

			t.Run("delete removes the object", func(t *testing.T) {
				require.NoError(t, actions.DeleteConfig(ctx, rec, k, ns, name))
				u := &unstructured.Unstructured{}
				u.SetGroupVersionKind(gvk)
				getErr := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, u)
				require.True(t, apierrors.IsNotFound(getErr), "want NotFound after the delete, got %v", getErr)
			})

			t.Run("a spec the CRD rejects is Invalid through the wrapper and creates nothing", func(t *testing.T) {
				bad := name + "-invalid"
				_, err := actions.CreateConfig(ctx, rec, k, ns, bad, fx.invalid)
				require.Error(t, err)
				require.True(t, apierrors.IsInvalid(err), "want Invalid from %s, got %v", fx.rejects, err)
				var status *apierrors.StatusError
				require.ErrorAs(t, err, &status, "the apiserver's error must be wrapped, not replaced")
				require.ErrorContains(t, err, "actions: create "+k.Kind)

				u := &unstructured.Unstructured{}
				u.SetGroupVersionKind(gvk)
				getErr := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: bad}, u)
				require.True(t, apierrors.IsNotFound(getErr), "the rejected create must have created nothing: %v", getErr)
			})
		})
	}

	t.Run("WriteSecret creates, then patches only the given keys", func(t *testing.T) {
		const ns, name = "default", "cfg-credentials"
		creates, patches := rec.count("create"), rec.count("patch")

		require.NoError(t, actions.WriteSecret(ctx, rec, ns, name, map[string]string{"apikey": "a", "username": "u"}))
		require.NoError(t, actions.WriteSecret(ctx, rec, ns, name, map[string]string{"password": "p"}))
		require.Equal(t, creates+2, rec.count("create"), "each write tries the create first")
		require.Equal(t, patches+1, rec.count("patch"), "the second is told AlreadyExists and patches")

		var s corev1.Secret
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &s))
		require.Equal(t, map[string][]byte{"apikey": []byte("a"), "username": []byte("u"), "password": []byte("p")}, s.Data,
			"the patch must add password and keep the keys it did not send")
		require.Empty(t, s.StringData, "stringData is write-only; the apiserver folds it into data")
		require.Equal(t, actions.OriginUI, s.Labels[actions.LabelOrigin])

		entry := requireOneUIEntry(t, &s)
		requireFieldsContain(t, entry, "f:data", "f:apikey")
		requireFieldsContain(t, entry, "f:data", "f:username")
		requireFieldsContain(t, entry, "f:data", "f:password")
	})

	t.Run("every grant the config actions used is declared in actions.Grants()", func(t *testing.T) {
		used := map[actions.Grant]bool{}
		for _, call := range rec.calls() {
			gvk, err := apiutil.GVKForObject(call.obj, scheme)
			require.NoError(t, err)
			mapping, err := c.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
			require.NoError(t, err)
			used[actions.Grant{Group: mapping.Resource.Group, Resource: mapping.Resource.Resource, Verb: call.verb}] = true
		}
		declared := map[actions.Grant]bool{}
		for _, g := range actions.Grants() {
			declared[g] = true
		}
		for g := range used {
			require.True(t, declared[g],
				"the config actions hit %+v on a real apiserver, but actions.Grants() does not declare it -- "+
					"config/rbac/ui_role.yaml would never grant it, so this passes here and is Forbidden only in "+
					"a real cluster", g)
		}
		require.Len(t, used, 3*len(kinds)+2,
			"create, patch and delete on every Settings kind, plus create and patch on secrets")
	})
}

// requireSpecField asserts spec.<key> is present and equal to want.
func requireSpecField(t *testing.T, obj *unstructured.Unstructured, key string, want any) {
	t.Helper()
	got := requireSpecFieldPresent(t, obj, key)
	require.Equal(t, want, got, "%s/%s: spec.%s", obj.GetNamespace(), obj.GetName(), key)
}

// requireSpecFieldPresent asserts spec.<key> is present and returns it.
func requireSpecFieldPresent(t *testing.T, obj *unstructured.Unstructured, key string) any {
	t.Helper()
	got, found, err := unstructured.NestedFieldNoCopy(obj.Object, "spec", key)
	require.NoError(t, err)
	require.True(t, found, "%s/%s: spec.%s is missing", obj.GetNamespace(), obj.GetName(), key)
	return got
}

// recordingConfigWriter is an actions.Writer over a real client that records
// the object and verb of every call, so the test can count what an action
// sent and map each call to the RBAC resource the apiserver routed it to.
type recordingConfigWriter struct {
	c   client.Client
	mu  sync.Mutex
	log []configCall
}

type configCall struct {
	obj  client.Object
	verb string
}

func (r *recordingConfigWriter) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	r.record(obj, "create")
	return r.c.Create(ctx, obj, opts...)
}

func (r *recordingConfigWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	r.record(obj, "patch")
	return r.c.Patch(ctx, obj, patch, opts...)
}

func (r *recordingConfigWriter) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	r.record(obj, "delete")
	return r.c.Delete(ctx, obj, opts...)
}

func (r *recordingConfigWriter) record(obj client.Object, verb string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, configCall{obj: obj, verb: verb})
}

func (r *recordingConfigWriter) calls() []configCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]configCall(nil), r.log...)
}

func (r *recordingConfigWriter) count(verb string) int {
	n := 0
	for _, call := range r.calls() {
		if call.verb == verb {
			n++
		}
	}
	return n
}
