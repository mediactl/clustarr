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
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// rbacRole is one ClusterRole `make manifests` generates: its name in the
// Makefile's RBAC_ROLES and the package patterns RBAC_PATHS_<name> hands
// controller-gen. Since task X14 there is one per identity (design §10), not
// one union role for every service.
type rbacRole struct {
	name  string
	paths []string
}

// file is the generated role's file under config/rbac.
func (r rbacRole) file() string { return strings.ReplaceAll(r.name, "-", "_") + "_role.yaml" }

// rbacRoles reads RBAC_ROLES and each RBAC_PATHS_<role> out of the Makefile
// -- what controller-gen is actually pointed at -- rather than restating
// them. A second copy here could drift from the generator's real input,
// which is precisely the class of bug these guards exist to catch.
func rbacRoles(t *testing.T, root string) []rbacRole {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err)

	vars := map[string][]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		name, value, found := strings.Cut(line, ":=")
		if !found {
			continue
		}
		vars[strings.TrimSpace(name)] = strings.Fields(value)
	}
	names := vars["RBAC_ROLES"]
	require.NotEmpty(t, names, "the Makefile has no RBAC_ROLES; this guard no longer knows what controller-gen is given")
	out := make([]rbacRole, 0, len(names))
	for _, name := range names {
		paths := vars["RBAC_PATHS_"+name]
		require.NotEmpty(t, paths, "RBAC_ROLES names %q but the Makefile has no RBAC_PATHS_%s", name, name)
		out = append(out, rbacRole{name: name, paths: paths})
	}
	return out
}

// patternCovers reports whether a controller-gen paths= pattern covers the
// package in dir (slash-separated, relative to the repo root): "./x/..." is
// x and everything beneath it, "./x" is x alone.
func patternCovers(pattern, dir string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	if base, ok := strings.CutSuffix(pattern, "/..."); ok {
		return dir == base || strings.HasPrefix(dir, base+"/")
	}
	return dir == pattern
}

// goSources returns the non-test .go files directly in dir.
func goSources(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

// roleSources returns every non-test .go file in the packages r's patterns
// cover: exactly what controller-gen reads markers from for r.
func roleSources(t *testing.T, root string, r rbacRole) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && strings.HasPrefix(d.Name(), ".") {
			return fs.SkipDir
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, pattern := range r.paths {
			if patternCovers(pattern, rel) {
				out = append(out, goSources(t, path)...)
				break
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, out, "RBAC_PATHS_%s %v covers no Go source", r.name, r.paths)
	return out
}

// hasMarkerLine reports whether raw has a +kubebuilder:rbac marker line.
func hasMarkerLine(raw []byte) bool {
	return bytes.Contains(raw, []byte("+kubebuilder:rbac:"))
}

// TestEveryPackageWithRBACMarkersIsInARole closes the hole that hid the
// importarr defect: importarr's controllers carried correct markers, and the
// Makefile simply did not point controller-gen at them, so not one of their
// rules reached the generated Role.
//
// A guard that restated the Makefile would not have caught it -- the two
// lists would have been wrong together. So this one DISCOVERS: any package
// anywhere in the tree whose non-test source carries a +kubebuilder:rbac
// marker must be covered by some role's RBAC_PATHS. A new package is then
// covered the moment it grows its first marker, and since the X14 split the
// same check catches a package left out of every per-service role -- say a
// new grabarr package that is neither the controller's nor the engines'.
func TestEveryPackageWithRBACMarkersIsInARole(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	roles := rbacRoles(t, root)

	var found int
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && strings.HasPrefix(d.Name(), ".") {
			return fs.SkipDir
		}
		marked := false
		for _, src := range goSources(t, path) {
			raw, rerr := os.ReadFile(src)
			require.NoError(t, rerr)
			if hasMarkerLine(raw) {
				marked = true
				break
			}
		}
		if !marked {
			return nil
		}
		found++
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, r := range roles {
			for _, pattern := range r.paths {
				if patternCovers(pattern, rel) {
					return nil
				}
			}
		}
		t.Errorf("%s/ carries +kubebuilder:rbac markers, but no RBAC_PATHS_<role> in the Makefile covers it, "+
			"so controller-gen never sees them and every rule in it is silently absent from every "+
			"generated role -- exactly the defect importarr shipped with. Add it to the role of the "+
			"identity whose process runs it.", rel)
		return nil
	})
	require.NoError(t, err)
	require.Positive(t, found, "no package carries an RBAC marker; this guard is not looking where it thinks it is")
}

