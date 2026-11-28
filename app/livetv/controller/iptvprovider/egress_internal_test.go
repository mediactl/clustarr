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

package iptvprovider

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
)

func wgKey(t *testing.T) string {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(k.Bytes())
}

// egressProvider is an Xtream provider with spec.stream.egress over the
// Secret "argon-egress" keys tunnel-1 and tunnel-2.
func egressProvider(t *testing.T, addrs ...string) *clustarrv1.IPTVProvider {
	p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "argon"}}
	p.Spec.Server, p.Spec.CredentialsRef.Name = "panel.example", "argon-credentials"
	e := &clustarrv1.StreamEgress{Endpoint: "10.1.0.1:51820", PublicKey: wgKey(t)}
	for i, a := range addrs {
		e.Tunnels = append(e.Tunnels, clustarrv1.EgressTunnel{Address: a, PrivateKeyRef: corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "argon-egress"}, Key: []string{"tunnel-1", "tunnel-2"}[i],
		}})
	}
	p.Spec.Stream.Egress = e
	return p
}

func egressReconciler(t *testing.T, keys map[string]string) *Reconciler {
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	objs := []*corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "argon-credentials"}, Data: map[string][]byte{
		clustarrv1.CredentialsKeyUsername: []byte("u"), clustarrv1.CredentialsKeyPassword: []byte("p"),
	}}}
	if keys != nil {
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "argon-egress"}, Data: map[string][]byte{}}
		for k, v := range keys {
			sec.Data[k] = []byte(v + "\n") // as kubectl --from-file leaves it
		}
		objs = append(objs, sec)
	}
	b := fake.NewClientBuilder().WithScheme(s)
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	c := b.Build()
	return &Reconciler{Client: c, SecretReader: c}
}

func TestTheEgressTunnelsKeysAreReadIntoTheTunersConfig(t *testing.T) {
	k1, k2 := wgKey(t), wgKey(t)
	p := egressProvider(t, "10.100.0.21", "10.100.0.22")

	res, valid := egressReconciler(t, map[string]string{"tunnel-1": k1, "tunnel-2": k2}).resolve(context.Background(), p)

	require.Equal(t, metav1.ConditionTrue, valid.Status, valid.Message)
	c := buildConfig(p, nil, res, nil)
	require.NotNil(t, c.Egress)
	assert.Equal(t, "10.1.0.1:51820", c.Egress.Endpoint)
	assert.Equal(t, []tuner.EgressTunnel{
		{Address: netip.MustParseAddr("10.100.0.21"), PrivateKey: k1},
		{Address: netip.MustParseAddr("10.100.0.22"), PrivateKey: k2},
	}, c.Egress.Tunnels, "in order, keys trimmed")
}

func TestABrokenEgressIsInvalidAndNamesNoKey(t *testing.T) {
	k := wgKey(t)
	for name, tc := range map[string]struct {
		keys   map[string]string
		addrs  []string
		mutate func(*clustarrv1.StreamEgress)
		reason string
	}{
		"no Secret":           {nil, []string{"10.100.0.21"}, nil, clustarrv1.ReasonSecretNotFound},
		"no key":              {map[string]string{"tunnel-2": k}, []string{"10.100.0.21"}, nil, clustarrv1.ReasonSecretKeyNotFound},
		"not a key":           {map[string]string{"tunnel-1": "c2hvcnQ="}, []string{"10.100.0.21"}, nil, clustarrv1.ReasonInvalidEgress},
		"one address twice":   {map[string]string{"tunnel-1": k, "tunnel-2": k}, []string{"10.100.0.21", "10.100.0.21"}, nil, clustarrv1.ReasonInvalidEgress},
		"an IPv6 address":     {map[string]string{"tunnel-1": k}, []string{"fd00::21"}, nil, clustarrv1.ReasonInvalidEgress},
		"an endpoint no port": {map[string]string{"tunnel-1": k}, []string{"10.100.0.21"}, func(e *clustarrv1.StreamEgress) { e.Endpoint = "10.1.0.1" }, clustarrv1.ReasonInvalidEgress},
		"a bad public key":    {map[string]string{"tunnel-1": k}, []string{"10.100.0.21"}, func(e *clustarrv1.StreamEgress) { e.PublicKey = "nope" }, clustarrv1.ReasonInvalidEgress},
	} {
		t.Run(name, func(t *testing.T) {
			p := egressProvider(t, tc.addrs...)
			if tc.mutate != nil {
				tc.mutate(p.Spec.Stream.Egress)
			}

			res, valid := egressReconciler(t, tc.keys).resolve(context.Background(), p)

			assert.Equal(t, metav1.ConditionFalse, valid.Status)
			assert.Equal(t, tc.reason, valid.Reason, valid.Message)
			assert.NotContains(t, valid.Message, k)
			assert.Nil(t, res.egress)
		})
	}
}
