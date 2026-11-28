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

package usenet

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	usenetclient "github.com/mediactl/clustarr/pkg/download/usenet"
	"github.com/mediactl/clustarr/pkg/download/usenetaccount"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// budgeted records the budgets a client was given.
type budgeted struct{ got []usenetclient.Budget }

func (b *budgeted) SetBudget(x usenetclient.Budget) { b.got = append(b.got, x) }
func (*budgeted) WireBytes() int64                  { return 0 }
func (*budgeted) ConnectionsHeld() int              { return 0 }

// A budget sets the limiter to its rate in bytes, and the client's
// transfers and weight; an older one is ignored; none at all is the engine
// as it runs without the feature.
func TestTheBudgetApplierSetsTheRateTheTransfersAndTheWeight(t *testing.T) {
	cl := &budgeted{}
	lim := NewLimiter()
	require.Equal(t, rate.Inf, lim.Limit(), "no bound before any budget")
	b := &BudgetApplier{Client: cl, Limiter: lim}

	b.Apply(&schema.EngineBudget{BitsPerSecond: 2_000_000_000, Transfers: 8, Weight: 2000, Seq: 5})
	assert.Equal(t, rate.Limit(250_000_000), lim.Limit(), "2 Gbit/s is 250 MB/s")
	assert.Equal(t, 62_500_000, lim.Burst(), "a quarter second of it at once")
	assert.Equal(t, usenetclient.Budget{Transfers: 8, Weight: 2000}, cl.got[len(cl.got)-1])
	assert.Equal(t, int64(5), b.Seq())

	b.Apply(&schema.EngineBudget{BitsPerSecond: 1_000_000, Transfers: 1, Weight: 1, Seq: 4})
	assert.Equal(t, rate.Limit(250_000_000), lim.Limit(), "an older budget changes nothing")
	assert.Equal(t, int64(5), b.Seq())

	b.Apply(&schema.EngineBudget{BitsPerSecond: 8_000_000, Transfers: 1, Weight: 8, Seq: 6})
	assert.Equal(t, rate.Limit(1_000_000), lim.Limit())
	assert.Equal(t, budgetBurst, lim.Burst(), "never less than a few articles at once")

	b.Apply(nil)
	assert.Equal(t, rate.Inf, lim.Limit())
	assert.Equal(t, usenetclient.Budget{Transfers: math.MaxInt32}, cl.got[len(cl.got)-1], "every slot of maxActive again")
	assert.Zero(t, b.Seq())
}

// The manager names an account from the spec as the engine names it from
// the provider it resolved: both read one ledger entry.
func TestTheManagerAndTheEngineNameAnAccountAlike(t *testing.T) {
	plain := false
	us := &downloadv1alpha1.UsenetSpec{
		ConnectionPools: []downloadv1alpha1.ConnectionPool{{Name: "plan", Connections: 100}},
		Providers: []downloadv1alpha1.NNTPProvider{
			{Name: "us", Host: " news.example ", Pool: "plan", SecretRef: corev1.LocalObjectReference{Name: "s"}},
			{Name: "eu", Host: "eunews.example", Port: 5563, Pool: "plan", Connections: 25, Backup: true},
			{Name: "alone", Host: "other.example"},
			{Name: "plain", Host: "plain.example", TLS: &plain},
			{Name: "stray", Host: "stray.example", Pool: "undeclared"},
		},
	}
	want := []string{"pool:plan:me", "pool:plan:me", "other.example:563:me", "plain.example:119:me", "stray.example:563:me"}
	for i, p := range us.Providers {
		engine := usenetclient.PooledProviderFromSpec(us, p, "me", "secret")
		assert.Equal(t, want[i], engine.Account(), p.Name)
		assert.Equal(t, engine.Account(), usenetaccount.KeyOf(us, p, "me"), p.Name)
	}
	pooled := usenetclient.PooledProviderFromSpec(us, us.Providers[0], "me", "secret")
	assert.Equal(t, 100, pooled.Connections, "a pooled provider that states none may hold the whole pool")
	assert.Equal(t, 25, usenetclient.PooledProviderFromSpec(us, us.Providers[1], "me", "secret").HostLimit())
	assert.Equal(t, 8, usenetclient.PooledProviderFromSpec(us, us.Providers[2], "me", "secret").Connections, "on its own: eight, as before")
}
