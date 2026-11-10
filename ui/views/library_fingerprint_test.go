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

package views

import (
	"bytes"
	"context"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// The library stream's manifest sends each card's fingerprint, and the
// browser fetches a card only when it moves: a field the card renders and
// the fingerprint leaves out would leave every open page showing the old
// card. So every field of LibraryItem is changed in turn, by reflection --
// a field added later is covered without anyone listing it -- and wherever
// the card's markup changes, the fingerprint must too.
func TestCardFingerprintMovesWithEveryRenderedField(t *testing.T) {
	base := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "dune"}, Kind: commonv1.MediaKindMovie,
		Tab: projection.TabMovies, Title: "Dune", Year: 2021, Monitored: true, Phase: "Imported", HasFile: true,
		QualityProfileRef: "hevc", Poster: "/art/movie/u/poster?v=1", ArtKey: "movie/u/poster",
	}
	render := func(li projection.LibraryItem) string {
		var b bytes.Buffer
		require.NoError(t, libraryCard(li).Render(context.Background(), &b))
		// The tooltip's ids are random per render; they are no change.
		return regexp.MustCompile(`id-[A-Z0-9]+|data-fp="[^"]*"`).ReplaceAllString(b.String(), "")
	}
	baseHTML, baseFP := render(base), CardFingerprint(base)
	require.Equal(t, baseHTML, render(base), "a card renders the same twice")

	v := reflect.ValueOf(&base).Elem()
	rendered := 0
	for i := range v.NumField() {
		f := v.Type().Field(i)
		changed := base
		mutate(t, reflect.ValueOf(&changed).Elem().Field(i), f.Name)
		if render(changed) == baseHTML {
			continue
		}
		rendered++
		require.NotEqual(t, baseFP, CardFingerprint(changed), "the card renders %s, so its fingerprint must cover it", f.Name)
	}
	require.Greater(t, rendered, 8, "the card renders most of what identifies it")
}

// mutate changes field to another value of its type.
func mutate(t *testing.T, field reflect.Value, name string) {
	t.Helper()
	switch field.Kind() {
	case reflect.String:
		field.SetString(field.String() + "x")
	case reflect.Bool:
		field.SetBool(!field.Bool())
	case reflect.Int32, reflect.Int64, reflect.Int:
		field.SetInt(field.Int() + 1)
	case reflect.Slice:
		switch field.Type().Elem().Kind() {
		case reflect.String:
			field.Set(reflect.Append(field, reflect.ValueOf("x")))
		default:
			field.Set(reflect.Append(field, reflect.New(field.Type().Elem()).Elem()))
		}
	case reflect.Struct:
		switch field.Type() {
		case reflect.TypeFor[time.Time]():
			field.Set(reflect.ValueOf(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
		case reflect.TypeFor[types.NamespacedName]():
			field.Set(reflect.ValueOf(types.NamespacedName{Namespace: "other", Name: "other"}))
		default:
			t.Fatalf("mutate: LibraryItem.%s is a %s; teach mutate to change it", name, field.Type())
		}
	default:
		t.Fatalf("mutate: LibraryItem.%s is a %s; teach mutate to change it", name, field.Type())
	}
}
