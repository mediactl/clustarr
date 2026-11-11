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

// Package lineup plans an IPTVProvider's custom lineup: it classifies a
// playlist's entries into the categories of the provider's rules, drops what
// the rules leave out, keeps one entry of each duplicated channel and numbers
// every category alphabetically from its base. It is pure: no client, no
// store, no clock -- the caller reduces its playlist entries to Entry and
// reads the Plan back. It reads the API's spec.lineup through FromSpec only.
package lineup

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// DefaultMaxChannels is a category's size limit when Spec.MaxChannels is 0.
const DefaultMaxChannels = 400

// Dropped reasons.
const (
	ReasonExcluded  = "excluded"
	ReasonDuplicate = "duplicate"
	ReasonUnmatched = "unmatched"
	ReasonOff       = "off"
	ReasonTooMany   = "tooMany"
)

// Rule puts the entries it matches into Category. Base is the category's
// first number less one, 0 for none: the first rule of a category that
// carries one sets it (the CRD admits it on any rule of the name), and a
// later rule's is ignored.
type Rule struct {
	Category string
	Base     int
	// Groups lists the playlist groups the rule takes, exactly; empty is any.
	Groups []string
	// Names are case-insensitive regexps over the trimmed name; empty is any.
	Names []string
	// Exclude are case-insensitive regexps over the trimmed name and the group.
	Exclude []string
}

// Spec is a lineup's rules.
type Spec struct {
	// Trim are regexps removed, in order, from every raw name.
	Trim []string
	// Exclude are case-insensitive regexps over the trimmed name and the
	// group; a match leaves the entry out of every category.
	Exclude     []string
	Rules       []Rule
	MaxChannels int // 0 = DefaultMaxChannels
}

// Override is a person's spec.category and spec.name on one channel.
type Override struct {
	// Category, when set, moves the channel to that category; "" turns it off.
	Category *string
	// Name replaces the trimmed name for sorting and display.
	Name string
}

// Entry is the part of a playlist entry the planner reads.
type Entry struct {
	Key, Name, Group, Logo string
}

// Assignment is one channel of the lineup.
type Assignment struct {
	Key, Name, Group, Category, Rule, Number string
	Base, Index                              int
}

// Category is one category of the plan, in rule order.
type Category struct {
	Name           string
	Base, Channels int
	TooMany        bool
}

// Dropped is an entry the lineup does not carry, and why.
type Dropped struct {
	Key, Name, Reason, DuplicateOf string
}

// Plan is the planner's answer.
type Plan struct {
	Channels   []Assignment
	Categories []Category
	Dropped    []Dropped
}

// FromSpec is an IPTVProvider's spec.lineup as the planner's Spec, each
// category a Rule in order with its own base, if any (Compile takes the
// first rule of a name that carries one). maxChannels is the provider's
// spec.dvr.maxChannels. A nil s is a Spec with no rules.
func FromSpec(s *clustarrv1.LineupSpec, maxChannels int) Spec {
	out := Spec{MaxChannels: maxChannels}
	if s == nil {
		return out
	}
	out.Trim, out.Exclude = s.Trim, s.Exclude
	for _, c := range s.Categories {
		r := Rule{Category: c.Name, Groups: c.Groups, Names: c.Names, Exclude: c.Exclude}
		if c.Base != nil {
			r.Base = int(*c.Base)
		}
		out.Rules = append(out.Rules, r)
	}
	return out
}

type compiledRule struct {
	Rule
	label   string
	names   []*regexp.Regexp
	exclude []*regexp.Regexp
}

// Planner is a compiled Spec.
type Planner struct {
	trim    []*regexp.Regexp
	exclude []*regexp.Regexp
	rules   []compiledRule
	max     int
	cats    []Category // in rule order, first occurrence
	catIdx  map[string]int
}

func ci(p string) (*regexp.Regexp, error) { return regexp.Compile("(?i)" + p) }

