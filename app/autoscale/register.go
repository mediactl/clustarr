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

// Package autoscale registers the manager's autoscaling (spec 2026-10-06
// §9): the HPA reconciler, the External Metrics API server, its
// certificate manager, the work-queue gauge and the namespace guard.
// Only internal/cli/manager imports it.
package autoscale

import (
	"context"
	"errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/app/autoscale/controller"
	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Flag defaults (§3.4.1).
const (
	DefaultBindAddress = ":6443"
	DefaultServiceName = "external-metrics"
	DefaultSecretName  = "external-metrics-tls"
)

// Options is the four autoscale flags plus the manager's namespace.
type Options struct {
	Enabled     bool   // --autoscale
	Namespace   string // --namespace
	BindAddress string // --external-metrics-bind-address; "0" disables the API
	ServiceName string // --external-metrics-service
	SecretName  string // --external-metrics-secret
}

// Validate is §3.4.1's rule: --autoscale implies an enabled
// --external-metrics-bind-address.
func (o Options) Validate() error {
	if o.Namespace == "" {
		return errors.New("autoscale: a namespace is required: the External Metrics API serves only the manager's own")
	}
	if !o.Enabled {
		return nil
	}
	switch {
	case o.BindAddress == "" || o.BindAddress == "0":
		return errors.New("autoscale: --autoscale needs the External Metrics API; set --external-metrics-bind-address or pass --autoscale=false")
	case o.ServiceName == "":
		return errors.New("autoscale: --external-metrics-service is empty")
	case o.SecretName == "":
		return errors.New("autoscale: --external-metrics-secret is empty")
	}
	return nil
}

// Register adds QueueGauge always; with Enabled the reconciler, the
// Server (a *manager.Server, so it starts before the caches), CertManager
// and NamespaceGuard; without it, a leader-only pass that deletes every
// HPA the manager labelled. It adds no readiness check (§3.3): the API has
// its own Service with publishNotReadyAddresses.
func Register(mgr ctrl.Manager, admin events.StreamAdmin, o Options) error {
	if err := o.Validate(); err != nil {
		return err
	}
	g := &extmetrics.QueueGauge{States: admin, Topology: events.Default()}
	if ss, ok := admin.(events.StreamStater); ok {
		g.Streams = ss
	}
	if err := mgr.Add(g); err != nil {
		return fmt.Errorf("autoscale: add the queue gauge: %w", err)
	}
	if !o.Enabled {
		return mgr.Add(k8s.LeaderOnly(func(ctx context.Context) error {
			n, err := controller.DeleteManagedHPAs(ctx, mgr.GetAPIReader(), mgr.GetClient(), o.Namespace)
			if err != nil {
				logging.FromContext(ctx).Warn("autoscale: --autoscale=false could not delete the HPAs it labelled", "error", err)
				return nil
			}
			if n > 0 {
				logging.FromContext(ctx).Info("autoscale: --autoscale=false; deleted the HPAs a previous run owned", "count", n)
			}
			return nil
		}))
	}

	rec := &controller.Reconciler{Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("autoscale"), Namespace: o.Namespace}
	if err := rec.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("autoscale: set up the HPA reconciler: %w", err)
	}
	series, err := extmetrics.ServableSeries(agentdomain.Domains(), events.Default())
	if err != nil {
		return err
	}
	srv, err := extmetrics.NewServer(extmetrics.ServerOptions{
		Namespace: o.Namespace, BindAddress: o.BindAddress, SecretName: o.SecretName, Series: series,
	}, mgr.GetAPIReader(), admin)
	if err != nil {
		return err
	}
	runnable, err := srv.Runnable()
	if err != nil {
		return err
	}
	if err := mgr.Add(runnable); err != nil {
		return fmt.Errorf("autoscale: add the External Metrics API server: %w", err)
	}
	if err := mgr.Add(&extmetrics.CertManager{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Recorder: mgr.GetEventRecorder("external-metrics"),
		Namespace: o.Namespace, ServiceName: o.ServiceName, SecretName: o.SecretName,
	}); err != nil {
		return fmt.Errorf("autoscale: add the certificate manager: %w", err)
	}
	if err := mgr.Add(&extmetrics.NamespaceGuard{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Namespace: o.Namespace, ServiceName: o.ServiceName,
	}); err != nil {
		return fmt.Errorf("autoscale: add the namespace guard: %w", err)
	}
	return nil
}
