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

package remediation

import (
	"time"

	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/dispatch"
)

// itemAdmitter is the ledger bound to one item key: what a kind's own
// publish (the metadata refresh, ruling R23) is admitted through.
type itemAdmitter struct {
	ledger *dispatch.Ledger
	key    dispatch.Key
}

var _ itempass.Admitter = itemAdmitter{}

// Admit implements itempass.Admitter.
func (a itemAdmitter) Admit(durable string, seq int64) (bool, time.Duration) {
	d := a.ledger.Admit(durable, a.key, seq)
	if !d.Admitted {
		a.ledger.Waiting(durable, a.key, d.Reason)
	}
	return d.Admitted, d.RetryAfter
}

// Published implements itempass.Admitter.
func (a itemAdmitter) Published(durable string, seq int64) { a.ledger.Published(durable, a.key, seq) }

// admitterFor is k's admitter, nil without a ledger (everything admitted).
func (ir *ItemReconciler) admitterFor(k Key) itempass.Admitter {
	if ir.Dispatch == nil {
		return nil
	}
	return itemAdmitter{ledger: ir.Dispatch, key: ItemLedgerKey(k)}
}

// ItemLedgerKey is an item key's dispatch.Key: its object Kind, namespace
// and name. The item stages that admit on their own durables (metadata,
// artwork fetch, overlay render) key their slots by it too.
func ItemLedgerKey(k Key) dispatch.Key {
	return dispatch.Key{Kind: string(k.Kind), Namespace: k.Namespace, Name: k.Name}
}
