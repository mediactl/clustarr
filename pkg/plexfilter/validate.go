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

package plexfilter

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"path"
	"slices"
	"strings"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// maxRules and maxMembers are the CRD's caps.
const (
	maxRules   = 32
	maxMembers = 1000
)

// Validate checks a spec before the UI writes it: everything the CRD's CEL
// rules check, plus a known field, an operator its type takes, a value
// where one is needed, and no member twice. Every problem is reported,
// each led by its path (filter.rules[1].rules[0], members.items[3]).
func Validate(spec catalogv1.PlexCollectionSpec) error {
	var errs []error
	fail := func(p, format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: "+format, append([]any{p}, a...)...))
	}

	if strings.TrimSpace(spec.Library) == "" {
		fail("library", "required")
	}
	if strings.TrimSpace(spec.Title) == "" {
		fail("title", "required")
	}
	switch spec.ItemType {
	case catalogv1.PlexItemMovie, catalogv1.PlexItemShow, catalogv1.PlexItemSeason, catalogv1.PlexItemEpisode:
	default:
		fail("itemType", "%q is not movie, show, season or episode", spec.ItemType)
	}

	switch {
	case spec.Filter != nil && spec.Members != nil:
		fail("filter and members", "a collection is smart (filter) or normal (members), not both")
	case spec.Filter == nil && spec.Members == nil:
		fail("filter or members", "a collection needs a filter or members")
	}
	if spec.Filter != nil {
		validateFilter(spec.ItemType, spec.Filter, fail)
	}
	if spec.Filter == nil && spec.Sort != nil {
		fail("sort", "belongs to a smart collection")
	}
	if spec.Filter != nil && spec.Sort != nil && !slices.ContainsFunc(Sorts(spec.ItemType), func(s Sort) bool { return s.Key == spec.Sort.Field }) {
		fail("sort.field", "a %s collection sorts by none of %q", spec.ItemType, spec.Sort.Field)
	}
	if spec.Filter == nil && spec.Limit != nil {
		fail("limit", "belongs to a smart collection")
	}
	if spec.Limit != nil && *spec.Limit < 1 {
		fail("limit", "must be at least 1")
	}
	if spec.Members != nil {
		validateMembers(spec.ItemType, spec.Members, fail)
	}
	if s := spec.Settings; s != nil {
		switch s.Mode {
		case "", catalogv1.PlexModeDefault, catalogv1.PlexModeHideCollection, catalogv1.PlexModeHideItems, catalogv1.PlexModeShowCollectionAndItems:
		default:
			fail("settings.mode", "%q is not a collection mode", s.Mode)
		}
		switch s.Order {
		case "":
		case catalogv1.PlexOrderReleaseDate, catalogv1.PlexOrderAlphabetical:
			if spec.Members == nil {
				fail("settings.order", "belongs to a normal collection; a smart one is ordered by its sort")
			}
		default:
			fail("settings.order", "%q is not releaseDate or alphabetical", s.Order)
		}
	}
	return errors.Join(errs...)
}

func validateFilter(t catalogv1.PlexItemType, f *catalogv1.PlexFilter, fail func(string, string, ...any)) {
	switch f.Match {
	case "", catalogv1.PlexMatchAll, catalogv1.PlexMatchAny:
	default:
		fail("filter.match", "%q is not all or any", f.Match)
	}
	switch n := len(f.Rules); {
	case n == 0:
		fail("filter.rules", "a filter needs a rule")
	case n > maxRules:
		fail("filter.rules", "at most %d rules, not %d", maxRules, n)
	}
	for i, r := range f.Rules {
		p := fmt.Sprintf("filter.rules[%d]", i)
		condition := r.Field != "" || r.Operator != "" || r.Value != ""
		group := r.Match != "" || len(r.Rules) > 0
		switch {
		case condition && group:
			fail(p, "a rule is a condition (field, operator) or a group (match, rules), not both")
		case group:
			if r.Match != catalogv1.PlexMatchAll && r.Match != catalogv1.PlexMatchAny {
				fail(p+".match", "%q is not all or any", r.Match)
			}
			switch n := len(r.Rules); {
			case n == 0:
				fail(p+".rules", "a group needs a rule")
			case n > maxRules:
				fail(p+".rules", "at most %d rules, not %d", maxRules, n)
			}
			for j, c := range r.Rules {
				validateCondition(t, fmt.Sprintf("%s.rules[%d]", p, j), c, fail)
			}
		default:
			validateCondition(t, p, catalogv1.PlexCondition{Field: r.Field, Operator: r.Operator, Value: r.Value}, fail)
		}
	}
}

