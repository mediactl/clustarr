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
package rescan

import (
	"context"
	"time"

	"github.com/mediactl/clustarr/app/import/manager/scanapply"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// ApplyingBus is bus with every library-scan observation published on it
// handed to the manager's import-intake-scan consumer, over applier, once
// the publish lands (ADR-0019 §7.6: the walk reports, the applier writes).
// A test that drives Handle through it sees, when Handle returns, what the
// walk and the applier did together -- the items and MediaFiles the walk
// used to write itself before A6.1.
func ApplyingBus(bus events.Bus, applier *scanapply.Applier) events.Bus {
	return applyingBus{Bus: bus, intake: &intake.ScanConsumer{Bus: bus, Applier: applier}}
}

type applyingBus struct {
	events.Bus
	intake *intake.ScanConsumer
}

func (b applyingBus) Publish(ctx context.Context, subject string, e *events.Envelope, opts ...events.PublishOption) (events.Receipt, error) {
	r, err := b.Bus.Publish(ctx, subject, e, opts...)
	if err != nil || e.Schema != (schema.ScanObservation{}).Schema() {
		return r, err
	}
	return r, b.intake.Handle(ctx, envelopeMessage{env: e})
}

// envelopeMessage is the delivery of one envelope, for the consumer's
// Handle.
type envelopeMessage struct{ env *events.Envelope }

func (m envelopeMessage) Envelope() *events.Envelope               { return m.env }
func (m envelopeMessage) Subject() string                          { return "" }
func (m envelopeMessage) Attempt() uint64                          { return 1 }
func (m envelopeMessage) Ack(context.Context) error                { return nil }
func (m envelopeMessage) Nak(context.Context, time.Duration) error { return nil }
func (m envelopeMessage) Term(context.Context, string) error       { return nil }
func (m envelopeMessage) InProgress(context.Context) error         { return nil }
