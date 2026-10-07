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

package history_test

import (
	"context"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

type testMessage struct {
	env     *events.Envelope
	subject string
}

func (m testMessage) Envelope() *events.Envelope             { return m.env }
func (m testMessage) Subject() string                        { return m.subject }
func (testMessage) Attempt() uint64                          { return 1 }
func (testMessage) Ack(context.Context) error                { return nil }
func (testMessage) Nak(context.Context, time.Duration) error { return nil }
func (testMessage) Term(context.Context, string) error       { return nil }
func (testMessage) InProgress(context.Context) error         { return nil }
