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

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// rbacServiceDirs must match the Makefile's RBAC_DIRS: the directories
// controller-gen is pointed at when it generates config/rbac/role.yaml.
var rbacServiceDirs = []string{
	"catalogarr", "importarr", "indexarr", "grabarr", "squasharr", "captionarr",
}

// TestRBACMarkersArePackageLevel catches the silent failure that let four
// controllers ship with no permissions at all.
//
// controller-gen registers +kubebuilder:rbac as a PACKAGE-level marker. It
// therefore only reads it from a comment group that is not attached to a
// declaration -- a blank line between the markers and the `func` or `type`
// below them. Attach the block to the declaration and it becomes that
// declaration's doc comment, controller-gen collects nothing, exits 0 and
// writes a role that quietly omits every rule in the file.
//
// That is not hypothetical. catalogarr's rootfolder, qualityprofile,
// delayprofile and metadataprovider controllers all wrote their markers
// directly above SetupWithManager, so none of their permissions -- including
// every /status write they make -- had ever reached config/rbac/role.yaml.
// envtest does not enforce RBAC, so their suites passed; the failure would
// have been four controllers unable to write status on a real cluster.
func TestRBACMarkersArePackageLevel(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	var checked int
	for _, dir := range rbacServiceDirs {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
			if perr != nil {
				return perr
			}

			// Every comment group that is some declaration's doc comment.
			// The file's own package doc is fine -- that IS package level.
			attached := map[*ast.CommentGroup]ast.Node{}
			ast.Inspect(file, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.GenDecl:
					if v.Doc != nil {
						attached[v.Doc] = v
					}
				case *ast.FuncDecl:
					if v.Doc != nil {
						attached[v.Doc] = v
					}
				case *ast.TypeSpec:
					if v.Doc != nil {
						attached[v.Doc] = v
					}
				case *ast.ValueSpec:
					if v.Doc != nil {
						attached[v.Doc] = v
					}
				case *ast.Field:
					if v.Doc != nil {
						attached[v.Doc] = v
					}
				}
				return true
			})

			for _, group := range file.Comments {
				if group == file.Doc || !hasRBACMarker(group) {
					continue
				}
				checked++
				if _, bad := attached[group]; bad {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s:%d: the +kubebuilder:rbac block is a declaration's doc comment, "+
						"so controller-gen silently ignores every marker in it. "+
						"Put a blank line between the markers and the declaration.",
						rel, fset.Position(group.Pos()).Line)
				}
			}
			return nil
		})
		require.NoError(t, err)
	}

	require.Positive(t, checked, "no RBAC markers were found at all; this guard is not looking where it thinks it is")
}

// hasRBACMarker reports whether any LINE of the group is an RBAC marker.
// Matching the whole group's text would also match prose that merely mentions
// the marker, which is how this very file's explanatory comments first tripped
// the guard.
func hasRBACMarker(group *ast.CommentGroup) bool {
	for _, line := range strings.Split(group.Text(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "+kubebuilder:rbac:") {
			return true
		}
	}
	return false
}

// TestGeneratedRoleCoversEveryStatusWriter is a second, coarser net over the
// same failure: every kind whose controller writes status must have its
// /status subresource in the generated Role.
//
// The marker-placement guard above catches the mechanism; this one catches
// the outcome, including the ways a marker can be missing rather than merely
// misplaced. Both are cheap and neither needs a cluster.
func TestGeneratedRoleCoversEveryStatusWriter(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(root, "config", "rbac", "role.yaml"))
	require.NoError(t, err)

	var role struct {
		Rules []struct {
			APIGroups []string `json:"apiGroups"`
			Resources []string `json:"resources"`
			Verbs     []string `json:"verbs"`
		} `json:"rules"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &role))

	granted := map[string]bool{}
	for _, rule := range role.Rules {
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				granted[group+"/"+resource] = true
			}
		}
	}

	// Every kind a Phase C controller or worker is the single writer of.
	for _, want := range []string{
		"catalog.clustarr.io/movies/status",
		"catalog.clustarr.io/series/status",
		"catalog.clustarr.io/episodes/status",
		"catalog.clustarr.io/mediafiles/status",
		"catalog.clustarr.io/rootfolders/status",
		"catalog.clustarr.io/qualityprofiles/status",
		"catalog.clustarr.io/delayprofiles/status",
		"catalog.clustarr.io/metadataproviders/status",
		"catalog.clustarr.io/searches/status",
		"catalog.clustarr.io/libraryscans/status",
		"catalog.clustarr.io/importexclusions/status",
	} {
		require.True(t, granted[want],
			"config/rbac/role.yaml does not grant %s; its controller would be denied every status write "+
				"on a real cluster (envtest does not enforce RBAC, so no suite can see this). "+
				"Run `make manifests` and check the marker is package level.", want)
	}

	// Both Event API groups: the tree uses record.EventRecorder (core/v1) in
	// some controllers and tools/events (events.k8s.io/v1) in others.
	require.True(t, granted["/events"], "the core Events group is not granted")
	require.True(t, granted["events.k8s.io/events"], "the events.k8s.io Events group is not granted")
}
