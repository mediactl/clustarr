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

import "context"

// Tick runs one leader tick at the ledger's clock (Start runs one every
// TickInterval of real time), so a test drives unattended detection with a
// fake clock.
func (l *Ledger) Tick(ctx context.Context) { l.tick(ctx) }

// Rebuild is Start's rebuild without its loop: it reads every registered
// Source once and marks the ledger rebuilt.
func (l *Ledger) Rebuild(ctx context.Context) error { return l.rebuild(ctx) }
