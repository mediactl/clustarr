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
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// rbacServiceDirs reads RBAC_DIRS out of the Makefile -- the list controller-gen
// is actually pointed at -- rather than restating it. A second copy here could
// drift from the generator's real input, which is precisely the class of bug
// these guards exist to catch.
func rbacServiceDirs(t *testing.T, root string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err)

	for _, line := range strings.Split(string(raw), "\n") {
		_, value, found := strings.Cut(line, "RBAC_DIRS :=")
		if !found {
			continue
		}
		dirs := strings.Fields(value)
		require.NotEmpty(t, dirs, "the Makefile's RBAC_DIRS is empty")
		return dirs
	}
	t.Fatal("no RBAC_DIRS assignment in the Makefile; this guard no longer knows what controller-gen is given")
	return nil
}

// TestEveryDirectoryWithRBACMarkersIsGenerated closes the hole that hid the
// importarr defect: importarr's controllers carried correct markers, and
// RBAC_DIRS simply did not list importarr, so controller-gen was never pointed
// at them and not one of its rules reached config/rbac/role.yaml.
//
// A guard that restated RBAC_DIRS would not have caught it -- the two lists
// would have been wrong together. So this one DISCOVERS: any top-level
// directory containing a +kubebuilder:rbac marker anywhere beneath it must
// appear in RBAC_DIRS. A new service is then covered the moment it grows its
// first marker, with no list to remember to update.
func TestEveryDirectoryWithRBACMarkersIsGenerated(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	generated := map[string]bool{}
	for _, dir := range rbacServiceDirs(t, root) {
		generated[dir] = true
	}

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		// No directory allowlist on purpose. The rule is simply "carries a
		// marker implies generated", so api/, pkg/, cmd/, test/ and hack/ are
		// skipped by having no markers rather than by being named here -- and
		// the day one of them grows one, it is flagged instead of excused.
		if !markerDirExists(t, filepath.Join(root, name)) {
			continue
		}
		require.True(t, generated[name],
			"%s/ carries +kubebuilder:rbac markers but is not in the Makefile's RBAC_DIRS, "+
				"so controller-gen never sees them and every rule in it is silently absent "+
				"from config/rbac/role.yaml -- exactly the defect importarr shipped with", name)
	}
}

// markerDirExists reports whether any non-test Go file under dir carries an
// RBAC marker.
func markerDirExists(t *testing.T, dir string) bool {
	t.Helper()
	var found bool
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return nil //nolint:nilerr // an unreadable subtree is not this guard's business
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if bytes.Contains(raw, []byte("+kubebuilder:rbac:")) {
			found = true
		}
		return nil
	})
	require.NoError(t, err)
	return found
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
	for _, dir := range rbacServiceDirs(t, root) {
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

	// Derived: every `<resource>/status` any marker in the tree grants must
	// be in the generated Role. This is the check that actually self-
	// maintains, and it is NOT circular with the generator: it reads the
	// marker TEXT from source, while controller-gen reads only the markers it
	// manages to collect. A marker that exists but is never collected -- the
	// package-level placement defect, or a directory missing from RBAC_DIRS --
	// fails here even though `make manifests` exits 0.
	for want := range statusGrantsInSource(t, root) {
		require.True(t, granted[want],
			"config/rbac/role.yaml does not grant %s, although a +kubebuilder:rbac marker in the tree "+
				"asks for it. controller-gen did not collect that marker: check it is package level "+
				"(a blank line above the declaration) and that its directory is in RBAC_DIRS.", want)
	}

	// A hardcoded floor, because the derived check above cannot see a marker
	// that was never written. Every kind a Phase C controller or worker is the
	// single writer of:
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

// statusGrantsInSource returns every "<group>/<resource>/status" an RBAC
// marker anywhere in the generated directories asks for, as the same
// "<group>/<resource>" keys TestGeneratedRoleCoversEveryStatusWriter builds
// from the role.
//
// It parses the marker text rather than using controller-gen's own parser
// precisely so it can disagree with it: a marker controller-gen silently
// declines to collect is still visible here.
func statusGrantsInSource(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, dir := range rbacServiceDirs(t, root) {
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
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for _, line := range strings.Split(string(raw), "\n") {
				_, marker, found := strings.Cut(line, "+kubebuilder:rbac:")
				if !found {
					continue
				}
				group, resources := markerGroupsAndResources(marker)
				for _, resource := range resources {
					if !strings.HasSuffix(resource, "/status") {
						continue
					}
					out[group+"/"+resource] = true
				}
			}
			return nil
		})
		require.NoError(t, err)
	}
	require.NotEmpty(t, out, "no /status grants were found in any marker; this guard is looking in the wrong place")
	return out
}