func compileAll(pats []string, insensitive bool, what string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(pats))
	for _, p := range pats {
		var re *regexp.Regexp
		var err error
		if insensitive {
			re, err = ci(p)
		} else {
			re, err = regexp.Compile(p)
		}
		if err != nil {
			return nil, fmt.Errorf("%s pattern %q: %w", what, p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// Compile compiles every pattern of s once. An error names the rule (or the
// spec's list) and the pattern, or the two categories whose numbers could
// meet: each numbers up to MaxChannels channels from its base+1, so two
// bases closer than that are refused.
func Compile(s Spec) (*Planner, error) {
	p := &Planner{max: s.MaxChannels, catIdx: map[string]int{}}
	if p.max <= 0 {
		p.max = DefaultMaxChannels
	}
	var err error
	if p.trim, err = compileAll(s.Trim, false, "trim"); err != nil {
		return nil, err
	}
	if p.exclude, err = compileAll(s.Exclude, true, "exclude"); err != nil {
		return nil, err
	}
	for i, r := range s.Rules {
		if strings.TrimSpace(r.Category) == "" {
			return nil, fmt.Errorf("rule %d has no category", i)
		}
		cr := compiledRule{Rule: r, label: fmt.Sprintf("%s[%d]", r.Category, i)}
		what := fmt.Sprintf("rule %q (%d) names", r.Category, i)
		if cr.names, err = compileAll(r.Names, true, what); err != nil {
			return nil, err
		}
		what = fmt.Sprintf("rule %q (%d) exclude", r.Category, i)
		if cr.exclude, err = compileAll(r.Exclude, true, what); err != nil {
			return nil, err
		}
		p.rules = append(p.rules, cr)
		idx, ok := p.catIdx[r.Category]
		if !ok {
			idx = len(p.cats)
			p.catIdx[r.Category] = idx
			p.cats = append(p.cats, Category{Name: r.Category})
		}
		// The first rule of the name that carries a base sets it: a category
		// whose first rule has none numbers from a later rule's, never from 1.
		if p.cats[idx].Base == 0 && r.Base != 0 {
			p.cats[idx].Base = r.Base
		}
	}
	if err := overlapping(p.cats, p.max); err != nil {
		return nil, err
	}
	return p, nil
}

// overlapping refuses two categories whose numbers could meet: by base, each
// must end (base + maxChannels) at or before the next one's base.
func overlapping(cats []Category, maxChannels int) error {
	byBase := slices.Clone(cats)
	sort.SliceStable(byBase, func(i, j int) bool { return byBase[i].Base < byBase[j].Base })
	for i := 1; i < len(byBase); i++ {
		a, b := byBase[i-1], byBase[i]
		if a.Base+maxChannels > b.Base {
			return fmt.Errorf("categories %q (base %d) and %q (base %d) overlap: each numbers up to %d channels from its base+1, so their bases must be at least %d apart",
				a.Name, a.Base, b.Name, b.Base, maxChannels, maxChannels)
		}
	}
	return nil
}

// TrimName applies the trim patterns in order, each replaced by "", then
// collapses doubled spaces and trims the space around the result.
func TrimName(name string, trim []*regexp.Regexp) string {
	for _, re := range trim {
		name = re.ReplaceAllString(name, "")
	}
	return strings.Join(strings.Fields(name), " ")
}

// Trim is name as the planner trims it: the spec's trim patterns applied
// by TrimName.
func (p *Planner) Trim(name string) string { return TrimName(name, p.trim) }

var resolutionRE = []struct {
	re   *regexp.Regexp
	rank int
}{
	{regexp.MustCompile(`(?i)(^|[^a-z0-9])(4k|2160p)($|[^a-z0-9])`), 4},
	{regexp.MustCompile(`(?i)(^|[^a-z0-9])1080[pi]($|[^a-z0-9])`), 3},
	{regexp.MustCompile(`(?i)(^|[^a-z0-9])720p($|[^a-z0-9])`), 2},
	{regexp.MustCompile(`(?i)(^|[^a-z0-9])480p($|[^a-z0-9])`), 1},
}

// resolution ranks the marker the raw name carries: 4K > 1080p > 720p > 480p > none.
func resolution(raw string) int {
	for _, r := range resolutionRE {
		if r.re.MatchString(raw) {
			return r.rank
		}
	}
	return 0
}

func anyMatch(res []*regexp.Regexp, ss ...string) bool {
	for _, re := range res {
		for _, s := range ss {
			if re.MatchString(s) {
				return true
			}
		}
	}
	return false
}

func (r *compiledRule) matches(trimmed, group string) bool {
	if len(r.Groups) > 0 {
		ok := false
		for _, g := range r.Groups {
			if g == group {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(r.names) > 0 && !anyMatch(r.names, trimmed) {
		return false
	}
	return !anyMatch(r.exclude, trimmed, group)
}

func (r *compiledRule) groupIndex(group string) int {
	for i, g := range r.Groups {
		if g == group {
			return i
		}
	}
	return len(r.Groups)
}

type candidate struct {
	e        Entry
	order    int
	trimmed  string
	display  string
	norm     string
	res      int
	category string
	rule     string
	groupIdx int
}

func normOf(s string) string {
	if n := release.TitleNorm(s); n != "" {
		return n
	}
	return strings.ToLower(s)
}

// Plan classifies entries. overrides is keyed by entry Key.
func (p *Planner) Plan(entries []Entry, overrides map[string]Override) Plan {
	var plan Plan
	var cands []*candidate
	for i, e := range entries {
		trimmed := TrimName(e.Name, p.trim)
		ov, hasOv := overrides[e.Key]
		if hasOv && ov.Category != nil && *ov.Category == "" {
			plan.Dropped = append(plan.Dropped, Dropped{Key: e.Key, Name: trimmed, Reason: ReasonOff})
			continue
		}
		c := &candidate{e: e, order: i, trimmed: trimmed, display: trimmed, res: resolution(e.Name)}
		if hasOv && ov.Name != "" {
			c.display = ov.Name
		}
		if hasOv && ov.Category != nil {
			// A person's pick wins over the rules, the spec's exclude included.
			if _, ok := p.catIdx[*ov.Category]; !ok {
				plan.Dropped = append(plan.Dropped, Dropped{Key: e.Key, Name: trimmed, Reason: ReasonUnmatched})
				continue
			}
			c.category, c.rule, c.groupIdx = *ov.Category, "override", 0
		} else {
			if anyMatch(p.exclude, trimmed, e.Group) {
				plan.Dropped = append(plan.Dropped, Dropped{Key: e.Key, Name: trimmed, Reason: ReasonExcluded})
				continue
			}
			found := false
			for j := range p.rules {
				r := &p.rules[j]
				if r.matches(trimmed, e.Group) {
					c.category, c.rule, c.groupIdx = r.Category, r.label, r.groupIndex(e.Group)
					found = true
					break
				}
			}
			if !found {
				plan.Dropped = append(plan.Dropped, Dropped{Key: e.Key, Name: trimmed, Reason: ReasonUnmatched})
				continue
			}
		}
		c.norm = normOf(trimmed)
		cands = append(cands, c)
	}

	// Duplicates: one entry of each trimmed name.
	best := map[string]*candidate{}
	for _, c := range cands {
		b, ok := best[c.norm]
		if !ok || better(c, b) {
			best[c.norm] = c
		}
	}
	kept := cands[:0:0]
	for _, c := range cands {
		w := best[c.norm]
		if w != c {
			plan.Dropped = append(plan.Dropped, Dropped{Key: c.e.Key, Name: c.trimmed, Reason: ReasonDuplicate, DuplicateOf: w.e.Key})
			continue
		}
		kept = append(kept, c)
	}

	byCat := map[string][]*candidate{}
	for _, c := range kept {
		byCat[c.category] = append(byCat[c.category], c)
	}
	plan.Categories = make([]Category, len(p.cats))
	copy(plan.Categories, p.cats)
	for i := range plan.Categories {
		cat := &plan.Categories[i]
		list := byCat[cat.Name]
		sort.SliceStable(list, func(a, b int) bool {
			na, nb := normOf(list[a].display), normOf(list[b].display)
			if na != nb {
				return na < nb
			}
			if list[a].display != list[b].display {
				return list[a].display < list[b].display
			}
			return list[a].order < list[b].order
		})
		cat.Channels = len(list)
		if len(list) > p.max {
			cat.TooMany = true
			for _, c := range list {
				plan.Dropped = append(plan.Dropped, Dropped{Key: c.e.Key, Name: c.display, Reason: ReasonTooMany})
			}
			continue
		}
		for idx, c := range list {
			plan.Channels = append(plan.Channels, Assignment{
				Key: c.e.Key, Name: c.display, Group: c.e.Group, Category: cat.Name, Rule: c.rule,
				Number: strconv.Itoa(cat.Base + 1 + idx), Base: cat.Base, Index: idx,
			})
		}
	}
	return plan
}

// better reports whether a beats b as the survivor of a duplicate: higher
// resolution, then the earlier group of its rule's list, then playlist order.
func better(a, b *candidate) bool {
	if a.res != b.res {
		return a.res > b.res
	}
	if a.groupIdx != b.groupIdx {
		return a.groupIdx < b.groupIdx
	}
	return a.order < b.order
}
