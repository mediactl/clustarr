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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// TestImageTypeEnumMatchesPkgMetadata holds catalog's Image.type enum to
// exactly pkg/metadata.ImageType's constants (gap-fix X1 item 5). The CRD used
// to accept three of the nine roles, so the metadata patcher dropped every
// banner, clearart, thumb, screenshot, disc and headshot a provider returned.
// Both sides are read from source -- the constants from pkg/metadata's AST,
// the enum from the generated CRD -- so neither list is restated here.
func TestImageTypeEnumMatchesPkgMetadata(t *testing.T) {
	want := imageTypeConstants(t, "../metadata/model.go")
	require.Len(t, want, 9, "pkg/metadata.ImageType should declare nine roles; update this guard if that changed deliberately")

	raw, err := os.ReadFile("../../config/crd/bases/catalog.clustarr.io_movies.yaml")
	require.NoError(t, err)
	var crd apiextensionsv1.CustomResourceDefinition
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	require.NotEmpty(t, crd.Spec.Versions)

	schema := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
	for _, step := range []string{"status", "metadata", "images"} {
		next, ok := schema.Properties[step]
		require.True(t, ok, "movies CRD has no %q on the way to status.metadata.images", step)
		schema = &next
	}
	require.NotNil(t, schema.Items)
	typ, ok := schema.Items.Schema.Properties["type"]
	require.True(t, ok)

	var got []string
	for _, v := range typ.Enum {
		var s string
		require.NoError(t, yaml.Unmarshal(v.Raw, &s))
		got = append(got, s)
	}
	sort.Strings(got)
	require.Equal(t, want, got,
		"catalog ImageType's enum (api/catalog/v1alpha1/shared_types.go) must equal pkg/metadata.ImageType's constants")
}

// imageTypeConstants returns the sorted string values of every constant
// declared with type ImageType in the named file.
func imageTypeConstants(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, err)
	var out []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			id, ok := vs.Type.(*ast.Ident)
			if !ok || id.Name != "ImageType" {
				continue
			}
			for _, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				require.True(t, ok, "an ImageType constant is not a string literal")
				s, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}
