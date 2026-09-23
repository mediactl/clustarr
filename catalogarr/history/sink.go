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

package history

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// SinkDeps is everything the history sink needs from the process around it.
type SinkDeps struct {
	// Recorder writes events.k8s.io/v1 Events. mgr.GetEventRecorder returns
	// this type directly -- see run.go's package doc for why that, and not
	// the deprecated GetEventRecorderFor, is the one recorder convention in
	// this tree.
	Recorder k8sevents.EventRecorder
}

// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Sink is the catalogarr-history consumer: it projects each domain event on
// clustarr.evt.> onto an events.k8s.io/v1 Event regarding the CR the event
// concerns. It never writes spec, status or an annotation -- see
// [DLQProjector] and [Replayer] for the writes this package makes (both
// annotations only), and ruling R1 for why they are not here either.
type Sink struct {
	Deps SinkDeps
}

// NewSink returns a Sink over d.
func NewSink(d SinkDeps) *Sink { return &Sink{Deps: d} }

// Subscription is the catalogarr-history durable consumer from §5's table,
// read from events.Default() rather than restated so the tuning lives in one
// place (AckWait 30s, MaxDeliver 3, BackOff 5s/30s, MaxAckPending 512 --
// CLUSTARR_EVENTS is a high-volume firehose whose consumer must never fall
// meaningfully behind the producers).
func (s *Sink) Subscription() events.Subscription {
	spec, ok := events.Default().Consumer(events.ConsumerCatalogHistory)
	if !ok {
		// Unreachable: ConsumerCatalogHistory is in defaultConsumers(). A
		// zero Subscription fails Validate loudly at Subscribe time rather
		// than silently consuming nothing.
		return events.Subscription{}
	}
	return spec.Subscription()
}

// SetupWithManager registers the subscription as a manager.Runnable so it
// starts with the manager and drains on shutdown.
//
// It is a k8s.EveryReplica rather than a manager.RunnableFunc: catalogarr's
// controller,worker,history Deployment runs every RoleHistory replica behind
// the same leader lease its controllers use, and a bare RunnableFunc has no
// NeedLeaderElection method, so controller-runtime would put it behind that
// lease -- exactly one replica would ever drain CLUSTARR_EVENTS, however many
// were scaled up. See k8s.EveryReplica's doc comment; catalogarr/worker/
// rssmatcher.Handler.SetupWithManager hit this first.
func (s *Sink) SetupWithManager(mgr ctrl.Manager, bus events.Bus) error {
	return mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, s.Subscription(), s.Handle)
		if err != nil {
			return fmt.Errorf("catalogarr: subscribe history: %w", err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	}))
}

// Handle projects one domain event onto an Event. A schema this package does
// not recognise, or a CR it cannot resolve, is logged and acknowledged: the
// firehose keeps flowing, and there is nothing actionable to retry -- the
// message will never decode differently on redelivery.
func (s *Sink) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	// Extract before Start, so this span continues the producer's trace
	// rather than beginning a new root per event.
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "history.Sink.Handle")
	defer span.End()
	log := logging.FromContext(ctx)

	action, note, ok := describeEvent(env)
	if !ok {
		log.Warn("history: unrecognised domain event schema; skipping",
			"schema", env.Schema)
		return nil
	}

	target := Resolve(env)
	if !target.KindKnown() {
		log.Warn("history: could not resolve the CR a domain event concerns; skipping",
			"schema", env.Schema, "key", env.Key)
		return nil
	}

	s.Deps.Recorder.Eventf(target.object(), nil, eventType(action), reasonFor(action), env.Type, note)
	return nil
}

