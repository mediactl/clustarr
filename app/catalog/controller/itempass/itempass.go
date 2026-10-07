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

package itempass

import (
	"context"
	"encoding/json"
	"sync"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Contribution is what the item stages decided for the catalogarr apply,
// which each kind's own renderer folds into its one catalogarr status
// (ADR-0019 §7.0). nil fields are "this pass decided nothing: re-send the
// stored value".
type Contribution struct {
	Downloads       *[]catalogv1alpha1.DownloadEntry
	DownloadPhase   *commonv1.DownloadPhase
	DownloadNonces  *catalogv1alpha1.DownloadNonces
	LegacyDownloads *catalogv1alpha1.LegacyDownloads // release N (A8.1)

	// Views on an Episode or Issue, from its container's entries (§6.1).
	// Viewed reports that the stage computed them this pass; then a nil
	// ActiveDownloadRef is "no covering entry" rather than "decided nothing".
	Viewed            bool
	Covering          []catalogv1alpha1.DownloadEntry
	ActiveDownloadRef *string
	DonorOpen         bool
}

// Set is one other field manager's complete set on the item (R5): status
// JSON field names to values; a name mapped to nil is sent as absent.
type Set struct {
	Manager k8s.FieldManager
	Fields  map[string]any
}

// Pass rides the context of one item pass.
type Pass struct {
	Contribution Contribution

	mu       sync.Mutex
	landedRV string
	landed   bool
}

type passKey struct{}

// With returns ctx carrying p.
func With(ctx context.Context, p *Pass) context.Context {
	return context.WithValue(ctx, passKey{}, p)
}

// From is the pass ctx carries; nil outside a staged pass (tests, F4's
// harness), which every helper below treats as "decided nothing".
func From(ctx context.Context) *Pass {
	p, _ := ctx.Value(passKey{}).(*Pass)
	return p
}

// Landed records that the catalogarr apply landed at rv. itemstatus.Apply
// calls it; a nil Pass ignores it.
func (p *Pass) Landed(rv string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.landedRV, p.landed = rv, true
}

// LandedRV is the resourceVersion the catalogarr apply returned, and whether
// it landed this pass.
func (p *Pass) LandedRV() (string, bool) {
	if p == nil {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.landedRV, p.landed
}

// Downloads is the entry list a catalogarr apply sends: the pass's decision
// when it made one, else the stored list.
func Downloads(p *Pass, stored []catalogv1alpha1.DownloadEntry) []catalogv1alpha1.DownloadEntry {
	if p != nil && p.Contribution.Downloads != nil {
		return *p.Contribution.Downloads
	}
	return stored
}

// Phase is the downloadPhase a catalogarr apply sends.
func Phase(p *Pass, stored commonv1.DownloadPhase) commonv1.DownloadPhase {
	if p != nil && p.Contribution.DownloadPhase != nil {
		return *p.Contribution.DownloadPhase
	}
	return stored
}

// Nonces is the downloadNonces a catalogarr apply sends.
func Nonces(p *Pass, stored *catalogv1alpha1.DownloadNonces) *catalogv1alpha1.DownloadNonces {
	if p != nil && p.Contribution.DownloadNonces != nil {
		return p.Contribution.DownloadNonces
	}
	return stored
}

// Legacy is the legacyDownloads a catalogarr apply sends (release N).
func Legacy(p *Pass, stored *catalogv1alpha1.LegacyDownloads) *catalogv1alpha1.LegacyDownloads {
	if p != nil && p.Contribution.LegacyDownloads != nil {
		return p.Contribution.LegacyDownloads
	}
	return stored
}

// ActiveRef is an Episode's or Issue's activeDownloadRef: the covering
// entry's id when the view was computed this pass, else the stored value.
func ActiveRef(p *Pass, stored *string) *string {
	if p != nil && p.Contribution.Viewed {
		return p.Contribution.ActiveDownloadRef
	}
	return stored
}

// Donor reports a covering audio donor entry open, when the view was
// computed this pass; else fallback (the kind's own reading).
func Donor(p *Pass, fallback bool) bool {
	if p != nil && p.Contribution.Viewed {
		return p.Contribution.DonorOpen
	}
	return fallback
}

// HasContribution reports that a stage decided something for the catalogarr
// apply this pass; the deletion path applies status only then.
func HasContribution(p *Pass) bool {
	if p == nil {
		return false
	}
	c := p.Contribution
	return c.Downloads != nil || c.DownloadPhase != nil || c.DownloadNonces != nil || c.LegacyDownloads != nil || c.Viewed
}

// EntryACs renders entries as apply configurations: the one place a
// DownloadEntry becomes one, shared by every kind's renderer so all eight
// declare the same leaves.
func EntryACs(entries []catalogv1alpha1.DownloadEntry) []*catalogac.DownloadEntryApplyConfiguration {
	out := make([]*catalogac.DownloadEntryApplyConfiguration, 0, len(entries))
	for i := range entries {
		out = append(out, EntryAC(&entries[i]))
	}
	return out
}

// NoncesAC renders downloadNonces, nil for none.
func NoncesAC(n *catalogv1alpha1.DownloadNonces) *catalogac.DownloadNoncesApplyConfiguration {
	if n == nil || (n.Remove == "" && n.Resume == "" && n.Import == "" && n.Unblock == "") {
		return nil
	}
	ac := catalogac.DownloadNonces()
	if n.Remove != "" {
		ac = ac.WithRemove(n.Remove)
	}
	if n.Resume != "" {
		ac = ac.WithResume(n.Resume)
	}
	if n.Import != "" {
		ac = ac.WithImport(n.Import)
	}
	if n.Unblock != "" {
		ac = ac.WithUnblock(n.Unblock)
	}
	return ac
}

// EntryAC renders one entry as its apply configuration through JSON: an
// apply configuration has the type's JSON shape with every field a pointer,
// so the round trip sends exactly the fields the entry serialises -- its
// always-written booleans included -- and stays complete as the type grows.
func EntryAC(e *catalogv1alpha1.DownloadEntry) *catalogac.DownloadEntryApplyConfiguration {
	ac := &catalogac.DownloadEntryApplyConfiguration{}
	roundTrip(e, ac)
	return ac
}

// LegacyAC renders legacyDownloads, nil for none.
func LegacyAC(l *catalogv1alpha1.LegacyDownloads) *catalogac.LegacyDownloadsApplyConfiguration {
	if l == nil {
		return nil
	}
	ac := &catalogac.LegacyDownloadsApplyConfiguration{}
	roundTrip(l, ac)
	return ac
}

// roundTrip copies from into an apply configuration of the same JSON shape.
// A typed API struct always marshals, and its own JSON always decodes into
// its apply configuration, so a failure is a programming error.
func roundTrip(from, into any) {
	b, err := json.Marshal(from)
	if err != nil {
		panic("itempass: marshal " + err.Error())
	}
	if err := json.Unmarshal(b, into); err != nil {
		panic("itempass: unmarshal " + err.Error())
	}
}
