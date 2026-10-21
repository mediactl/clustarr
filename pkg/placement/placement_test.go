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

package placement

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
)

// workers is the spec's workers profile (§3), verbatim.
var workers = Profile{
	NodeAffinity: []corev1.NodeSelectorRequirement{
		{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.NodeSelectorOpDoesNotExist},
		{Key: "kubernetes.io/arch", Operator: corev1.NodeSelectorOpIn, Values: []string{"amd64"}},
	},
	Tolerations: []corev1.Toleration{
		{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "media", Effect: corev1.TaintEffectNoSchedule},
	},
}

func required(spec *corev1.PodSpec) []corev1.NodeSelectorTerm {
	return spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
}

func TestApplyANDsIntoEveryRequiredTerm(t *testing.T) {
	a := corev1.NodeSelectorRequirement{Key: "a", Operator: corev1.NodeSelectorOpIn, Values: []string{"x"}}
	b := corev1.NodeSelectorRequirement{Key: "b", Operator: corev1.NodeSelectorOpExists}
	spec := &corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{MatchExpressions: []corev1.NodeSelectorRequirement{a}},
			{MatchExpressions: []corev1.NodeSelectorRequirement{b}},
		}},
	}}}

	workers.Apply(spec)

	terms := required(spec)
	require.Len(t, terms, 2, "terms OR: the profile joins every term, never adds one")
	assert.Equal(t, append([]corev1.NodeSelectorRequirement{a}, workers.NodeAffinity...), terms[0].MatchExpressions)
	assert.Equal(t, append([]corev1.NodeSelectorRequirement{b}, workers.NodeAffinity...), terms[1].MatchExpressions)
}

func TestApplyCreatesTheTermWhenNone(t *testing.T) {
	spec := &corev1.PodSpec{}

	workers.Apply(spec)

	terms := required(spec)
	require.Len(t, terms, 1)
	assert.Equal(t, workers.NodeAffinity, terms[0].MatchExpressions)
}

func TestApplyKeepsPreferredAndPodAffinity(t *testing.T) {
	preferred := []corev1.PreferredSchedulingTerm{{Weight: 10, Preference: corev1.NodeSelectorTerm{
		MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "p", Operator: corev1.NodeSelectorOpExists}},
	}}}
	podAffinity := &corev1.PodAffinity{PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
		Weight: 5, PodAffinityTerm: corev1.PodAffinityTerm{TopologyKey: "kubernetes.io/hostname",
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}},
	}}}
	anti := &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
		TopologyKey: "kubernetes.io/hostname", LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "me"}},
	}}}
	spec := &corev1.PodSpec{Affinity: &corev1.Affinity{
		NodeAffinity:    &corev1.NodeAffinity{PreferredDuringSchedulingIgnoredDuringExecution: preferred},
		PodAffinity:     podAffinity,
		PodAntiAffinity: anti,
	}}

	workers.Apply(spec)

	assert.Equal(t, preferred, spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution)
	assert.Equal(t, podAffinity, spec.Affinity.PodAffinity)
	assert.Equal(t, anti, spec.Affinity.PodAntiAffinity)
	assert.Equal(t, workers.NodeAffinity, required(spec)[0].MatchExpressions)
}

func TestApplyAppendsTolerationsOnce(t *testing.T) {
	own := corev1.Toleration{Key: "gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
	spec := &corev1.PodSpec{Tolerations: []corev1.Toleration{own}}

	workers.Apply(spec)
	workers.Apply(spec)

	assert.Equal(t, append([]corev1.Toleration{own}, workers.Tolerations...), spec.Tolerations)
}

func TestZeroProfileChangesNothing(t *testing.T) {
	spec := &corev1.PodSpec{NodeSelector: map[string]string{"k": "v"}}
	before := spec.DeepCopy()

	Profile{}.Apply(spec)

	assert.Equal(t, before, spec)
	assert.True(t, Profile{}.IsZero())
	assert.False(t, workers.IsZero())
}

func TestParseRejectsInvalidJSON(t *testing.T) {
	_, err := Parse("{")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker placement")
}

func TestParseEmptyIsZero(t *testing.T) {
	p, err := Parse("")

	require.NoError(t, err)
	assert.True(t, p.IsZero())
}

func TestParseReadsAProfile(t *testing.T) {
	b, err := json.Marshal(workers)
	require.NoError(t, err)

	p, err := Parse(string(b))

	require.NoError(t, err)
	assert.Equal(t, workers, p)
}

func TestApplyACMatchesApply(t *testing.T) {
	own := corev1.NodeSelectorRequirement{Key: "kubernetes.io/hostname", Operator: corev1.NodeSelectorOpIn, Values: []string{"b8"}}
	typed := &corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{MatchExpressions: []corev1.NodeSelectorRequirement{own}},
		}},
	}}}
	ac := corev1ac.PodSpec().WithAffinity(corev1ac.Affinity().WithNodeAffinity(corev1ac.NodeAffinity().
		WithRequiredDuringSchedulingIgnoredDuringExecution(corev1ac.NodeSelector().WithNodeSelectorTerms(
			corev1ac.NodeSelectorTerm().WithMatchExpressions(corev1ac.NodeSelectorRequirement().
				WithKey(own.Key).WithOperator(own.Operator).WithValues(own.Values...)),
		))))

	workers.Apply(typed)
	workers.ApplyAC(ac)

	// The scheduling fields only: a typed PodSpec also serializes its empty
	// containers as null, which an apply configuration omits.
	want, err := json.Marshal(map[string]any{"affinity": typed.Affinity, "tolerations": typed.Tolerations})
	require.NoError(t, err)
	got, err := json.Marshal(map[string]any{"affinity": ac.Affinity, "tolerations": ac.Tolerations})
	require.NoError(t, err)
	assert.JSONEq(t, string(want), string(got))
}

func TestApplyACCreatesTheTermWhenNone(t *testing.T) {
	ac := corev1ac.PodSpec()

	workers.ApplyAC(ac)
	workers.ApplyAC(ac)

	require.NotNil(t, ac.Affinity)
	terms := ac.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	require.Len(t, terms, 1)
	assert.Len(t, terms[0].MatchExpressions, len(workers.NodeAffinity), "applying twice does not repeat the expressions")
	assert.Len(t, ac.Tolerations, len(workers.Tolerations))
}

// TestParseRejectsAMistypedProfile is the final review's M3 (Review Focus 4):
// a mistyped key used to parse as the zero profile, so the manager started
// with no placement at all.
func TestParseRejectsAMistypedProfile(t *testing.T) {
	for name, in := range map[string]string{
		"unknown key":               `{"nodeAfinity":[{"key":"k","operator":"Exists"}]}`,
		"bad operator":              `{"nodeAffinity":[{"key":"k","operator":"Exist"}]}`,
		"In without values":         `{"nodeAffinity":[{"key":"k","operator":"In"}]}`,
		"Exists with values":        `{"nodeAffinity":[{"key":"k","operator":"Exists","values":["x"]}]}`,
		"no key":                    `{"nodeAffinity":[{"operator":"Exists"}]}`,
		"unknown toleration effect": `{"tolerations":[{"key":"k","operator":"Exists","effect":"NoSchedul"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(in)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "worker placement")
		})
	}
}
