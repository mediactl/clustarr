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
	"context"
	"encoding/json"
	"math"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	usenetclient "github.com/mediactl/clustarr/pkg/download/usenet"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// budgetBurst is the least the engine's limiter lets through at once: a
// few articles, so one pipelined exchange is never split into many waits.
const budgetBurst = 4 << 20

// budgetRetry is how soon a failed watch of the budget bucket is opened
// again.
const budgetRetry = 30 * time.Second

// NewLimiter is the limiter an engine hands its client
// ([ClientOptions.Limiter]) and its [BudgetApplier]: no bound until a
// budget says otherwise.
func NewLimiter() *rate.Limiter { return rate.NewLimiter(rate.Inf, budgetBurst) }

// BudgetApplier applies the engine's bandwidth budget (spec 2026-11-27
// §5): it watches the engine's own key in clustarr-engine-budgets, which
// the DownloadClient controller writes, and sets the limiter's rate, the
// client's transfer slots and its weight among the pods sharing its
// provider accounts. With no budget -- the key absent or deleted -- the
// engine runs as it does without the feature: unbounded, on maxActive.
type BudgetApplier struct {
	KV      events.KV
	Key     string
	Client  usenetclient.Budgeted
	Limiter *rate.Limiter
	// ClientName and Engine label the budget gauge; "" records none.
	ClientName, Engine string

	seq atomic.Int64
}

// NeedLeaderElection implements manager.LeaderElectionRunnable: every
// engine pod applies its own budget, lease or no lease.
func (b *BudgetApplier) NeedLeaderElection() bool { return false }

// Seq is the budget last applied, 0 for none (the engine record's
// budgetSeq).
func (b *BudgetApplier) Seq() int64 { return b.seq.Load() }

// Apply applies one budget; nil is no budget.
func (b *BudgetApplier) Apply(bud *schema.EngineBudget) {
	if b.ClientName != "" {
		var bits int64
		if bud != nil && bud.Seq >= b.seq.Load() {
			bits = bud.BitsPerSecond
		}
		if bud == nil || bud.Seq >= b.seq.Load() {
			metrics.UsenetEngineBudget.WithLabelValues(b.ClientName, b.Engine).Set(float64(bits))
		}
	}
	if bud == nil {
		b.Limiter.SetLimit(rate.Inf)
		b.Client.SetBudget(usenetclient.Budget{Transfers: math.MaxInt32})
		b.seq.Store(0)
		return
	}
	if bud.Seq < b.seq.Load() {
		return
	}
	if bud.BitsPerSecond > 0 {
		perSecond := bud.BitsPerSecond / 8
		// A quarter second of the rate at once, never less than a few
		// articles.
		b.Limiter.SetBurst(int(max(budgetBurst, min(perSecond/4, math.MaxInt32))))
		b.Limiter.SetLimit(rate.Limit(perSecond))
	} else {
		b.Limiter.SetLimit(rate.Inf)
	}
	transfers := int(bud.Transfers)
	if transfers <= 0 {
		transfers = math.MaxInt32
	}
	b.Client.SetBudget(usenetclient.Budget{Transfers: transfers, Weight: bud.Weight})
	b.seq.Store(bud.Seq)
}

// Start watches the key until ctx ends, opening the watch again after a
// failure: a budget is advice the engine can run without.
func (b *BudgetApplier) Start(ctx context.Context) error {
	ctx = logging.Subsystem(ctx, "grab.engine")
	log := logging.FromContext(ctx)
	for ctx.Err() == nil {
		ch, err := b.KV.Watch(ctx, b.Key)
		if err != nil {
			log.WarnContext(ctx, "usenet engine: watching the bandwidth budget failed; trying again", "err", err)
		} else {
			for e := range ch {
				if len(e.Value) == 0 {
					log.InfoContext(ctx, "usenet engine: the bandwidth budget was removed; running without one")
					b.Apply(nil)
					continue
				}
				var bud schema.EngineBudget
				if err := json.Unmarshal(e.Value, &bud); err != nil {
					log.WarnContext(ctx, "usenet engine: an unreadable bandwidth budget is ignored", "err", err)
					continue
				}
				b.Apply(&bud)
				log.InfoContext(ctx, "usenet engine: bandwidth budget applied",
					"bitsPerSecond", bud.BitsPerSecond, "transfers", bud.Transfers, "weight", bud.Weight, "seq", bud.Seq)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(budgetRetry):
		}
	}
	return nil
}
