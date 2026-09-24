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

// Package wantedcron is spec §6.1's "wantedcron (12h missing/cutoff-unmet
// scan with per-item >=6h gap and Attempts backoff 6h*2^n capped 7d)".
//
// It is a manager.Runnable, not a reconciler: there is no custom resource to
// reconcile, only a clock. Every twelve hours it lists the catalog, decides
// which namespaces still hold an item worth searching for, and publishes one
// WantedScan per eligible namespace at the low tier.
//
// # Why the sweep publishes per namespace, not per item
//
// §5's subject table pins clustarr.work.catalogarr.wantedscan.low.<namespace>
// to catalog.WantedScan.v1 and describes it as "cron -> search workers", and
// the catalogarr-search-normal consumer -- the SEARCH worker's, not this
// package's -- already filters wantedscan.>. So the handoff is one message per
// namespace, and the search worker expands it into per-item searches.
//
// That leaves the per-item backoff §6.1 attributes to wantedcron running in
// the search worker. Backoff and NextEligible are therefore exported from
// here: this package owns the policy, the search worker (Task C8) applies it
// per item. eligibleNamespaces uses the same two functions, so the sweep never
// wakes a namespace whose every item the search worker would immediately skip.
//
// # Registration (Task C12)
//
// Nothing registers itself. app/catalog/run.go's setupControllers makes exactly
// this call -- and it needs an events.Bus, which setupControllers does not
// take today, so C12 either threads bus through or registers this from Run:
//
//	if err := (&wantedcron.Runnable{
//		Client:     mgr.GetClient(),
//		Bus:        bus,
//		Schedule:   wantedcron.TwelveHourly(),
//		Namespaces: o.WatchNamespaces,
//	}).SetupWithManager(mgr); err != nil {
//		return err
//	}
//
// It is leader-elected: a sweep from every replica would publish the same
// WantedScan N times. (The stream's deduplication window would absorb the
// duplicates, but relying on that instead of on the lease would make the
// Msg-Id load-bearing for correctness rather than for safety.)
package wantedcron
