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

package controller

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	autoscalingv2ac "k8s.io/client-go/applyconfigurations/autoscaling/v2"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
)

// Warning Event reasons the reconciler records on the Deployment.
const (
	ReasonNoMatchingNodes          = "NoMatchingNodes"
	ReasonSlotsExceedMaxAckPending = "SlotsExceedMaxAckPending"
	ReasonMinReplicasAboveCapacity = "MinReplicasAboveCapacity"
	ReasonInvalidSlots             = "InvalidSlots"
	ReasonInvalidMinReplicas       = "InvalidMinReplicas"
	ReasonScaleToZeroUnavailable   = "ScaleToZeroUnavailable"
	ReasonAutoscaleConflict        = "AutoscaleConflict"
	ReasonUnknownDomain            = "UnknownDomain"
)

// ScaleDownFloor is the shortest scale-down stabilization window (§9.0).
const ScaleDownFloor = 300 * time.Second

// scalePeriod is both behaviour policies' period.
const scalePeriod = 15

// RenderInput is everything RenderHPA reads.
type RenderInput struct {
	Deployment    *appsv1.Deployment
	Domain        agentdomain.Domain
	Consumers     []events.ConsumerSpec // the domain's, in Domain.Consumers order
	MatchingNodes int
	// ForceMinOne renders minReplicas of at least 1, for a cluster that
	// rejected 0 (HPAScaleToZero off).
	ForceMinOne bool
}

// Warning is one Event the reconciler records.
type Warning struct{ Reason, Message string }

// Rendered is the HPA and what went into it.
type Rendered struct {
	HPA         *autoscalingv2ac.HorizontalPodAutoscalerApplyConfiguration
	MinReplicas int32
	MaxReplicas int32
	Slots       map[string]int // consumer -> per-pod slots used as its AverageValue
	Warnings    []Warning
}

