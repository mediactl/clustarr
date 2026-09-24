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

package forms_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	crdbases "github.com/mediactl/clustarr/config/crd/bases"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/forms"
	"github.com/mediactl/clustarr/ui/schema"
)

func rootOf(t *testing.T, k forms.Kind) *schema.Field {
	t.Helper()
	crd, err := crdbases.Load(k.Group, k.Kind)
	require.NoError(t, err)
	return schema.Walk(crdbases.SpecSchema(crd, "v1alpha1"))
}

func kind(t *testing.T, slug string) forms.Kind {
	t.Helper()
	k, ok := forms.Lookup(slug)
	require.True(t, ok, slug)
	return k
}

func section(t *testing.T, f *forms.Form, title string) forms.Section {
	t.Helper()
	for _, s := range f.Sections {
		if s.Title == title {
			return s
		}
	}
	titles := make([]string, 0, len(f.Sections))
	for _, s := range f.Sections {
		titles = append(titles, s.Title)
	}
	require.Failf(t, "no section", "%q not among %v", title, titles)
	return forms.Section{}
}

func control(t *testing.T, s forms.Section, name string) forms.Control {
	t.Helper()
	for _, c := range s.Controls {
		if c.Name == name {
			return c
		}
	}
	names := make([]string, 0, len(s.Controls))
	for _, c := range s.Controls {
		names = append(names, c.Name)
	}
	require.Failf(t, "no control", "%q not among %v in %q", name, names, s.Title)
	return forms.Control{}
}

// Every kind the Settings page configures has a form definition, in the
// page's order, and every path an overlay names exists in that kind's CRD
// schema -- an overlay typo would otherwise silently drop a field.
func TestEveryConfigKindHasAnOverlayWhosePathsExist(t *testing.T) {
	require.Len(t, forms.Kinds(), len(actions.ConfigKinds()))
	for i, ck := range actions.ConfigKinds() {
		k := forms.Kinds()[i]
		require.Equal(t, ck, k.ConfigKind, "the forms follow actions.ConfigKinds' order")
		require.NotEmpty(t, k.Title)
		require.NotEmpty(t, k.Singular)
		require.NotEmpty(t, k.Groups, "%s has groups", k.Slug)
		root := rootOf(t, k)
		for _, g := range k.Groups {
			require.NotEmpty(t, g.Title, "%s: a group has a title", k.Slug)
			for _, p := range g.Paths {
				require.NotNil(t, root.Lookup(p), "%s: group %q names %q, which the CRD has not", k.Slug, g.Title, p)
			}
			if g.When != nil {
				require.NotNil(t, root.Lookup(g.When.Path), "%s: group %q shows when %q, which the CRD has not", k.Slug, g.Title, g.When.Path)
			}
		}
		for _, p := range k.Hidden {
			require.NotNil(t, root.Lookup(p), "%s hides %q, which the CRD has not", k.Slug, p)
		}
		for p := range k.Labels {
			require.NotNil(t, root.Lookup(p), "%s labels %q, which the CRD has not", k.Slug, p)
		}
		for p, ref := range k.Refs {
			f := root.Lookup(p)
			require.NotNil(t, f, "%s: ref %q", k.Slug, p)
			require.Equal(t, schema.TypeString, f.Type, "%s: a ref field is a string", k.Slug)
			require.NotEmpty(t, ref)
		}
		for _, s := range k.Secrets {
			f := root.Lookup(s.Path)
			require.NotNil(t, f, "%s: secret at %q", k.Slug, s.Path)
			require.Equal(t, schema.TypeObject, f.Type)
			require.NotNil(t, f.Lookup("name"), "%s: %q is a LocalObjectReference", k.Slug, s.Path)
			require.NotEmpty(t, s.Keys)
		}
		for _, p := range k.ReadOnlyOnEdit {
			require.NotNil(t, root.Lookup(p), "%s: read-only %q", k.Slug, p)
		}
		// Every kind builds an empty form and one from a nil spec.
		f := forms.Build(k, root, nil, nil, forms.ModeNew)
		require.NotEmpty(t, f.Sections)
	}
}

