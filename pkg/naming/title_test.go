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

package naming_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/naming"
)

func TestRenderCleanTitleStripsApostrophesKeepsBang(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	got, err := e.Render("{Movie CleanTitle}", naming.Context{Title: "The Series Name's Title!"})
	require.NoError(t, err)
	require.Equal(t, "The Series Names Title!", got)
}

func TestRenderTitleTheMovesLeadingThe(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	got, err := e.Render("{Movie TitleThe}", naming.Context{Title: "The Series Name"})
	require.NoError(t, err)
	require.Equal(t, "Series Name, The", got)
}

func TestRenderColonReplacementModes(t *testing.T) {
	tests := []struct {
		name string
		mode naming.ColonReplacement
		want string
	}{
		{"delete", naming.ColonDelete, "Spider-Man Into the Spider-Verse"},
		{"dash", naming.ColonDash, "Spider-Man- Into the Spider-Verse"},
		{"spaceDash", naming.ColonSpaceDash, "Spider-Man - Into the Spider-Verse"},
		{"spaceDashSpace", naming.ColonSpaceDashSpace, "Spider-Man - Into the Spider-Verse"},
		{"smart", naming.ColonSmart, "Spider-Man - Into the Spider-Verse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := naming.NewEngine(naming.Config{ColonReplacement: tt.mode})
			got, err := e.Render("{Movie CleanTitle}", naming.Context{Title: "Spider-Man: Into the Spider-Verse"})
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