func validateCondition(t catalogv1.PlexItemType, p string, c catalogv1.PlexCondition, fail func(string, string, ...any)) {
	f, ok := Lookup(t, c.Field)
	if !ok {
		fail(p, "%q is not a field a %s collection filters on", c.Field, t)
		return
	}
	if !takes(f.Type, c.Operator) {
		fail(p, "%s (%s) takes no operator %q", f.Title, f.Type, c.Operator)
		return
	}
	if c.Operator != catalogv1.PlexOpIsTrue && c.Operator != catalogv1.PlexOpIsFalse && strings.TrimSpace(c.Value) == "" {
		fail(p, "%s %s needs a value", f.Title, OperatorLabel(c.Operator))
	}
}

func validateMembers(t catalogv1.PlexItemType, m *catalogv1.PlexMembers, fail func(string, string, ...any)) {
	want := map[catalogv1.PlexItemType]string{catalogv1.PlexItemMovie: "Movie", catalogv1.PlexItemShow: "Series"}[t]
	if want == "" {
		fail("members", "a normal collection holds movies or shows, not %s", t)
	}
	switch n := len(m.Items); {
	case n == 0:
		fail("members.items", "a normal collection needs a member")
	case n > maxMembers:
		fail("members.items", "at most %d members, not %d", maxMembers, n)
	}
	seen := map[catalogv1.PlexMember]int{}
	for i, it := range m.Items {
		p := fmt.Sprintf("members.items[%d]", i)
		if want != "" && it.Kind != want {
			fail(p, "a %s collection's members are %s, not %s", t, want, it.Kind)
		}
		if strings.TrimSpace(it.Name) == "" {
			fail(p, "name required")
		}
		if j, dup := seen[it]; dup {
			fail(p, "duplicate of members.items[%d]", j)
			continue
		}
		seen[it] = i
	}
}

// ImageURLAllowed is the design's image URL rule as far as it can be
// judged without resolving a name: https to a host that is no private,
// loopback or link-local address and no cluster or single-label name, or
// anything under uiArtBase, clustarr UI's own /art/ at its external URL.
// The UI's own host is internal, so outside /art/ it is refused too.
// cluster-plex judges again after resolving.
func ImageURLAllowed(raw, uiArtBase string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not a URL", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%q: only http and https are fetched", raw)
	}
	if b, err := url.Parse(uiArtBase); uiArtBase != "" && err == nil && strings.EqualFold(u.Host, b.Host) {
		base := strings.TrimSuffix(b.Path, "/") + "/"
		if u.Scheme == b.Scheme && strings.HasPrefix(path.Clean(u.Path), base) {
			return nil
		}
		return fmt.Errorf("%q: clustarr's own host serves images only under %s", raw, base)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("%q: an image outside clustarr's /art/ must be https", raw)
	}
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		if !publicIP(ip.Unmap()) {
			return fmt.Errorf("%q: %s is not a public address", raw, host)
		}
		return nil
	}
	if host == "localhost" || !strings.Contains(host, ".") || strings.HasSuffix(host, ".svc") ||
		strings.HasSuffix(host, ".cluster.local") || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("%q: %s is not a public host", raw, host)
	}
	return nil
}

// notPublic are ranges IsGlobalUnicast admits that are no public host:
// "this network", shared (CGNAT), IETF protocol assignments, the
// documentation and benchmarking nets, future use, IPv6 documentation,
// local-use NAT64 and Teredo. cluster-plex's image fetcher holds every
// dialled address to the same ranges (its pkg/plex/collections).
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"),
}

// Translated IPv6 is judged by the IPv4 it carries.
var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

func publicIP(ip netip.Addr) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(ip) {
			return false
		}
	}
	b := ip.As16()
	switch {
	case nat64.Contains(ip):
		return publicIP(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
	case sixToFour.Contains(ip):
		return publicIP(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
	}
	return true
}