// RenderHPA builds the complete HPA declaration for one autoscaled
// Deployment (§9.5): minReplicas from the domain, raised by the
// min-replicas annotation; maxReplicas = max(min, max(1, min(nodes,
// floor(MaxAckPending/slots) over the consumers))); one External metric per
// consumer at AverageValue = its slots; scale-up at once by up to
// maxReplicas pods; scale-down after max(300 s, the longest AckWait).
func RenderHPA(in RenderInput) Rendered {
	d := in.Deployment
	out := Rendered{Slots: map[string]int{}}
	warn := func(reason, format string, args ...any) {
		out.Warnings = append(out.Warnings, Warning{Reason: reason, Message: fmt.Sprintf(format, args...)})
	}

	overrides, w := slotOverrides(&d.Spec.Template.Spec, in.Domain)
	if w != nil {
		out.Warnings = append(out.Warnings, *w)
	}

	floorMin := in.Domain.MinReplicas
	if v, ok := d.Annotations[agentdomain.AnnotationMinReplicas]; ok {
		n, err := strconv.ParseInt(v, 10, 32)
		switch {
		case err != nil || n < 0:
			warn(ReasonInvalidMinReplicas, "%s=%q is not a non-negative integer; the domain's floor %d is used",
				agentdomain.AnnotationMinReplicas, v, in.Domain.MinReplicas)
		case int32(n) > floorMin:
			floorMin = int32(n)
		}
	}
	if in.ForceMinOne && floorMin < 1 {
		floorMin = 1
	}

	clamp := math.MaxInt
	down := ScaleDownFloor
	metrics := make([]*autoscalingv2ac.MetricSpecApplyConfiguration, 0, len(in.Consumers))
	for _, c := range in.Consumers {
		// Topology.Validate holds Slots >= 1 and ParseSlotOverrides rejects
		// n < 1; the floor only keeps a hand-built input from dividing by 0.
		slots := max(1, events.SlotsFor(c, overrides))
		out.Slots[c.Name] = slots
		feed := c.MaxAckPending / slots
		if feed == 0 {
			warn(ReasonSlotsExceedMaxAckPending, "%s runs %d slots per pod, above its MaxAckPending %d; no second replica could be fed",
				c.Name, slots, c.MaxAckPending)
		}
		clamp = min(clamp, feed)
		down = max(down, c.AckWait)
		metrics = append(metrics, autoscalingv2ac.MetricSpec().
			WithType(autoscalingv2.ExternalMetricSourceType).
			WithExternal(autoscalingv2ac.ExternalMetricSource().
				WithMetric(autoscalingv2ac.MetricIdentifier().
					WithName(agentdomain.MetricConsumerLag).
					WithSelector(metav1ac.LabelSelector().WithMatchLabels(map[string]string{
						"stream": c.Stream, "consumer": c.Name,
					}))).
				WithTarget(autoscalingv2ac.MetricTarget().
					WithType(autoscalingv2.AverageValueMetricType).
					WithAverageValue(*resource.NewQuantity(int64(slots), resource.DecimalSI)))))
	}
	if in.MatchingNodes == 0 {
		warn(ReasonNoMatchingNodes, "no Ready, schedulable node matches the pod template's selectors, affinity, tolerations and extended resources")
	}
	computed := int32(max(1, min(in.MatchingNodes, clamp)))
	if floorMin > computed {
		warn(ReasonMinReplicasAboveCapacity, "minReplicas %d is above the %d replicas the matching nodes and MaxAckPending allow; maxReplicas follows it",
			floorMin, computed)
	}
	maxReplicas := max(floorMin, computed)
	out.MinReplicas, out.MaxReplicas = floorMin, maxReplicas

	labels := map[string]string{
		agentdomain.LabelDomain:     in.Domain.Name,
		"app.kubernetes.io/part-of": "clustarr",
	}
	if comp, ok := d.Labels["app.kubernetes.io/component"]; ok {
		labels["app.kubernetes.io/component"] = comp
	}
	out.HPA = autoscalingv2ac.HorizontalPodAutoscaler(d.Name, d.Namespace).
		WithLabels(labels).
		// No blockOwnerDeletion: it would need deployments/finalizers.
		WithOwnerReferences(metav1ac.OwnerReference().
			WithAPIVersion("apps/v1").WithKind("Deployment").
			WithName(d.Name).WithUID(d.UID).WithController(true)).
		WithSpec(autoscalingv2ac.HorizontalPodAutoscalerSpec().
			WithScaleTargetRef(autoscalingv2ac.CrossVersionObjectReference().
				WithAPIVersion("apps/v1").WithKind("Deployment").WithName(d.Name)).
			WithMinReplicas(floorMin).
			WithMaxReplicas(maxReplicas).
			WithMetrics(metrics...).
			WithBehavior(autoscalingv2ac.HorizontalPodAutoscalerBehavior().
				WithScaleUp(autoscalingv2ac.HPAScalingRules().
					WithStabilizationWindowSeconds(0).
					WithSelectPolicy(autoscalingv2.MaxChangePolicySelect).
					WithPolicies(autoscalingv2ac.HPAScalingPolicy().
						WithType(autoscalingv2.PodsScalingPolicy).WithValue(maxReplicas).WithPeriodSeconds(scalePeriod))).
				WithScaleDown(autoscalingv2ac.HPAScalingRules().
					WithStabilizationWindowSeconds(int32(down / time.Second)).
					WithSelectPolicy(autoscalingv2.MaxChangePolicySelect).
					WithPolicies(autoscalingv2ac.HPAScalingPolicy().
						WithType(autoscalingv2.PercentScalingPolicy).WithValue(100).WithPeriodSeconds(scalePeriod)))))
	return out
}

// slotOverrides reads CLUSTARR_CONSUMER_SLOTS from the pod template's
// literal env, the one source the agent and markers read too (§9.5, OD34).
// A valueFrom, two containers that disagree, a malformed value or an
// override naming a consumer outside the domain is a Warning, and the
// topology's slots are used.
func slotOverrides(spec *corev1.PodSpec, d agentdomain.Domain) (map[string]int, *Warning) {
	invalid := func(format string, args ...any) (map[string]int, *Warning) {
		return nil, &Warning{Reason: ReasonInvalidSlots,
			Message: fmt.Sprintf(format, args...) + "; the topology's slots are used"}
	}
	var value string
	seen := false
	for _, c := range spec.Containers {
		for _, e := range c.Env {
			if e.Name != events.SlotsEnv {
				continue
			}
			if e.ValueFrom != nil {
				return invalid("container %s sets %s through valueFrom, which the autoscaler cannot read", c.Name, events.SlotsEnv)
			}
			if seen && e.Value != value {
				return invalid("containers disagree on %s (%q and %q)", events.SlotsEnv, value, e.Value)
			}
			value, seen = e.Value, true
		}
	}
	if !seen || value == "" {
		return nil, nil
	}
	o, err := events.ParseSlotOverrides(value)
	if err != nil {
		return invalid("%s=%q: %v", events.SlotsEnv, value, err)
	}
	for name := range o {
		if !slices.Contains(d.Consumers, name) {
			return invalid("%s names %s, which domain %s does not drain", events.SlotsEnv, name, d.Name)
		}
	}
	return o, nil
}
