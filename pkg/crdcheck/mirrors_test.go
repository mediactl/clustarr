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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// This file holds the guards for Go values that mirror a CRD marker, so the
// two cannot drift: each reads the marker's effect from the generated CRD in
// config/crd/bases rather than restating it.

// crdSchemaAt loads a generated CRD and walks its first version's schema
// down the named properties.
func crdSchemaAt(t *testing.T, file string, path ...string) *apiextensionsv1.JSONSchemaProps {
	t.Helper()
	raw, err := os.ReadFile("../../config/crd/bases/" + file)
	require.NoError(t, err)
	var crd apiextensionsv1.CustomResourceDefinition
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	require.NotEmpty(t, crd.Spec.Versions)
	schema := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
	for _, step := range path {
		next, ok := schema.Properties[step]
		require.True(t, ok, "%s has no %q on the way to %v", file, step, path)
		schema = &next
	}
	return schema
}

// TestMaxAlsoOnMatchesTheCRD holds commonv1.MaxAlsoOn, the cap a merge
// truncates ReleaseInfo.AlsoOn to, equal to the field's MaxItems, checked on
// both CRDs that carry a ReleaseInfo.
func TestMaxAlsoOnMatchesTheCRD(t *testing.T) {
	onDownload := crdSchemaAt(t, "download.clustarr.io_downloads.yaml", "spec", "release", "alsoOn")
	results := crdSchemaAt(t, "catalog.clustarr.io_searches.yaml", "status", "results")
	require.NotNil(t, results.Items)
	onSearch, ok := results.Items.Schema.Properties["alsoOn"]
	require.True(t, ok, "status.results[] has no alsoOn")

	for where, schema := range map[string]*apiextensionsv1.JSONSchemaProps{
		"Download spec.release.alsoOn":   onDownload,
		"Search status.results[].alsoOn": &onSearch,
	} {
		require.NotNil(t, schema.MaxItems, "%s is uncapped", where)
		require.Equal(t, int64(commonv1.MaxAlsoOn), *schema.MaxItems, "%s: MaxAlsoOn and the MaxItems marker disagree", where)
	}
}

// TestImageTypeEnumMatchesPkgMetadata holds catalog's Image.type enum to
// exactly pkg/metadata.ImageType's constants (gap-fix X1 item 5). The CRD used
// to accept three of the nine roles, so the metadata patcher dropped every
// banner, clearart, thumb, screenshot, disc and headshot a provider returned.
// Both sides are read from source -- the constants from pkg/metadata's AST,
// the enum from the generated CRD -- so neither list is restated here.
func TestImageTypeEnumMatchesPkgMetadata(t *testing.T) {
	want := imageTypeConstants(t, "../metadata/model.go")
	require.Len(t, want, 9, "pkg/metadata.ImageType should declare nine roles; update this guard if that changed deliberately")

	schema := crdSchemaAt(t, "catalog.clustarr.io_movies.yaml", "status", "metadata", "images")
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

// TestRatingSourceEnumMatchesPkgMetadata is
// TestImageTypeEnumMatchesPkgMetadata's counterpart for ratings (task C1):
// pkg/metadata.RatingSource* (model.go) are deliberately plain, untyped
// strings, not a named type the way ImageType is (RatingsProvider.Ratings
// returns pkg/metadata.Ratings, keyed by Rating.Source, itself a plain
// string field) -- so this reads the const block by name prefix
// ("RatingSource") rather than by an *ast.Ident type annotation
// imageTypeConstants relies on, which an untyped string constant has none
// of. Both sides are still read from source; neither list is restated
// here.
func TestRatingSourceEnumMatchesPkgMetadata(t *testing.T) {
	want := ratingSourceConstants(t, "../metadata/model.go")
	require.Len(t, want, 7, "pkg/metadata should declare seven RatingSource* constants; update this guard if that changed deliberately")

	schema := crdSchemaAt(t, "catalog.clustarr.io_movies.yaml", "status", "metadata", "ratings")
	require.NotNil(t, schema.Items)
	source, ok := schema.Items.Schema.Properties["source"]
	require.True(t, ok)

	var got []string
	for _, v := range source.Enum {
		var s string
		require.NoError(t, yaml.Unmarshal(v.Raw, &s))
		got = append(got, s)
	}
	sort.Strings(got)
	require.Equal(t, want, got,
		"catalog RatingSource's enum (api/catalog/v1alpha1/shared_types.go) must equal pkg/metadata's RatingSource* constants")
}

// ratingSourceConstants returns the sorted string values of every constant
// declared in the named file whose identifier starts with "RatingSource".
func ratingSourceConstants(t *testing.T, path string) []string {
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
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "RatingSource") {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				require.True(t, ok, "%s is not a string literal", name.Name)
				s, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

// TestMaxTranscodeProfileLengthMatchesTheCRD holds
// pkg/mediainfo.MaxTranscodeProfileLength, the longest CLUSTARR_PROFILE tag
// the probe keeps, equal to MediaInfo.transcodeProfile's MaxLength on both
// CRDs that carry a MediaInfo. A marker lowered below the Go bound would have
// the apiserver reject a probed file's whole status apply.
func TestMaxTranscodeProfileLengthMatchesTheCRD(t *testing.T) {
	for where, schema := range map[string]*apiextensionsv1.JSONSchemaProps{
		"MediaFile status.mediaInfo.transcodeProfile": crdSchemaAt(t, "catalog.clustarr.io_mediafiles.yaml",
			"status", "mediaInfo", "transcodeProfile"),
		"TranscodeJob status.result.mediaInfo.transcodeProfile": crdSchemaAt(t, "transcode.clustarr.io_transcodejobs.yaml",
			"status", "result", "mediaInfo", "transcodeProfile"),
	} {
		require.NotNil(t, schema.MaxLength, "%s is unbounded", where)
		require.Equal(t, int64(mediainfo.MaxTranscodeProfileLength), *schema.MaxLength,
			"%s: MaxTranscodeProfileLength and the MaxLength marker disagree", where)
	}
}