// markerGroupsAndResources pulls the first groups= value and the resources=
// list out of one marker body. Only single-group markers are read: every
// /status marker in this tree names exactly one group, and a multi-group one
// would need the cross product, which is more machinery than the check earns.
func markerGroupsAndResources(marker string) (string, []string) {
	var group string
	var resources []string
	for _, field := range strings.Split(strings.TrimSpace(marker), ",") {
		key, value, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "groups":
			group = strings.Trim(strings.TrimSpace(value), `"`)
		case "resources":
			resources = strings.Split(strings.TrimSpace(value), ";")
		}
	}
	return group, resources
}

// TestEveryCRDKindAControllerTouchesHasAnRBACMarker closes the last hole the
// two guards above cannot see.
//
// TestGeneratedRoleCoversEveryStatusWriter checks that every marker in the
// source reached the generated Role, and TestRBACMarkersArePackageLevel
// checks that markers are placed where controller-gen will collect them.
// Neither can see a marker that was never written at all: a controller that
// Gets a kind nobody granted has no marker to compare against anything.
// envtest does not enforce RBAC, so no suite in this repo can catch it
// either -- the reconcile passes locally and fails with Forbidden on the
// first real cluster.
//
// That is not hypothetical. mediafile's reconciler read TranscodeProfile
// (to render §4.5's "<name>@<hash>" profile tag) with markers granting
// transcodejobs and nothing else, and TestTranscodeJobWatchTriggersReconcile
// exercised the path and passed. On a real cluster the first successful
// TranscodeJob would have started a lazy informer, been denied, and failed
// the reconcile on every retry, so the transcode swap could never be
// incorporated.
//
// The rule is "touches a Clustarr CRD kind implies a marker names its
// resource, in the same service directory". Resolution is exact rather than
// by string-matching: the kind comes from the selector expression, its API
// group from the import path the selector's alias resolves to, and its
// plural from the generated CRD itself.
func TestEveryCRDKindAControllerTouchesHasAnRBACMarker(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	plurals := crdPlurals(t, root)
	require.NotEmpty(t, plurals, "no CRDs were read; run `make manifests`")

	for _, dir := range rbacServiceDirs(t, root) {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		granted := markerResources(t, base)
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
			groups := apiGroupAliases(file)
			if len(groups) == 0 {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				group, ok := groups[ident.Name]
				if !ok {
					return true
				}
				kind := strings.TrimSuffix(sel.Sel.Name, "List")
				plural, ok := plurals[group+"/"+kind]
				if !ok {
					return true // a spec/enum/helper type, not a root kind
				}
				if !granted[group+"/"+plural] {
					t.Errorf("%s:%d: %s touches %s.%s, but no +kubebuilder:rbac marker under %s/ "+
						"grants %s in group %s. envtest does not enforce RBAC, so this only fails on a "+
						"real cluster, as a Forbidden on the first lazy informer or Get.",
						rel, fset.Position(sel.Pos()).Line, dir, ident.Name, sel.Sel.Name, dir, plural, group)
					granted[group+"/"+plural] = true // report each gap once
				}
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}
}

// crdPlurals maps "<group>/<Kind>" to the resource plural, read from the
// generated CRDs so the guard cannot disagree with what is installed. It is
// deliberately not a lowercase-and-append-s rule: "series" is its own plural.
func crdPlurals(t *testing.T, root string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "config", "crd", "bases"))
	require.NoError(t, err)

	out := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, "config", "crd", "bases", entry.Name()))
		require.NoError(t, err)
		var crd struct {
			Spec struct {
				Group string `json:"group"`
				Names struct {
					Kind   string `json:"kind"`
					Plural string `json:"plural"`
				} `json:"names"`
			} `json:"spec"`
		}
		require.NoError(t, yaml.Unmarshal(raw, &crd))
		if crd.Spec.Group == "" || crd.Spec.Names.Kind == "" {
			continue
		}
		out[crd.Spec.Group+"/"+crd.Spec.Names.Kind] = crd.Spec.Names.Plural
	}
	return out
}

// apiGroupAliases maps each import alias for an api/<group>/v1alpha1 package
// in file to its API group, so a selector expression can be resolved to a
// group without guessing from the alias's spelling.
func apiGroupAliases(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		rest, ok := strings.CutPrefix(path, "github.com/mediactl/clustarr/api/")
		if !ok {
			continue
		}
		group, _, ok := strings.Cut(rest, "/")
		if !ok || group == "common" || group == "applyconfiguration" {
			continue
		}
		name := group + "v1alpha1"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		out[name] = group + ".clustarr.io"
	}
	return out
}

