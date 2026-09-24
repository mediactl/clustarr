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

package schema_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	crdbases "github.com/mediactl/clustarr/config/crd/bases"
	"github.com/mediactl/clustarr/ui/schema"
)

func root(t *testing.T, group, kind string) *schema.Field {
	t.Helper()
	crd, err := crdbases.Load(group, kind)
	require.NoError(t, err)
	spec := crdbases.SpecSchema(crd, "v1alpha1")
	require.NotNil(t, spec)
	return schema.Walk(spec)
}

// Walk turns a CRD's spec schema into the field tree the settings forms
// render from: every scalar with its type, enum, bounds and default, every
// object with its children, arrays with their item, string maps as maps.
func TestWalkReadsTheDownloadClientSchema(t *testing.T) {
	r := root(t, "download.clustarr.io", "DownloadClient")
	require.Equal(t, schema.TypeObject, r.Type)
	require.Empty(t, r.Path, "the root is spec itself")

	protocol := r.Lookup("protocol")
	require.NotNil(t, protocol)
	require.Equal(t, schema.TypeString, protocol.Type)
	require.True(t, protocol.Required, "protocol is in spec.required")
	require.Equal(t, []string{"torrent", "usenet"}, protocol.Enum)
	require.Equal(t, "protocol", protocol.Name)

	port := r.Lookup("torrent.listenPort")
	require.NotNil(t, port)
	require.Equal(t, schema.TypeInteger, port.Type)
	require.Equal(t, "torrent.listenPort", port.Path)
	require.NotNil(t, port.Minimum)
	require.InDelta(t, 1, *port.Minimum, 0)
	require.InDelta(t, 65535, *port.Maximum, 0)
	require.Equal(t, float64(42069), port.Default, "a JSON default decodes as JSON does, a float64")
	require.Contains(t, port.Description, "ListenPort is the TCP/uTP port")

	enabled := r.Lookup("enabled")
	require.Equal(t, schema.TypeBoolean, enabled.Type)
	require.Equal(t, true, enabled.Default)

	categories := r.Lookup("categories")
	require.Equal(t, schema.TypeMap, categories.Type)
	require.NotNil(t, categories.Values)
	require.Equal(t, schema.TypeString, categories.Values.Type)

	providers := r.Lookup("usenet.providers")
	require.Equal(t, schema.TypeArray, providers.Type)
	require.NotNil(t, providers.Items)
	require.Equal(t, schema.TypeObject, providers.Items.Type)
	host := providers.Items.Lookup("host")
	require.NotNil(t, host, "an array item's children are reachable through the item")
	require.True(t, host.Required)
	require.Equal(t, "usenet.providers[].host", host.Path, "an item's child path carries [] for the index")
	require.Nil(t, r.Lookup("usenet.providers.host"), "there is no such path without the index")

	ratio := r.Lookup("torrent.seed.ratio")
	require.Equal(t, schema.TypeQuantity, ratio.Type, "an int-or-string with a quantity pattern")
	limits := r.Lookup("resources.limits")
	require.Equal(t, schema.TypeMap, limits.Type)
	require.Equal(t, schema.TypeQuantity, limits.Values.Type)

	tolerations := r.Lookup("tolerations")
	require.Equal(t, schema.TypeArray, tolerations.Type)
	require.Equal(t, schema.TypeObject, tolerations.Items.Type)

	// Children come sorted by name, so a render is stable.
	names := make([]string, 0, len(r.Fields))
	for _, f := range r.Fields {
		names = append(names, f.Name)
	}
	require.Equal(t, []string{"categories", "enabled", "nodeSelector", "priority", "protocol", "replicas", "resources", "tolerations", "torrent", "usenet"}, names)
}

