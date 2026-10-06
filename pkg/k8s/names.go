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

import "github.com/mediactl/clustarr/pkg/names"

// The naming functions live in pkg/names, which imports only the standard
// library, so ui/actions (barred from pkg/k8s) can name a catalog item
// exactly as importarr does (Add New, 2026-09-29). These wrappers keep
// every existing caller; each one's doc is its pkg/names original's.

const (
	MaxNameLength       = names.MaxNameLength
	MaxLabelValueLength = names.MaxLabelValueLength
	HashSuffixLength    = names.HashSuffixLength
)

// HashSuffix is [names.HashSuffix].
func HashSuffix(parts ...string) string { return names.HashSuffix(parts...) }

// DeterministicName is [names.DeterministicName].
func DeterministicName(target string, maxLen int, parts ...string) string {
	return names.DeterministicName(target, maxLen, parts...)
}

// ChildName is [names.ChildName].
func ChildName(target string, parts ...string) string { return names.ChildName(target, parts...) }

// LabelSafeName is [names.LabelSafeName].
func LabelSafeName(target string, parts ...string) string {
	return names.LabelSafeName(target, parts...)
}

// NormalizeName is [names.NormalizeName].
func NormalizeName(s string) string { return names.NormalizeName(s) }

// HashOrdinal is [names.HashOrdinal].
func HashOrdinal(replicas int32, parts ...string) int32 { return names.HashOrdinal(replicas, parts...) }

// AudioGraftName is the name of an item's AudioGraft (anime dual-audio spec
// §6.2): one per Episode or Movie, which importarr creates and the item's
// reconciler reads.
func AudioGraftName(item string) string { return ChildName(item, "audiograft") }