// markerResources returns every "<group>/<resource>" the RBAC markers under
// dir grant, expanding the "resources=a;b;c" form and dropping the
// "/status" and "/finalizers" subresource suffixes -- a Get on the main
// resource is what this guard is about.
func markerResources(t *testing.T, dir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			_, marker, found := strings.Cut(line, "+kubebuilder:rbac:")
			if !found {
				continue
			}
			var groups, resources []string
			for _, field := range strings.Split(strings.TrimSpace(marker), ",") {
				key, value, ok := strings.Cut(field, "=")
				if !ok {
					continue
				}
				value = strings.Trim(value, `"`)
				switch key {
				case "groups":
					groups = strings.Split(value, ";")
				case "resources":
					resources = strings.Split(value, ";")
				}
			}
			for _, g := range groups {
				for _, r := range resources {
					out[strings.Trim(g, `"`)+"/"+strings.Split(r, "/")[0]] = true
				}
			}
		}
		return nil
	})
	require.NoError(t, err)
	return out
}

// TestEveryBuiltinKindAControllerTouchesHasAnRBACMarker is the other half of
// TestEveryCRDKindAControllerTouchesHasAnRBACMarker, and until now it was
// missing entirely.
//
// That guard resolves a kind's plural through crdPlurals, which is built from
// config/crd/bases -- so ConfigMap, Secret, Pod, Lease, Job and every other
// built-in is STRUCTURALLY outside its reach. It is not that they were
// checked and passed; there was nothing to check them against. Demonstrated:
// an unmarked corev1.ConfigMap read added to a controller fired no guard at
// all, and envtest does not enforce RBAC, so the reconcile passes locally and
// fails with Forbidden on the first real cluster.
//
// The universe of kinds is taken from k8s.MustNewScheme rather than from a
// table written here. That is exact rather than convenient: a type a Clustarr
// manager's scheme does not register cannot be read through its client at
// all, and the scheme is also what tells a root kind (corev1.Secret) apart
// from a helper struct or a constant in the same package
// (corev1.LocalObjectReference, corev1.EventTypeWarning), which no naming
// heuristic does reliably. Plurals come from meta.UnsafeGuessKindToResource,
// the same derivation the API machinery uses.
func TestEveryBuiltinKindAControllerTouchesHasAnRBACMarker(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	known := k8s.MustNewScheme().AllKnownTypes()
	require.NotEmpty(t, known, "the scheme registered nothing; this guard has no universe to check against")

	var checked int
	for _, dir := range rbacServiceDirs(t, root) {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		granted := markerResources(t, base)
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
			groups := builtinGroupAliases(file)
			if len(groups) == 0 {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				gv, ok := groups[ident.Name]
				if !ok {
					return true
				}
				gvk := gv.WithKind(strings.TrimSuffix(sel.Sel.Name, "List"))
				if _, isRootKind := known[gvk]; !isRootKind {
					return true // a helper struct, an enum or a constant
				}
				checked++
				gvr, _ := meta.UnsafeGuessKindToResource(gvk)
				resource := gvr.Resource
				if !granted[gvk.Group+"/"+resource] {
					group := gvk.Group
					if group == "" {
						group = `"" (core)`
					}
					t.Errorf("%s:%d: %s touches %s.%s, but no +kubebuilder:rbac marker under %s/ "+
						"grants %s in group %s. envtest does not enforce RBAC, so this only fails on a "+
						"real cluster, as a Forbidden on the first Get or lazy informer.",
						rel, fset.Position(sel.Pos()).Line, dir, ident.Name, sel.Sel.Name, dir, resource, group)
					granted[gvk.Group+"/"+resource] = true // report each gap once
				}
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}
	require.Positive(t, checked,
		"no built-in kind was found in any service directory; this guard is not looking where it "+
			"thinks it is (did the k8s.io/api import aliases change shape?)")
}

// builtinGroupAliases maps each import alias for a k8s.io/api/<group>/<version>
// package in file to its GroupVersion, so a selector expression resolves to a
// GVK without guessing from the alias's spelling. "core" is the empty group.
func builtinGroupAliases(file *ast.File) map[string]schema.GroupVersion {
	out := map[string]schema.GroupVersion{}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		rest, ok := strings.CutPrefix(path, "k8s.io/api/")
		if !ok {
			continue
		}
		group, version, ok := strings.Cut(rest, "/")
		if !ok || strings.Contains(version, "/") {
			continue
		}
		// k8s.io/api/<group>/<version>'s package name is the version, so an
		// unaliased import is `v1`. Every one in this tree is aliased.
		name := version
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if group == "core" {
			group = ""
		}
		out[name] = schema.GroupVersion{Group: group, Version: version}
	}
	return out
}
