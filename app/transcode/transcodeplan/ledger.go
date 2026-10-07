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

package transcodeplan

import transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"

// GrantKind is what admission granted a file (loop spec §5.5).
type GrantKind int

// The grants.
const (
	// GrantNone: nothing granted.
	GrantNone GrantKind = iota
	// GrantWindow: a place in the profile's window (§5.11): plan the file.
	GrantWindow
	// GrantDispatch: a slot of Ledger.Class: dispatch the Planned file.
	GrantDispatch
)

// Ledger is one file's admission state as the transcode planner reads it
// (admission.Snapshot is this type). The zero value is "nothing granted".
type Ledger struct {
	Grant      GrantKind
	Class      transcodev1alpha1.Hardware // a dispatch grant's class
	Directive  *Directive
	WaitReason string // the Planned message admission wants written
	WaitCause  string // its reason constant
	Device     Device // the granted class's device
}

// DirectiveKind is what admission directs the planner to do with a file
// already in flight.
type DirectiveKind int

// The directives.
const (
	// DirectiveReroute: withdraw the dispatch at Seq and return the file to
	// Planned (an unschedulable pool or an unusable class, §5.7 step 2).
	DirectiveReroute DirectiveKind = iota + 1
)

// Directive is one admission directive for one file.
type Directive struct {
	Kind    DirectiveKind
	Seq     int64
	Reason  string
	KeepGPU bool // hardware gpu: no fallbackReason, so the file chooses among the GPU classes again
}