// The download-client form: the overlay's groups in order, each control
// typed from the schema and filled from the object, the torrent and usenet
// groups conditional on the protocol, providers as rows with credentials,
// workload plumbing hidden, and whatever the overlay did not place in an
// Other section at the end.
func TestBuildRendersTheDownloadClientForm(t *testing.T) {
	k := kind(t, "downloadclients")
	root := rootOf(t, k)
	spec := map[string]any{
		"protocol":   "usenet",
		"enabled":    false,
		"priority":   int64(5),
		"categories": map[string]any{"movie": "films"},
		"usenet": map[string]any{
			"providers": []any{
				map[string]any{"name": "eweka", "host": "news.eweka.nl", "port": int64(563), "tls": true, "secretRef": map[string]any{"name": "eweka-creds"}},
			},
			"postProcess": map[string]any{"cleanupPatterns": []any{"*.nfo", "*.sfv"}},
		},
	}
	f := forms.Build(k, root, spec, nil, forms.ModeEdit)
	require.Equal(t, forms.ModeEdit, f.Mode)
	require.Equal(t, k.Slug, f.Kind.Slug)

	client := section(t, f, "Client")
	protocol := control(t, client, "protocol")
	require.Equal(t, forms.ControlSelect, protocol.Type)
	require.Equal(t, "usenet", protocol.Value)
	require.True(t, protocol.Required)
	require.True(t, protocol.ReadOnly, "protocol is immutable: read-only on edit")
	require.Equal(t, []forms.Option{{Value: "torrent", Label: "torrent"}, {Value: "usenet", Label: "usenet"}}, protocol.Options)
	enabled := control(t, client, "enabled")
	require.Equal(t, forms.ControlCheckbox, enabled.Type)
	require.False(t, enabled.Checked)
	require.Equal(t, "Enabled", enabled.Label)
	priority := control(t, client, "priority")
	require.Equal(t, forms.ControlNumber, priority.Type)
	require.Equal(t, "5", priority.Value)
	require.InDelta(t, 1, *priority.Min, 0)
	require.InDelta(t, 50, *priority.Max, 0)
	require.Contains(t, priority.Help, "Priority orders clients")
	categories := control(t, client, "categories")
	require.Equal(t, forms.ControlMap, categories.Type)
	require.Equal(t, []forms.Pair{{Key: "movie", Value: "films"}}, categories.Pairs)

	torrent := section(t, f, "Torrent")
	require.Equal(t, &forms.When{Path: "protocol", Values: []string{"torrent"}}, torrent.When)
	port := control(t, torrent, "torrent.listenPort")
	require.Equal(t, forms.ControlNumber, port.Type)
	require.Equal(t, "", port.Value, "unset on this object")
	require.Equal(t, "42069", port.Placeholder, "the schema default shows as the placeholder")
	require.Equal(t, "Listen port", port.Label)
	require.Equal(t, "Enable DHT", control(t, torrent, "torrent.enableDHT").Label)
	ratio := control(t, torrent, "torrent.seed.ratio")
	require.Equal(t, forms.ControlText, ratio.Type, "a quantity is typed as text")

	usenet := section(t, f, "Usenet")
	require.Equal(t, &forms.When{Path: "protocol", Values: []string{"usenet"}}, usenet.When)
	providers := control(t, usenet, "usenet.providers")
	require.Equal(t, forms.ControlRows, providers.Type)
	require.Len(t, providers.Rows, 1)
	row := providers.Rows[0]
	require.Equal(t, 0, row.Index)
	host := control(t, forms.Section{Controls: row.Controls}, "usenet.providers.0.host")
	require.Equal(t, "news.eweka.nl", host.Value)
	require.True(t, host.Required)
	require.Equal(t, forms.ControlNumber, control(t, forms.Section{Controls: row.Controls}, "usenet.providers.0.port").Type)
	require.True(t, control(t, forms.Section{Controls: row.Controls}, "usenet.providers.0.tls").Checked)
	secretName := control(t, forms.Section{Controls: row.Controls}, "usenet.providers.0.secretRef.name")
	require.Equal(t, forms.ControlText, secretName.Type)
	require.Equal(t, "eweka-creds", secretName.Value)
	user := control(t, forms.Section{Controls: row.Controls}, "__secret.usenet.providers.0.secretRef.username")
	require.Equal(t, forms.ControlPassword, user.Type)
	require.Equal(t, "", user.Value, "a credential is never read back")
	require.Equal(t, "Username", user.Label)
	require.True(t, user.Required)
	require.Equal(t, forms.ControlPassword, control(t, forms.Section{Controls: row.Controls}, "__secret.usenet.providers.0.secretRef.password").Type)
	require.NotEmpty(t, providers.Template, "a row template for the Add button")
	require.Equal(t, "usenet.providers.__i__.host", control(t, forms.Section{Controls: providers.Template}, "usenet.providers.__i__.host").Name)
	patterns := control(t, usenet, "usenet.postProcess.cleanupPatterns")
	require.Equal(t, forms.ControlLines, patterns.Type)
	require.Equal(t, []string{"*.nfo", "*.sfv"}, patterns.Lines)

	for _, s := range f.Sections {
		for _, c := range s.Controls {
			require.False(t, strings.HasPrefix(c.Name, "resources"), "workload plumbing stays a YAML concern: %s", c.Name)
			require.False(t, strings.HasPrefix(c.Name, "nodeSelector"), c.Name)
			require.False(t, strings.HasPrefix(c.Name, "tolerations"), c.Name)
		}
	}
	last := f.Sections[len(f.Sections)-1]
	require.NotEqual(t, "Other", last.Title, "the download-client overlay places every field it shows")
}

