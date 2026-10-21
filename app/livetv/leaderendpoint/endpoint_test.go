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

package leaderendpoint_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mediactl/clustarr/app/livetv/leaderendpoint"
	"github.com/mediactl/clustarr/pkg/k8s"
)

type tuner struct{ ch chan struct{} }

func (t tuner) Listening() <-chan struct{} { return t.ch }

func newClient() client.Client {
	return fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithReturnManagedFields().Build()
}

var key = types.NamespacedName{Namespace: "clustarr-system", Name: "clustarr-livetv"}

func publisher(c client.Client, tn tuner, ip string) *leaderendpoint.Publisher {
	return &leaderendpoint.Publisher{Client: c, Tuner: tn, Service: key.Name, Namespace: key.Namespace,
		PodName: "clustarr-manager-abc", PodIP: ip, Port: 5004}
}

func run(t *testing.T, p *leaderendpoint.Publisher) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })
	return cancel
}

func get(c client.Client) (*discoveryv1.EndpointSlice, error) {
	var s discoveryv1.EndpointSlice
	err := c.Get(context.Background(), key, &s)
	return &s, err
}

// TestPublishesTheLeadersAddressAfterTheTunerListens is the control-plane
// placement design's §5.4: the livetv Service has no selector, and the
// leader is its one endpoint -- but only once its tuner listens.
func TestPublishesTheLeadersAddressAfterTheTunerListens(t *testing.T) {
	c := newClient()
	tn := tuner{ch: make(chan struct{})}
	run(t, publisher(c, tn, "10.42.2.17"))

	time.Sleep(100 * time.Millisecond)
	_, err := get(c)
	require.True(t, apierrors.IsNotFound(err), "no endpoint before the tuner listens, got %v", err)

	close(tn.ch)
	require.Eventually(t, func() bool { _, err := get(c); return err == nil }, 2*time.Second, 10*time.Millisecond)
	s, err := get(c)
	require.NoError(t, err)

	assert.Equal(t, key.Name, s.Labels[discoveryv1.LabelServiceName])
	assert.Equal(t, leaderendpoint.ManagedBy, s.Labels[discoveryv1.LabelManagedBy])
	assert.Equal(t, discoveryv1.AddressTypeIPv4, s.AddressType)
	require.Len(t, s.Endpoints, 1)
	ep := s.Endpoints[0]
	assert.Equal(t, []string{"10.42.2.17"}, ep.Addresses)
	require.NotNil(t, ep.Conditions.Ready)
	assert.True(t, *ep.Conditions.Ready)
	require.NotNil(t, ep.TargetRef)
	assert.Equal(t, corev1.ObjectReference{Kind: "Pod", Name: "clustarr-manager-abc", Namespace: key.Namespace}, *ep.TargetRef)
	require.Len(t, s.Ports, 1)
	assert.Equal(t, "http", *s.Ports[0].Name, "the livetv Service's port name")
	assert.Equal(t, int32(5004), *s.Ports[0].Port)
	assert.Equal(t, corev1.ProtocolTCP, *s.Ports[0].Protocol)
	var managers []string
	for _, m := range s.ManagedFields {
		managers = append(managers, m.Manager)
	}
	assert.Contains(t, managers, k8s.ManagerLiveTVEndpoint.String())
}

func TestANewLeaderReplacesTheAddress(t *testing.T) {
	c := newClient()
	first := tuner{ch: make(chan struct{})}
	close(first.ch)
	stop := run(t, publisher(c, first, "10.42.2.17"))
	require.Eventually(t, func() bool { _, err := get(c); return err == nil }, 2*time.Second, 10*time.Millisecond)
	stop()

	second := tuner{ch: make(chan struct{})}
	close(second.ch)
	p := publisher(c, second, "10.42.0.9")
	p.PodName = "clustarr-manager-def"
	run(t, p)

	require.Eventually(t, func() bool {
		s, err := get(c)
		return err == nil && len(s.Endpoints) == 1 && s.Endpoints[0].Addresses[0] == "10.42.0.9"
	}, 2*time.Second, 10*time.Millisecond)
}

func TestAnIPv6PodIsAnIPv6Slice(t *testing.T) {
	c := newClient()
	tn := tuner{ch: make(chan struct{})}
	close(tn.ch)
	run(t, publisher(c, tn, "fd00::17"))

	require.Eventually(t, func() bool { _, err := get(c); return err == nil }, 2*time.Second, 10*time.Millisecond)
	s, _ := get(c)
	assert.Equal(t, discoveryv1.AddressTypeIPv6, s.AddressType)
}

// TestSkipsWithoutAPodIP: make run-dev, or an overlay without POD_IP, must
// never publish an empty address (the plan's Review Focus 2).
func TestSkipsWithoutAPodIP(t *testing.T) {
	c := newClient()
	tn := tuner{ch: make(chan struct{})}
	close(tn.ch)
	run(t, publisher(c, tn, ""))

	time.Sleep(200 * time.Millisecond)
	_, err := get(c)
	assert.True(t, apierrors.IsNotFound(err), "got %v", err)
}

func TestNeedsLeaderElection(t *testing.T) {
	assert.True(t, (&leaderendpoint.Publisher{}).NeedLeaderElection(), "the tuner listens on the leader alone")
}
