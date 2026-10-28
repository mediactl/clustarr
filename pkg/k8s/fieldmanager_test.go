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

package k8s

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"testing"
)

func TestFieldManagersAreTheOnesTheSpecLists(t *testing.T) {
	// The managers as built: the fold retired catalog-markers (F3.4) and
	// caption-worker (F5.3), ADR-0019 deleted grab-engine (A3.6, A9.1), and
	// the cold turkey deleted each outright rather than keeping it valid
	// (TestRetiredFieldManagersStayValid). clustarr-autoscale joined last
	// (W4.62); Wave U unbranded every value (U3).
	want := []string{
		"catalog", "catalog-series", "catalog-worker",
		"catalog-metadata", "catalog-grab", "catalog-fanout",
		"catalog-artwork", "catalog-classify", "catalog-imageset",
		"import", "import-worker",
		"index", "index-worker",
		"grab",
		"transcode", "transcode-pool",
		"caption",
		"clustarr-dlq-projector", "clustarr-ui", "clustarr-autoscale",
		"clustarr-livetv", "clustarr-livetv-channel", "clustarr-livetv-endpoint",
	}
	got := FieldManagers()
	if len(got) != len(want) {
		t.Fatalf("got %d field managers, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].String() != w {
			t.Errorf("manager %d = %q, want %q", i, got[i], w)
		}
		if !got[i].Valid() {
			t.Errorf("%q reported invalid", got[i])
		}
	}
	if FieldManager("").Validate() == nil {
		t.Error("the empty field manager was accepted")
	}
	if FieldManager("kubectl").Validate() == nil {
		t.Error("an unlisted field manager was accepted")
	}
}

// retiredNames are the managers the fold and ADR-0019 retired: the markers
// writer (F3.4), the subtitle fetch worker (F5.3) and the grab engines
// (A3.6), in their unbranded spelling (Wave U §2.5). The cold turkey
// (docs/runbooks/unify-cold-cutover.md) cleans their managedFields up by
// hand, so none of them is kept valid for a release.
var retiredNames = []FieldManager{"catalog-markers", "caption-worker", "grab-engine"}

// TestRetiredFieldManagersStayValid: under the cold turkey nothing stays
// valid once retired. RetiredFieldManagers is empty, and every name the fold
// and ADR-0019 retired is refused by Validate, so a writer left behind that
// still applies as one fails at its first write instead of reappearing in
// managedFields.
func TestRetiredFieldManagersStayValid(t *testing.T) {
	if got := RetiredFieldManagers(); len(got) != 0 {
		t.Errorf("retired managers = %v, want none: a retired name is deleted, not kept valid", got)
	}
	for _, m := range retiredNames {
		if slices.Contains(FieldManagers(), m) {
			t.Errorf("%s is still a live field manager", m)
		}
		if m.Valid() {
			t.Errorf("%s is still valid", m)
		}
		if m.Validate() == nil {
			t.Errorf("%s still validates", m)
		}
	}
}

// TestCaptionWorkerIsRetired: the subtitle fetch worker answers in
// clustarr-subtitles and writes no Kubernetes object (loop spec §6.7.5), so
// caption-worker is neither a live nor a retired manager, and no longer
// validates.
func TestCaptionWorkerIsRetired(t *testing.T) {
	const captionWorker = FieldManager("caption-worker")
	if slices.Contains(FieldManagers(), captionWorker) {
		t.Errorf("caption-worker is still a live field manager")
	}
	if slices.Contains(RetiredFieldManagers(), captionWorker) {
		t.Errorf("caption-worker is kept among the retired field managers; the cold turkey deletes it")
	}
	if captionWorker.Validate() == nil {
		t.Errorf("caption-worker still validates: the fetch worker writes nothing (loop spec §6.7.5)")
	}
	if !slices.Contains(FieldManagers(), ManagerCaption) {
		t.Errorf("the caption controller's manager %q is gone with its worker's", ManagerCaption)
	}
}

// brandStems are the services whose *arr names Wave U retired; brandRE
// matches each stem's branded spelling and the transcoder's old name. It is
// spelled so it does not match itself, as hack/brand-gate.sh's pattern is.
var (
	brandStems = []string{"catalog", "import", "index", "grab", "caption", "segment"}
	brandRE    = regexp.MustCompile(`(?i)(catalog|import|index|grab|caption|segment)arr|squas[h]`)
)

// TestFieldManagerValuesAreUnbranded: every manager name is stored in the
// managedFields of every object it writes, so a branded value would outlive
// the rename on every object in the cluster (Wave U spec §2.5). No live or
// retired manager, and not the default field owner, names a brand.
func TestFieldManagerValuesAreUnbranded(t *testing.T) {
	for _, stem := range brandStems {
		if !brandRE.MatchString(stem + "arr") {
			t.Fatalf("brandRE misses the %s brand: the check below would pass vacuously", stem)
		}
	}
	if !brandRE.MatchString("squas" + "h-pool") {
		t.Fatal("brandRE misses the transcoder's old name")
	}

	names := append(append([]FieldManager{}, FieldManagers()...), RetiredFieldManagers()...)
	names = append(names, FieldManager(DefaultFieldOwner))
	for _, m := range names {
		if brandRE.MatchString(m.String()) {
			t.Errorf("field manager %q names a retired brand (Wave U §2.5)", m)
		}
	}
}

// TestEveryDeclaredFieldManagerIsListed: a manager declared in
// fieldmanager.go but missing from FieldManagers fails Validate at its first
// write. catalog-imageset was (2026-10-22 to 2026-10-28): the image-set
// stage's every status apply failed "is not a field manager listed in the
// design spec", unseen until a mediux provider existed on talos.
func TestEveryDeclaredFieldManagerIsListed(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "fieldmanager.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, spec := range g.Specs {
			vs := spec.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "FieldManager" {
				continue
			}
			for i, name := range vs.Names {
				lit := vs.Values[i].(*ast.BasicLit)
				m := FieldManager(lit.Value[1 : len(lit.Value)-1])
				n++
				if !m.Valid() {
					t.Errorf("%s (%q) is declared but not in FieldManagers: every write under it fails", name.Name, m)
				}
			}
		}
	}
	if n < len(FieldManagers()) {
		t.Errorf("found %d declared managers, fewer than the %d listed: the parse missed some", n, len(FieldManagers()))
	}
}