// A new form has no values, secret inputs still render, an immutable field
// is editable, and the Other section collects what an overlay left out.
func TestBuildNewFormAndTheOtherSection(t *testing.T) {
	k := kind(t, "downloadclients")
	root := rootOf(t, k)
	f := forms.Build(k, root, nil, nil, forms.ModeNew)
	require.False(t, control(t, section(t, f, "Client"), "protocol").ReadOnly)
	require.Empty(t, control(t, section(t, f, "Usenet"), "usenet.providers").Rows)

	// A kind with a deliberately sparse overlay puts the rest under Other.
	sparse := k
	sparse.Groups = []forms.Group{{Title: "Client", Paths: []string{"protocol"}}}
	sparse.Hidden = nil
	f = forms.Build(sparse, root, nil, nil, forms.ModeNew)
	other := section(t, f, "Other")
	require.NotEmpty(t, control(t, other, "enabled").Name)
	require.NotEmpty(t, control(t, other, "torrent.listenPort").Name, "an object's children are expanded")
	require.Equal(t, "Other", f.Sections[len(f.Sections)-1].Title)
	_, has := lookupControl(f, "protocol", "Other")
	require.False(t, has, "a placed field is not repeated")
}

func lookupControl(f *forms.Form, name, title string) (forms.Control, bool) {
	for _, s := range f.Sections {
		if s.Title != title {
			continue
		}
		for _, c := range s.Controls {
			if c.Name == name {
				return c, true
			}
		}
	}
	return forms.Control{}, false
}

// Reference fields take their choices from live objects; a missing list
// leaves a plain text input so the form still works without a cluster.
func TestBuildFillsReferenceChoices(t *testing.T) {
	k := kind(t, "rootfolders")
	root := rootOf(t, k)
	choices := forms.Choices{forms.RefQualityProfiles: {{Value: "hd-bluray-web", Label: "hd-bluray-web"}, {Value: "web-1080p", Label: "web-1080p"}}}
	f := forms.Build(k, root, map[string]any{"defaults": map[string]any{"qualityProfileRef": "web-1080p"}}, choices, forms.ModeEdit)
	qp := control(t, section(t, f, "Defaults for new items"), "defaults.qualityProfileRef")
	require.Equal(t, forms.ControlSelect, qp.Type)
	require.Equal(t, "web-1080p", qp.Value)
	require.Equal(t, []forms.Option{{Value: "", Label: "—"}, {Value: "hd-bluray-web", Label: "hd-bluray-web"}, {Value: "web-1080p", Label: "web-1080p"}}, qp.Options)
	f = forms.Build(k, root, nil, nil, forms.ModeNew)
	require.Equal(t, forms.ControlText, control(t, section(t, f, "Defaults for new items"), "defaults.qualityProfileRef").Type)

	path := control(t, section(t, f, "Folder"), "path")
	require.True(t, path.Required)
	require.False(t, path.ReadOnly)
	require.True(t, control(t, section(t, forms.Build(k, root, map[string]any{"path": "/data/media/movies"}, nil, forms.ModeEdit), "Folder"), "path").ReadOnly)
}

// Ensure fixes what a form cannot express: a torrent client needs an
// empty torrent object for the CRD's rule, and never a usenet one.
func TestEnsureCompletesADownloadClientSpec(t *testing.T) {
	k := kind(t, "downloadclients")
	spec := map[string]any{"protocol": "torrent", "usenet": map[string]any{"preCheck": true}}
	k.Ensure(spec)
	require.Equal(t, map[string]any{"protocol": "torrent", "torrent": map[string]any{}}, spec)
	spec = map[string]any{"protocol": "usenet", "torrent": map[string]any{"listenPort": int64(1)}}
	k.Ensure(spec)
	require.Equal(t, map[string]any{"protocol": "usenet", "usenet": map[string]any{}}, spec)
}

// Humanize turns a JSON field name into a label.
func TestHumanize(t *testing.T) {
	for in, want := range map[string]string{
		"listenPort": "Listen port", "enableDHT": "Enable DHT", "baseURL": "Base URL", "maxUnverifiedBytes": "Max unverified bytes",
		"tls": "TLS", "crf": "CRF", "hdr10Plus": "HDR10 plus", "path": "Path", "requestsPerSecondMilli": "Requests per second milli",
		"qualityProfileRef": "Quality profile", "secretRef": "Secret", "downloadClientRef": "Download client", "apiPath": "API path",
	} {
		require.Equal(t, want, forms.Humanize(in), in)
	}
}
