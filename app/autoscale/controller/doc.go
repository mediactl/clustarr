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

// Package controller is the manager's autoscale reconciler (spec
// 2026-10-06 §9.5): one autoscaling/v2 HorizontalPodAutoscaler per
// Deployment labelled autoscale.clustarr.io/domain in the manager's
// namespace, applied under clustarr-autoscale. MatchingNodes and RenderHPA
// are its pure halves.
package controller

// The reconciler's RBAC (spec 2026-10-06 §9.5). horizontalpodautoscalers
// carries create and update beside patch because a server-side apply that
// creates the object needs them; deployments/scale carries get beside
// update because k8s.ScaleTo reads the Scale before writing it; nodes is
// the MatchingNodes watch.
//
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments/scale,verbs=get;update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
