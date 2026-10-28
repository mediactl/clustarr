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
	"errors"

	"github.com/prometheus/client_golang/prometheus"

	usenetclient "github.com/mediactl/clustarr/pkg/download/usenet"
	"github.com/mediactl/clustarr/pkg/par2child"
)

// newRepairer is the engine's repairer: par2go in a child of this binary,
// "agent par2-repair" (split spec 2026-10-06 §8.4, §8.6), never in the
// engine's own process. A variable for one test.
var newRepairer = func() usenetclient.Repairer {
	return &usenetclient.LibraryRepairer{Runner: par2child.Exec{}, FileThreads: par2FileThreads}
}

// par2FileThreads is how many of a set's files par2 verifies at once. A
// verify hashes each file with MD5, one core apiece, and par2go's default
// of two left most of talos-88's sixteen idle once the working area moved
// onto its NVMe (owner, 2026-10-28), where reads no longer limit it.
const par2FileThreads = 8

// registerUnreaped exports par2child.Unreaped as
// clustarr_par2_unreaped_children; a registry that has it already is fine.
// It never gates readiness: a read stuck in the kernel is not a fault a
// restart cures.
func registerUnreaped(r prometheus.Registerer) error {
	err := r.Register(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "clustarr_par2_unreaped_children",
		Help: "par2 repair children still not reaped after their deadline and grace (a read blocked in the kernel).",
	}, func() float64 { return float64(par2child.Unreaped()) }))
	var are prometheus.AlreadyRegisteredError
	if errors.As(err, &are) {
		return nil
	}
	return err
}
