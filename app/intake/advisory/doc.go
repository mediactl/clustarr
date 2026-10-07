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

// Package advisory is the manager's task-events intake (ADR-0019 §8.2,
// §8.3, §8.5; rulings R8, R10, R16): the leader-only clustarr-task-events
// consumer on CLUSTARR_TASK_EVENTS, which turns JetStream's MSG_NAKED and
// MSG_TERMINATED advisories of every dispatched task into delivery state in
// app/dispatch's DeliveryBook and wakes the CR the task concerns; and the
// core subscriptions on $JS.EVENT.METRIC.CONSUMER.ACK, which feed metrics
// and nothing else.
//
// A naked message is still in its stream, so the intake reads it by stream
// sequence (StreamAdmin.Message) and resolves its subject and envelope
// (app/catalog/history.ResolveDispatch). A terminated one is gone from its
// WorkQueue stream, so natsbus terms with the Clustarr-Id as the reason's
// first field (R10), and the intake resolves that id: from its own index of
// the naks it resolved, else by parsing the Msg-Id shape
// (history.ParseDispatchID) and asking its UIDResolver (ruling A2-2). Only
// the first nak of a dispatch and the nak at MaxDeliver - 1 are recorded;
// the others count as metricsOnly. A terminated task whose dead-letter copy
// the DLQ projector does not report within CopyWait is annotated through the
// projector's code: the second net.
//
// The intake never writes a CR's status (R8): planners render
// Dispatch.delivery from the book. Advisories are history, not state, so
// every one is acked; a lost one leaves delivery stale until the next
// transition.
package advisory