// describeEvent decodes env into whichever of the eight known evt payload
// types it names and returns the domain action plus a short, human-readable
// note. ok is false for any schema not in this switch, which mirrors the
// resolvers map in target.go exactly -- both lists are "every evt.* payload
// this package has read the struct for".
//
// The note may freely carry title, path or release-name text: CLAUDE.md's
// "never label or name Events by title, path or release name" is about
// bounded-cardinality identifiers (Prometheus labels, object names), and an
// Event's free-text Note is neither -- Kubernetes Events already carry
// exactly this kind of detail (see any kubelet image-pull event).
func describeEvent(env *events.Envelope) (action, note string, ok bool) {
	switch env.Schema {
	case schema.ItemEvent{}.Schema():
		var p schema.ItemEvent
		if err := schema.Decode(p.Schema(), env.Data, &p); err != nil {
			return "", "", false
		}
		return p.Action, fmt.Sprintf("catalog item %s (title=%q year=%d monitored=%t)",
			p.Action, p.Title, p.Year, p.Monitored), true

	case schema.ReleaseEvent{}.Schema():
		var p schema.ReleaseEvent
		if err := schema.Decode(p.Schema(), env.Data, &p); err != nil {
			return "", "", false
		}
		return p.Action, fmt.Sprintf("release %s (indexer=%q releaseGroup=%q formatScore=%d)",
			p.Action, p.Indexer, p.ReleaseGroup, p.FormatScore), true

	case schema.MediaFileEvent{}.Schema():
		var p schema.MediaFileEvent
		if err := schema.Decode(p.Schema(), env.Data, &p); err != nil {
			return "", "", false
		}
		return p.Action, fmt.Sprintf("media file %s (path=%q reason=%q sizeBytes=%d)",
			p.Action, p.ImportedPath, p.Reason, p.SizeBytes), true

	case schema.ImportListSynced{}.Schema():
		var p schema.ImportListSynced
		if err := schema.Decode(p.Schema(), env.Data, &p); err != nil {
			return "", "", false
		}
		note := fmt.Sprintf("import list synced (fetched=%d added=%d removed=%d skipped=%d)",
			p.Fetched, p.Added, p.Removed, p.Skipped)
		if p.Error != "" {
			return events.ActionFailed, note + fmt.Sprintf(" error=%q", p.Error), true
		}
		return events.ActionSynced, note, true

	case schema.DownloadEvent{}.Schema():
		var p schema.DownloadEvent
		if err := schema.Decode(p.Schema(), env.Data, &p); err != nil {
			return "", "", false
		}
		return p.Action, fmt.Sprintf("download %s (title=%q protocol=%q sizeBytes=%d reason=%q)",
			p.Action, p.Title, p.Protocol, p.SizeBytes, p.Reason), true

	case schema.JobEvent{}.Schema():
		var p schema.JobEvent
		if err := schema.Decode(p.Schema(), env.Data, &p); err != nil {
			return "", "", false
		}
		return p.Action, fmt.Sprintf("transcode job %s (mode=%q encoder=%q reason=%q)",
			p.Action, p.Mode, p.Encoder, p.Reason), true

	case schema.SubtitleEvent{}.Schema():
		var p schema.SubtitleEvent
		if err := schema.Decode(p.Schema(), env.Data, &p); err != nil {
			return "", "", false
		}
		return p.Action, fmt.Sprintf("subtitle %s (lang=%q provider=%q score=%d reason=%q)",
			p.Action, p.LangKey, p.Provider, p.Score, p.Reason), true

	case schema.IndexerEvent{}.Schema():
		var p schema.IndexerEvent
		if err := schema.Decode(p.Schema(), env.Data, &p); err != nil {
			return "", "", false
		}
		return p.Action, fmt.Sprintf("indexer %s (reason=%q failures=%d)",
			p.Action, p.Reason, p.Failures), true

	default:
		return "", "", false
	}
}

// warningActions are the domain actions that read as a problem rather than a
// routine transition. Everything else is corev1.EventTypeNormal.
var warningActions = map[string]bool{
	events.ActionRejected:    true,
	events.ActionFailed:      true,
	events.ActionBlocklisted: true,
	events.ActionDisabled:    true,
	events.ActionLimited:     true,
	events.ActionRemoved:     true,
	events.ActionSkipped:     true,
	events.ActionDeleted:     true,
}

func eventType(action string) string {
	if warningActions[action] {
		return corev1.EventTypeWarning
	}
	return corev1.EventTypeNormal
}

// reasonFor renders a domain action as a Kubernetes Event Reason: a single
// PascalCase word, matching the k8s.ReasonReconciled-style convention every
// controller in this tree already uses.
func reasonFor(action string) string {
	if action == "" {
		return "Unknown"
	}
	return strings.ToUpper(action[:1]) + action[1:]
}
