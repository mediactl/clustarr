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

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// ReasonDispatchUnattended is the Warning Event recorded on the manager's
// Pod when a dispatched durable turns unattended (§5.5, §7.7).
const ReasonDispatchUnattended = "DispatchUnattended"

// attendance is one durable's unattended detection state.
type attendance struct {
	known bool
	// lag and lagSince: the last lag read and when it last changed.
	lag      uint64
	lagSince time.Time
	// stalled: Pending > 0, Waiting == 0 and the lag unchanged for
	// UnattendedAfter, at the last successful read.
	stalled bool
	// absentSince is when the domain's presence was first seen missing
	// while the durable had lag; zero while present or idle.
	absentSince time.Time
	unattended  bool
}

// tick is the leader's periodic pass: lapse every book, detect every
// dispatched durable's attendance, and set the two dispatch gauges.
func (l *Ledger) tick(ctx context.Context) {
	now := l.now()
	l.mu.Lock()
	for durable, book := range l.books {
		l.lapseLocked(book, now)
		l.lapseWaitingLocked(durable, now)
	}
	l.mu.Unlock()
	domains := durableDomains()
	for _, c := range l.o.Topology.Consumers {
		if !c.Dispatched || c.Stream == events.StreamAdvisories {
			continue
		}
		l.detect(ctx, c, domains[c.Name], now)
		metrics.DispatchWaiting.WithLabelValues(c.Name).Set(float64(l.WaitingCount(c.Name)))
	}
}

// durableDomains maps every agent domain's durable to its domain.
func durableDomains() map[string]string {
	out := map[string]string{}
	for _, d := range agentdomain.Domains() {
		for _, c := range d.Consumers {
			out[c] = d.Name
		}
	}
	return out
}

// detect decides c's attendance (§5.5): unattended when the durable has
// Pending > 0, Waiting == 0 and an unchanged lag for UnattendedAfter, or
// when its domain has no presence key while it has lag, for the same two
// minutes (ruling A2-1: an autoscaled domain at zero with nothing queued is
// not unattended, and the HPA gets the window to wake it). A durable the
// manager itself consumes (no domain) is judged on its state alone. A
// failed read keeps the last verdict. Each transition sets
// clustarr_dispatch_unattended and records one Warning on unattended.
func (l *Ledger) detect(ctx context.Context, c events.ConsumerSpec, domain string, now time.Time) {
	log := logging.FromContext(ctx)
	var (
		st     events.ConsumerState
		absent bool
		prErr  error
	)
	stErr := errors.New("no consumer state reader")
	if l.o.States != nil {
		st, stErr = l.o.States.ConsumerState(ctx, c.Stream, c.Name)
	}
	if domain != "" && l.o.Presence != nil {
		var present bool
		present, prErr = l.o.Presence.Present(ctx, domain)
		absent = prErr == nil && !present
	}

	l.mu.Lock()
	a := l.attendance[c.Name]
	if a == nil {
		a = &attendance{}
		l.attendance[c.Name] = a
	}
	if stErr == nil {
		if !a.known || st.Lag() != a.lag {
			a.lag, a.lagSince, a.known = st.Lag(), now, true
		}
		a.stalled = st.Pending > 0 && st.Waiting == 0 && now.Sub(a.lagSince) >= UnattendedAfter
	}
	if prErr == nil {
		switch {
		case absent && a.known && a.lag > 0:
			if a.absentSince.IsZero() {
				a.absentSince = now
			}
		default:
			a.absentSince = time.Time{}
		}
	}
	noAgent := !a.absentSince.IsZero() && now.Sub(a.absentSince) >= UnattendedAfter
	was := a.unattended
	a.unattended = a.stalled || noAgent
	is, stalled, lag := a.unattended, a.stalled, a.lag
	l.mu.Unlock()

	if stErr != nil {
		log.Debug("dispatch: could not read a durable's state; its attendance is unchanged", "consumer", c.Name, "error", stErr)
	}
	if prErr != nil {
		log.Debug("dispatch: could not read presence; attendance by presence is unchanged", "domain", domain, "error", prErr)
	}
	if is {
		metrics.DispatchUnattended.WithLabelValues(c.Name).Set(1)
	} else {
		metrics.DispatchUnattended.WithLabelValues(c.Name).Set(0)
	}
	if is == was {
		return
	}
	if !is {
		log.Info("dispatch: durable attended again", "consumer", c.Name)
		return
	}
	var why []string
	if stalled {
		why = append(why, fmt.Sprintf("%d pending with no pull open and no progress for %s", lag, UnattendedAfter))
	}
	if noAgent {
		why = append(why, fmt.Sprintf("no %s agent present for %s", domain, UnattendedAfter))
	}
	note := fmt.Sprintf("%s is unattended: %s", c.Name, strings.Join(why, "; "))
	log.Warn("dispatch: "+note, "consumer", c.Name)
	l.record(note)
}

// record files the DispatchUnattended Warning on the manager's Pod.
func (l *Ledger) record(note string) {
	if l.o.Recorder == nil || l.o.Pod.Name == "" {
		return
	}
	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Name: l.o.Pod.Name, Namespace: l.o.Pod.Namespace},
	}
	l.o.Recorder.Eventf(pod, nil, corev1.EventTypeWarning, ReasonDispatchUnattended, "Detect", "%s", note)
}