// Decode types every posted input by its field: integers parsed, booleans
// from the input's last value (a hidden false under a checkbox true), an
// empty input omitted, an enum held to the schema, a map from paired
// key/value inputs, an unknown name ignored.
func TestDecodeTypesEveryInputFromTheSchema(t *testing.T) {
	r := root(t, "download.clustarr.io", "DownloadClient")
	v := url.Values{
		"protocol":           {"torrent"},
		"enabled":            {"false", "true"},
		"priority":           {"5"},
		"replicas":           {""},
		"torrent.listenPort": {"51413"},
		"torrent.enableDHT":  {"false"},
		"torrent.publicIP":   {"  "},
		"torrent.seed.ratio": {"2.0"},
		"categories.__k.0":   {"movie"},
		"categories.__v.0":   {"movies"},
		"categories.__k.1":   {""},
		"categories.__v.1":   {"orphan value without a key"},
		"bogus":              {"1"},
		"torrent.bogus":      {"1"},
	}
	got, err := schema.Decode(r, v)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"protocol": "torrent",
		"enabled":  true,
		"priority": int64(5),
		"torrent": map[string]any{
			"listenPort": int64(51413),
			"enableDHT":  false,
			"seed":       map[string]any{"ratio": "2.0"},
		},
		"categories": map[string]any{"movie": "movies"},
	}, got)

	_, err = schema.Decode(r, url.Values{"protocol": {"torrent"}, "priority": {"abc"}})
	require.ErrorContains(t, err, "priority")
	require.ErrorContains(t, err, "integer")
	_, err = schema.Decode(r, url.Values{"protocol": {"ftp"}})
	require.ErrorContains(t, err, "protocol")
	require.ErrorContains(t, err, "torrent, usenet")
	_, err = schema.Decode(r, url.Values{"protocol": {"torrent"}, "torrent.enableDHT": {"maybe"}})
	require.ErrorContains(t, err, "torrent.enableDHT")
}

// Arrays: objects from indexed names (sparse indices compacted, an empty
// row dropped), scalars from repeated inputs or one input with a line per
// value, integers parsed per element.
func TestDecodeArraysOfObjectsAndScalars(t *testing.T) {
	r := root(t, "download.clustarr.io", "DownloadClient")
	got, err := schema.Decode(r, url.Values{
		"protocol":                           {"usenet"},
		"usenet.providers.0.name":            {"eweka"},
		"usenet.providers.0.host":            {"news.eweka.nl"},
		"usenet.providers.0.port":            {"563"},
		"usenet.providers.0.tls":             {"true"},
		"usenet.providers.0.secretRef.name":  {"eweka-credentials"},
		"usenet.providers.1.name":            {""},
		"usenet.providers.1.host":            {""},
		"usenet.providers.2.name":            {"backup"},
		"usenet.providers.2.host":            {"news.backup.example"},
		"usenet.providers.2.backup":          {"true"},
		"usenet.postProcess.cleanupPatterns": {"*.nfo\n*.sfv\n\n"},
	})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"protocol": "usenet",
		"usenet": map[string]any{
			"providers": []any{
				map[string]any{"name": "eweka", "host": "news.eweka.nl", "port": int64(563), "tls": true, "secretRef": map[string]any{"name": "eweka-credentials"}},
				map[string]any{"name": "backup", "host": "news.backup.example", "backup": true},
			},
			"postProcess": map[string]any{"cleanupPatterns": []any{"*.nfo", "*.sfv"}},
		},
	}, got)

	idx := root(t, "index.clustarr.io", "Indexer")
	got, err = schema.Decode(idx, url.Values{
		"baseURL":    {"https://example.org"},
		"categories": {"2000", "5000"},
		"tags":       {"anime\nhd"},
	})
	require.NoError(t, err)
	require.Equal(t, []any{int64(2000), int64(5000)}, got["categories"])
	require.Equal(t, []any{"anime", "hd"}, got["tags"])
	_, err = schema.Decode(idx, url.Values{"baseURL": {"x"}, "categories": {"2000", "tv"}})
	require.ErrorContains(t, err, "categories")
}

// Diff is the merge patch an edit sends: a changed leaf, null for a key
// the new spec dropped (so a cleared field really clears), nothing for an
// unchanged one, whole arrays.
func TestDiffClearsDroppedFieldsAndLeavesUnchangedOnesOut(t *testing.T) {
	old := map[string]any{
		"enabled":    true,
		"priority":   int64(5),
		"torrent":    map[string]any{"listenPort": int64(1), "seed": map[string]any{"ratio": "1"}},
		"categories": map[string]any{"movie": "m"},
		"tags":       []any{"a", "b"},
	}
	current := map[string]any{
		"enabled":  true,
		"priority": int64(7),
		"torrent":  map[string]any{"listenPort": int64(1)},
		"tags":     []any{"a", "b"},
	}
	require.Equal(t, map[string]any{
		"priority":   int64(7),
		"torrent":    map[string]any{"seed": nil},
		"categories": nil,
	}, schema.Diff(old, current))

	require.Empty(t, schema.Diff(old, old), "no change, no patch")
	require.Equal(t, map[string]any{"tags": []any{"b"}}, schema.Diff(
		map[string]any{"tags": []any{"a", "b"}}, map[string]any{"tags": []any{"b"}}), "an array is replaced whole")
	require.Equal(t, map[string]any{"torrent": map[string]any{"listenPort": int64(2)}}, schema.Diff(
		map[string]any{}, map[string]any{"torrent": map[string]any{"listenPort": int64(2)}}), "a new object is sent whole")
}
