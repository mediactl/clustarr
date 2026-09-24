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

package fileimport

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
)

// G2-4 found status.import written uncapped: 200 items and 1024 characters
// per rejection are the CRD's limits, and an apply over either is rejected
// whole, so the import would be recorded nowhere.
func TestCapRejectionsFitsTheCRD(t *testing.T) {
	require.Nil(t, capRejections(nil))
	require.Equal(t, []string{"a: bad"}, capRejections([]string{"a: bad"}))

	many := make([]string, maxImportList+30)
	for i := range many {
		many[i] = "file" + strconv.Itoa(i) + ": rejected"
	}
	got := capRejections(many)
	require.Len(t, got, maxImportList)
	assert.Equal(t, "file0: rejected", got[0])
	assert.Equal(t, "... and 31 more rejections not listed", got[maxImportList-1],
		"the last slot counts what was left out instead of dropping it silently")

	exactly := many[:maxImportList]
	require.Equal(t, exactly, capRejections(exactly), "a list exactly at the cap is sent whole")

	long := strings.Repeat("é", maxRejectionChars+10) // two bytes per rune
	got = capRejections([]string{long})
	assert.Equal(t, maxRejectionChars, utf8.RuneCountInString(got[0]), "the apiserver counts characters, not bytes")
	assert.True(t, utf8.ValidString(got[0]), "a rune is never split")
}

func TestCapImportedKeepsTheFirstAndCountsTheRest(t *testing.T) {
	imported := make([]*downloadac.ImportedFileApplyConfiguration, maxImportList+3)
	for i := range imported {
		imported[i] = downloadac.ImportedFile().WithMediaFileRef("mf-" + strconv.Itoa(i))
	}
	listed, unlisted := capImported(imported)
	require.Len(t, listed, maxImportList)
	assert.Equal(t, 3, unlisted)
	assert.Equal(t, "mf-0", *listed[0].MediaFileRef)

	listed, unlisted = capImported(imported[:2])
	assert.Len(t, listed, 2)
	assert.Zero(t, unlisted)
}

func TestTruncateChars(t *testing.T) {
	assert.Equal(t, "abc", truncateChars("abc", 3))
	assert.Equal(t, "ab", truncateChars("abc", 2))
	assert.Equal(t, "日本", truncateChars("日本語", 2))
	assert.Equal(t, "", truncateChars("abc", 0))
}