// TestEveryGeneratedRoleMatchesItsMarkers holds each generated role to the
// marker TEXT in its own packages, grant for grant: a marker added and never
// regenerated -- a process Getting something its pod is Forbidden to Get,
// which no envtest can see -- fails here, and so does a hand edit to a role,
// and so does a grant that reaches a role from a package that is not its
// own. It reads the markers without controller-gen, so a marker
// controller-gen silently declines to collect (the package-level placement
// defect) fails too. Until X14 this held only the transcode worker's role.
func TestEveryGeneratedRoleMatchesItsMarkers(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	for _, r := range rbacRoles(t, root) {
		t.Run(r.name, func(t *testing.T) {
			want := map[rbacGrant]bool{}
			for _, src := range roleSources(t, root, r) {
				raw, err := os.ReadFile(src)
				require.NoError(t, err)
				for _, line := range strings.Split(string(raw), "\n") {
					_, marker, found := strings.Cut(line, "+kubebuilder:rbac:")
					if !found {
						continue
					}
					fields := map[string][]string{}
					for _, field := range strings.Split(strings.TrimSpace(marker), ",") {
						if k, v, ok := strings.Cut(field, "="); ok {
							fields[strings.TrimSpace(k)] = strings.Split(strings.Trim(strings.TrimSpace(v), `"`), ";")
						}
					}
					for _, g := range fields["groups"] {
						for _, res := range fields["resources"] {
							for _, v := range fields["verbs"] {
								want[rbacGrant{g, res, v}] = true
							}
						}
					}
				}
			}
			require.NotEmpty(t, want, "no +kubebuilder:rbac markers in %v; this guard is looking in the wrong place", r.paths)

			got := grantsOf(readRole(t, root, r.file()).Rules)
			require.Equal(t, sortedGrants(want), sortedGrants(got),
				"config/rbac/%s no longer matches the +kubebuilder:rbac markers in %v.\n"+
					"Run `make manifests`, then copy its rules into charts/clustarr/templates/rbac.yaml "+
					"between its BEGIN/END sentinels.", r.file(), r.paths)
		})
	}
}

