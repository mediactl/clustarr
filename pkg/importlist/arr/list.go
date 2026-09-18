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

// Package arr is a pkg/importlist provider stub for following the library
// of another *arr instance (including another Clustarr). It is deferred
// past M6 (spec §17 "Deferred") and its Fetch always returns a typed
// importlist.NotImplementedError.
package arr

import (
	"context"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
)

// List is a placeholder ImportList for the arr provider; see the package
// doc comment.
type List struct {
	name string
	kind commonv1.MediaKind
	cfg  importlist.ArrConfig
}

// New returns a List.
func New(name string, kind commonv1.MediaKind, cfg importlist.ArrConfig) *List {
	return &List{name: name, kind: kind, cfg: cfg}
}

// Name implements importlist.ImportList.
func (l *List) Name() string { return l.name }

// Kind implements importlist.ImportList.
func (l *List) Kind() commonv1.MediaKind { return l.kind }

// Fetch always returns a *importlist.NotImplementedError; see the package
// doc comment.
func (l *List) Fetch(context.Context) ([]importlist.Item, error) {
	return nil, &importlist.NotImplementedError{
		Provider: "arr",
		TODO:     "*arr-to-*arr library sync — deferred past M6, see docs/research/metadata.md §3.5",
	}
}
