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

package lifecycle

import catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"

// BookOf is what the stage's command book holds for e once a pass issued
// its dispatch seq: the seq and the hash of e's desired state under v.
// Tests seed View.Issued with it to start from an owner in its steady state
// (CLAUDE.md: exercise a path against an object that already has status).
func BookOf(v View, e *catalogv1alpha1.DownloadEntry) IssuedCommand {
	d := &decider{v: v, now: v.Now.UTC(), clients: map[string]Client{}}
	for _, c := range v.Clients {
		d.clients[c.Name] = c
	}
	d.plan.Nonces = v.Nonces
	return IssuedCommand{Seq: e.Dispatch.Seq, Hash: hashOf(d.desired(e))}
}