// readRole reads one generated ClusterRole from config/rbac.
func readRole(t *testing.T, root, file string) rbacv1.ClusterRole {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "config", "rbac", file))
	require.NoError(t, err)
	var role rbacv1.ClusterRole
	require.NoError(t, yaml.Unmarshal(raw, &role))
	require.NotEmpty(t, role.Rules, "config/rbac/%s has no rules", file)
	return role
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
	seen := map[string]bool{}
	for _, r := range rbacRoles(t, root) {
		for _, path := range roleSources(t, root, r) {
			if seen[path] {
				continue
			}
			seen[path] = true
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
			require.NoError(t, perr)

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
		}
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

// TestGeneratedRolesCoverEveryStatusWriter is a floor under
// TestEveryGeneratedRoleMatchesItsMarkers: that test proves each role is
// exactly its markers, so it cannot see a marker that was never written.
// This one names every kind whose status a Phase C controller or worker is
// the single writer of, and requires some generated role to grant it.
func TestGeneratedRolesCoverEveryStatusWriter(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	granted := map[string]bool{}
	coreEvents := map[string]bool{}
	for _, r := range rbacRoles(t, root) {
		for _, rule := range readRole(t, root, r.file()).Rules {
			for _, group := range rule.APIGroups {
				for _, resource := range rule.Resources {
					granted[group+"/"+resource] = true
					if group == "" && resource == "events" {
						coreEvents[r.file()] = true
					}
				}
			}
		}
	}

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
			"no generated role grants %s; its controller would be denied every status write "+
				"on a real cluster (envtest does not enforce RBAC by itself, so no suite can see this). "+
				"Run `make manifests` and check the marker is package level.", want)
	}

	// ONE Event API group. Every controller in this tree takes a
	// k8s.io/client-go/tools/events.EventRecorder from mgr.GetEventRecorder
	// and writes events.k8s.io/v1; the deprecated mgr.GetEventRecorderFor,
	// which writes core/v1, is gone. The negative half is the real guard:
	// the core rule reappearing means a controller went back to the old
	// recorder, or a groups="" marker outlived the recorder it described.
	require.True(t, granted["events.k8s.io/events"], "the events.k8s.io Events group is not granted")
	require.Empty(t, coreEvents,
		"these generated roles grant events in the CORE group, but nothing in this tree writes "+
			"core/v1 Events any more. Either a controller is back on the deprecated "+
			`mgr.GetEventRecorderFor, or a stale groups="" events marker survived the migration. `+
			"Neither shows up in any other test: envtest does not enforce RBAC.")
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
// resource, in the packages of the same generated role" -- since the X14
// split, a grant another service's markers happen to carry is no longer in
// this service's role, so it no longer counts. Resolution is exact rather than
// by string-matching: the kind comes from the selector expression, its API
// group from the import path the selector's alias resolves to, and its
// plural from the generated CRD itself.
func TestEveryCRDKindAControllerTouchesHasAnRBACMarker(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	plurals := crdPlurals(t, root)
	require.NotEmpty(t, plurals, "no CRDs were read; run `make manifests`")

	for _, r := range rbacRoles(t, root) {
		dir := r.name + " role " + strings.Join(r.paths, " ")
		sources := roleSources(t, root, r)
		granted := markerResources(t, sources)
		for _, path := range sources {
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
			require.NoError(t, perr)
			groups := apiGroupAliases(file)
			if len(groups) == 0 {
				continue
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
					t.Errorf("%s:%d: %s touches %s.%s, but no +kubebuilder:rbac marker in the %s "+
						"grants %s in group %s. envtest does not enforce RBAC, so this only fails on a "+
						"real cluster, as a Forbidden on the first lazy informer or Get.",
						rel, fset.Position(sel.Pos()).Line, dir, ident.Name, sel.Sel.Name, dir, plural, group)
					granted[group+"/"+plural] = true // report each gap once
				}
				return true
			})
		}
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

// markerResources returns every "<group>/<resource>" the RBAC markers in
// sources grant, expanding the "resources=a;b;c" form and dropping the
// "/status" and "/finalizers" subresource suffixes -- a Get on the main
// resource is what this guard is about.
func markerResources(t *testing.T, sources []string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, path := range sources {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
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
	}
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
	for _, r := range rbacRoles(t, root) {
		dir := r.name + " role " + strings.Join(r.paths, " ")
		sources := roleSources(t, root, r)
		granted := markerResources(t, sources)
		for _, path := range sources {
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
			require.NoError(t, perr)
			groups := builtinGroupAliases(file)
			if len(groups) == 0 {
				continue
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
					t.Errorf("%s:%d: %s touches %s.%s, but no +kubebuilder:rbac marker in the %s "+
						"grants %s in group %s. envtest does not enforce RBAC, so this only fails on a "+
						"real cluster, as a Forbidden on the first Get or lazy informer.",
						rel, fset.Position(sel.Pos()).Line, dir, ident.Name, sel.Sel.Name, dir, resource, group)
					granted[gvk.Group+"/"+resource] = true // report each gap once
				}
				return true
			})
		}
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
