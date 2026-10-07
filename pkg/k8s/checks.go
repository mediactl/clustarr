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

package k8s

import (
	"fmt"
	"regexp"
	"slices"

	"sigs.k8s.io/controller-runtime/pkg/healthz"
)

// checkName is a process check ("cache") or a domain's ("import.data").
var checkName = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)?$`)

// Checks is an ordered set of named health checks: what one process, or one
// agent domain, adds to /readyz or /healthz (spec §3.3). Its zero value is an
// empty set. A name is a process check, registered once per process (ping,
// jetstream, cache, ffgo), or <domain>.<check>, so a check's name says which
// registration owns it; controller-runtime overwrites a duplicate silently,
// so Add refuses one.
type Checks struct {
	entries []checkEntry
}

type checkEntry struct {
	name  string
	check healthz.Checker
}

// NewChecks returns an empty set.
func NewChecks() *Checks { return &Checks{} }

// Add appends a check. It refuses an empty or malformed name, a nil checker
// and a name already in the set.
func (c *Checks) Add(name string, check healthz.Checker) error {
	if !checkName.MatchString(name) {
		return fmt.Errorf("k8s: %q is not a check name: want <name> or <domain>.<name>, lower-case kebab", name)
	}
	if check == nil {
		return fmt.Errorf("k8s: check %q has no checker", name)
	}
	if c.Get(name) != nil {
		return fmt.Errorf("k8s: check %q is registered twice", name)
	}
	c.entries = append(c.entries, checkEntry{name: name, check: check})
	return nil
}

// Merge adds every check of o, in o's order. A nil o adds nothing.
func (c *Checks) Merge(o *Checks) error {
	if o == nil {
		return nil
	}
	for _, e := range o.entries {
		if err := c.Add(e.name, e.check); err != nil {
			return err
		}
	}
	return nil
}

// Names are the set's names in the order they were added.
func (c *Checks) Names() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e.name)
	}
	return out
}

// Get is the named check, or nil.
func (c *Checks) Get(name string) healthz.Checker {
	if c == nil {
		return nil
	}
	if i := slices.IndexFunc(c.entries, func(e checkEntry) bool { return e.name == name }); i >= 0 {
		return c.entries[i].check
	}
	return nil
}
