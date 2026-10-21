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

// Package placement is the control-plane placement design's profile
// (docs/superpowers/specs/2026-10-21-control-plane-placement-design.md §3,
// §5.6) for the pods the manager builds itself: the engines, the transcode
// pools and graft Jobs. The chart renders the same profile for its own
// templates; the manager takes it as --worker-placement.
package placement

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
)

// Profile is a placement profile: expressions every required node-affinity
// term must carry, and tolerations to add. Terms OR and a term's
// expressions AND, so the expressions join every term the pod already has
// -- a new term would let the pod schedule by the old one alone.
type Profile struct {
	NodeAffinity []corev1.NodeSelectorRequirement `json:"nodeAffinity,omitempty"`
	Tolerations  []corev1.Toleration              `json:"tolerations,omitempty"`
}

// Parse reads a profile from its JSON form, as --worker-placement carries
// it. The empty string is the zero Profile, which places nothing. Anything
// else must parse strictly and hold valid requirements and tolerations: a
// mistyped key would otherwise read as the zero profile, and the manager
// would start placing nothing (final review M3).
func Parse(s string) (Profile, error) {
	var p Profile
	if s == "" {
		return p, nil
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("worker placement: %w", err)
	}
	if err := p.validate(); err != nil {
		return Profile{}, fmt.Errorf("worker placement: %w", err)
	}
	return p, nil
}

// validate holds each requirement and toleration to what the apiserver
// accepts, so a bad profile stops the manager at start rather than failing
// every pod it builds.
func (p Profile) validate() error {
	for i, r := range p.NodeAffinity {
		if r.Key == "" {
			return fmt.Errorf("nodeAffinity[%d]: no key", i)
		}
		switch r.Operator {
		case corev1.NodeSelectorOpIn, corev1.NodeSelectorOpNotIn:
			if len(r.Values) == 0 {
				return fmt.Errorf("nodeAffinity[%d] %s: operator %s needs values", i, r.Key, r.Operator)
			}
		case corev1.NodeSelectorOpExists, corev1.NodeSelectorOpDoesNotExist:
			if len(r.Values) != 0 {
				return fmt.Errorf("nodeAffinity[%d] %s: operator %s takes no values", i, r.Key, r.Operator)
			}
		case corev1.NodeSelectorOpGt, corev1.NodeSelectorOpLt:
			if len(r.Values) != 1 {
				return fmt.Errorf("nodeAffinity[%d] %s: operator %s takes one value", i, r.Key, r.Operator)
			}
		default:
			return fmt.Errorf("nodeAffinity[%d] %s: unknown operator %q", i, r.Key, r.Operator)
		}
	}
	for i, t := range p.Tolerations {
		switch t.Operator {
		case "", corev1.TolerationOpEqual, corev1.TolerationOpExists:
		default:
			return fmt.Errorf("tolerations[%d]: unknown operator %q", i, t.Operator)
		}
		switch t.Effect {
		case "", corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule, corev1.TaintEffectNoExecute:
		default:
			return fmt.Errorf("tolerations[%d]: unknown effect %q", i, t.Effect)
		}
	}
	return nil
}

// IsZero reports whether p places nothing.
func (p Profile) IsZero() bool { return len(p.NodeAffinity) == 0 && len(p.Tolerations) == 0 }

// Apply joins p's expressions to every required node-affinity term of spec,
// creating the one term when there is none, and appends p's tolerations.
// Preferred terms and pod (anti-)affinity are left alone, and applying twice
// adds nothing the first application did not.
func (p Profile) Apply(spec *corev1.PodSpec) {
	if len(p.NodeAffinity) > 0 {
		if spec.Affinity == nil {
			spec.Affinity = &corev1.Affinity{}
		}
		if spec.Affinity.NodeAffinity == nil {
			spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
		}
		na := spec.Affinity.NodeAffinity
		if na.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			na.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{}
		}
		sel := na.RequiredDuringSchedulingIgnoredDuringExecution
		if len(sel.NodeSelectorTerms) == 0 {
			sel.NodeSelectorTerms = []corev1.NodeSelectorTerm{{}}
		}
		for i := range sel.NodeSelectorTerms {
			term := &sel.NodeSelectorTerms[i]
			for _, r := range p.NodeAffinity {
				if !slices.ContainsFunc(term.MatchExpressions, func(e corev1.NodeSelectorRequirement) bool {
					return equality.Semantic.DeepEqual(e, r)
				}) {
					term.MatchExpressions = append(term.MatchExpressions, *r.DeepCopy())
				}
			}
		}
	}
	for _, t := range p.Tolerations {
		if !slices.ContainsFunc(spec.Tolerations, func(have corev1.Toleration) bool { return sameToleration(have, t) }) {
			spec.Tolerations = append(spec.Tolerations, t)
		}
	}
}

