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

package crdbases_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	crdbases "github.com/mediactl/clustarr/config/crd/bases"
)

// The UI derives its settings forms from the CRDs the apiserver enforces
// (settings CRUD design, 2026-09-24), so the generated CRDs are embedded
// from the directory controller-gen writes them into: every kind the
// Settings page edits loads, with its v1alpha1 spec schema.
func TestEveryEditableKindHasAnEmbeddedSchema(t *testing.T) {
	for _, tc := range []struct{ group, kind string }{
		{"catalog.clustarr.io", "RootFolder"},
		{"catalog.clustarr.io", "QualityProfile"},
		{"catalog.clustarr.io", "MetadataProvider"},
		{"index.clustarr.io", "Indexer"},
		{"index.clustarr.io", "IndexerDefinition"},
		{"download.clustarr.io", "DownloadClient"},
		{"subtitle.clustarr.io", "SubtitleProvider"},
		{"subtitle.clustarr.io", "SubtitleProfile"},
		{"transcode.clustarr.io", "TranscodeProfile"},
	} {
		crd, err := crdbases.Load(tc.group, tc.kind)
		require.NoError(t, err, "%s/%s", tc.group, tc.kind)
		require.Equal(t, tc.kind, crd.Spec.Names.Kind)
		require.Equal(t, tc.group, crd.Spec.Group)
		schema := crdbases.SpecSchema(crd, "v1alpha1")
		require.NotNil(t, schema, "%s has a v1alpha1 spec schema", tc.kind)
		require.NotEmpty(t, schema.Properties, "%s's spec has properties", tc.kind)
	}
	_, err := crdbases.Load("catalog.clustarr.io", "Nothing")
	require.Error(t, err, "an unknown kind is an error, not a nil")
	require.Len(t, crdbases.All(), 30, "every generated CRD is embedded; regenerate with make manifests")
}
