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

package delayprofile

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

func profile(name string, order int32, tags ...string) catalogv1alpha1.DelayProfile {
	return catalogv1alpha1.DelayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       catalogv1alpha1.DelayProfileSpec{Order: order, Tags: tags},
	}
}

func TestResolveExplicitRefWinsOutright(t *testing.T) {
	profiles := []catalogv1alpha1.DelayProfile{
		profile("catchall", 1000),
		profile("anime", 50, "anime"),
		profile("chosen", 500, "4k"),
	}
	ref := "chosen"
	got, err := Resolve(&ref, []string{"anime"}, profiles) // tags would otherwise match "anime"
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Name != "chosen" {
		t.Errorf("got %q, want chosen (explicit ref must win over a tag match)", got.Name)
	}
}

func TestResolveTagMatchBeatsCatchallEvenWithHigherOrder(t *testing.T) {
	profiles := []catalogv1alpha1.DelayProfile{
		profile("catchall", 1000),       // no tags: matches everything, order 1000
		profile("anime", 5000, "anime"), // matches "anime" but a WORSE (higher) order
	}
	got, err := Resolve(nil, []string{"anime"}, profiles)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Lowest order wins among everything that matches (tag overlap OR
	// catch-all); "anime" has 5000 > "catchall"'s 1000, so catchall wins even
	// though "anime" is the more specific tag match. This mirrors the CRD
	// doc's literal "lowest order" rule -- Order is the sole tie-breaker
	// once a profile is in the candidate set, tag specificity is not a
	// second-order signal.
	if got.Name != "catchall" {
		t.Errorf("got %q, want catchall (lower order wins regardless of tag specificity)", got.Name)
	}
}

func TestResolveLowestOrderAmongTagMatches(t *testing.T) {
	profiles := []catalogv1alpha1.DelayProfile{
		profile("catchall", 1000),
		profile("anime-strict", 50, "anime", "strict"),
		profile("anime-loose", 100, "anime"),
	}
	got, err := Resolve(nil, []string{"anime"}, profiles)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Name != "anime-strict" {
		t.Errorf("got %q, want anime-strict (order 50 < 100 < 1000)", got.Name)
	}
}

func TestResolveNoTagMatchFallsBackToCatchall(t *testing.T) {
	profiles := []catalogv1alpha1.DelayProfile{
		profile("catchall", 1000),
		profile("anime", 50, "anime"),
	}
	got, err := Resolve(nil, []string{"documentary"}, profiles)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Name != "catchall" {
		t.Errorf("got %q, want catchall", got.Name)
	}
}

func TestResolveDanglingRefFallsThroughToTagMatch(t *testing.T) {
	// The CRD does not say what happens when spec.delayProfileRef names a
	// profile that no longer exists. Treated here as "no ref" rather than an
	// error, so a deleted profile does not wedge the grab pipeline for every
	// item that referenced it; flagged as an inferred rule, not a spec fact.
	profiles := []catalogv1alpha1.DelayProfile{
		profile("catchall", 1000),
		profile("anime", 50, "anime"),
	}
	ref := "deleted-profile"
	got, err := Resolve(&ref, []string{"anime"}, profiles)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Name != "anime" {
		t.Errorf("got %q, want anime (dangling ref falls back to tag match)", got.Name)
	}
}

func TestResolveTiesBreakByName(t *testing.T) {
	profiles := []catalogv1alpha1.DelayProfile{
		profile("zzz", 100, "anime"),
		profile("aaa", 100, "anime"),
	}
	got, err := Resolve(nil, []string{"anime"}, profiles)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Name != "aaa" {
		t.Errorf("got %q, want aaa (lexicographically first on an order tie)", got.Name)
	}
}

func TestResolveNoProfilesIsAnError(t *testing.T) {
	if _, err := Resolve(nil, nil, nil); err != ErrNoProfiles {
		t.Errorf("err = %v, want ErrNoProfiles", err)
	}
}

func TestResolveNoMatchAndNoCatchallIsAnError(t *testing.T) {
	profiles := []catalogv1alpha1.DelayProfile{profile("anime", 50, "anime")}
	if _, err := Resolve(nil, []string{"documentary"}, profiles); err != ErrNoMatch {
		t.Errorf("err = %v, want ErrNoMatch", err)
	}
}
