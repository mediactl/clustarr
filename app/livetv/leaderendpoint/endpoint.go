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

package leaderendpoint

import (
	"context"
	"math"
	"net"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	discoveryv1ac "k8s.io/client-go/applyconfigurations/discovery/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// ManagedBy is the EndpointSlice's endpointslice.kubernetes.io/managed-by
// value, so the EndpointSlice controller leaves the slice alone.
const ManagedBy = "clustarr.io/manager"

// portName is the livetv Service's port name: kube-proxy matches a slice's
// ports to the Service's by name.
const portName = "http"

// Publisher is the leader-only runnable that writes the slice.
type Publisher struct {
	Client client.Client
	// Tuner is the leader's tuner; the slice is written once it listens.
	Tuner interface{ Listening() <-chan struct{} }
	// Service and Namespace name the livetv Service (--livetv-service).
	Service   string
	Namespace string
	// PodName and PodIP are this pod's, from the downward API.
	PodName string
	PodIP   string
	// Port is the tuner's port, the bind address's (TargetPort).
	Port int32
}

// NeedLeaderElection keeps the publisher on the leader: only its tuner
// listens.
func (p *Publisher) NeedLeaderElection() bool { return true }

// Start waits for the tuner to listen, writes the slice -- retrying a failed
// write from 1 s doubling to 30 s -- and holds until ctx ends. Without a pod
// IP it publishes nothing: an empty address would route nowhere.
func (p *Publisher) Start(ctx context.Context) error {
	log := logging.FromContext(ctx).With("service", p.Service)
	if p.PodIP == "" {
		log.Warn("livetv: no POD_IP, so this leader is not published as the livetv endpoint")
		<-ctx.Done()
		return nil
	}
	select {
	case <-ctx.Done():
		return nil
	case <-p.Tuner.Listening():
	}
	backoff := wait.Backoff{Duration: time.Second, Factor: 2, Cap: 30 * time.Second, Steps: math.MaxInt32}
	for {
		_, err := k8s.Apply(ctx, p.Client, k8s.ManagerLiveTVEndpoint, p.slice())
		if err == nil {
			log.Info("livetv: published this leader as the livetv endpoint", "address", p.PodIP, "port", p.Port)
			break
		}
		log.Error("livetv: publish the livetv endpoint", "err", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff.Step()):
		}
	}
	<-ctx.Done()
	return nil
}

// slice is the whole EndpointSlice, a complete declaration under the
// publisher's field manager: the Service's one endpoint, this pod.
func (p *Publisher) slice() *discoveryv1ac.EndpointSliceApplyConfiguration {
	family := discoveryv1.AddressTypeIPv4
	if ip := net.ParseIP(p.PodIP); ip != nil && ip.To4() == nil {
		family = discoveryv1.AddressTypeIPv6
	}
	ep := discoveryv1ac.Endpoint().
		WithAddresses(p.PodIP).
		WithConditions(discoveryv1ac.EndpointConditions().WithReady(true))
	if p.PodName != "" {
		ep = ep.WithTargetRef(corev1ac.ObjectReference().WithKind("Pod").WithName(p.PodName).WithNamespace(p.Namespace))
	}
	return discoveryv1ac.EndpointSlice(p.Service, p.Namespace).
		WithLabels(map[string]string{
			discoveryv1.LabelServiceName: p.Service,
			discoveryv1.LabelManagedBy:   ManagedBy,
		}).
		WithAddressType(family).
		WithEndpoints(ep).
		WithPorts(discoveryv1ac.EndpointPort().WithName(portName).WithPort(p.Port).WithProtocol(corev1.ProtocolTCP))
}