// ApplyAC is Apply for a pod spec built as an apply configuration, as the
// DownloadClient controller builds its engines.
func (p Profile) ApplyAC(spec *corev1ac.PodSpecApplyConfiguration) {
	if len(p.NodeAffinity) > 0 {
		if spec.Affinity == nil {
			spec.WithAffinity(corev1ac.Affinity())
		}
		if spec.Affinity.NodeAffinity == nil {
			spec.Affinity.WithNodeAffinity(corev1ac.NodeAffinity())
		}
		na := spec.Affinity.NodeAffinity
		if na.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			na.WithRequiredDuringSchedulingIgnoredDuringExecution(corev1ac.NodeSelector())
		}
		sel := na.RequiredDuringSchedulingIgnoredDuringExecution
		if len(sel.NodeSelectorTerms) == 0 {
			sel.WithNodeSelectorTerms(corev1ac.NodeSelectorTerm())
		}
		for i := range sel.NodeSelectorTerms {
			term := &sel.NodeSelectorTerms[i]
			for _, r := range p.NodeAffinity {
				if !slices.ContainsFunc(term.MatchExpressions, func(e corev1ac.NodeSelectorRequirementApplyConfiguration) bool {
					return requirementAC(r).equal(e)
				}) {
					term.WithMatchExpressions(requirementAC(r).ac())
				}
			}
		}
	}
	for _, t := range p.Tolerations {
		if !slices.ContainsFunc(spec.Tolerations, func(have corev1ac.TolerationApplyConfiguration) bool {
			return sameToleration(fromTolerationAC(have), t)
		}) {
			spec.WithTolerations(tolerationAC(t))
		}
	}
}

// sameToleration compares the fields that make a toleration distinct; a
// toleration differing only in TolerationSeconds is the same toleration.
func sameToleration(a, b corev1.Toleration) bool {
	return a.Key == b.Key && a.Operator == b.Operator && a.Value == b.Value && a.Effect == b.Effect
}

type requirementAC corev1.NodeSelectorRequirement

func (r requirementAC) ac() *corev1ac.NodeSelectorRequirementApplyConfiguration {
	c := corev1ac.NodeSelectorRequirement().WithKey(r.Key).WithOperator(r.Operator)
	if len(r.Values) > 0 {
		c.WithValues(r.Values...)
	}
	return c
}

func (r requirementAC) equal(e corev1ac.NodeSelectorRequirementApplyConfiguration) bool {
	return e.Key != nil && *e.Key == r.Key && e.Operator != nil && *e.Operator == r.Operator && slices.Equal(e.Values, r.Values)
}

func tolerationAC(t corev1.Toleration) *corev1ac.TolerationApplyConfiguration {
	c := corev1ac.Toleration()
	if t.Key != "" {
		c.WithKey(t.Key)
	}
	if t.Operator != "" {
		c.WithOperator(t.Operator)
	}
	if t.Value != "" {
		c.WithValue(t.Value)
	}
	if t.Effect != "" {
		c.WithEffect(t.Effect)
	}
	if t.TolerationSeconds != nil {
		c.WithTolerationSeconds(*t.TolerationSeconds)
	}
	return c
}

func fromTolerationAC(c corev1ac.TolerationApplyConfiguration) corev1.Toleration {
	var t corev1.Toleration
	if c.Key != nil {
		t.Key = *c.Key
	}
	if c.Operator != nil {
		t.Operator = *c.Operator
	}
	if c.Value != nil {
		t.Value = *c.Value
	}
	if c.Effect != nil {
		t.Effect = *c.Effect
	}
	return t
}
