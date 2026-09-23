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

package cardigann_test

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/cardigann"
)

// bundleDef is a minimal valid definition with the given id.
func bundleDef(id string) []byte {
	return []byte(strings.Replace(fmt.Sprintf(defHeader, "UTF-8"), "id: feature", "id: "+id, 1) + `search:
  path: search
  rows:
    selector: tr
` + htmlRowFields)
}

func TestLoadBundleLoadsEveryGoodFileAndReportsEveryBadOne(t *testing.T) {
	fsys := fstest.MapFS{
		"alpha.yml":        {Data: bundleDef("alpha")},
		"beta.yaml":        {Data: bundleDef("beta")},
		"alpha-copy.yml":   {Data: bundleDef("alpha")}, // sorts first, so it wins and alpha.yml is the duplicate
		"broken.yml":       {Data: []byte("id: broken\nname: broken\n")},
		"huge.yml":         {Data: append(bundleDef("huge"), []byte("# "+strings.Repeat("x", cardigann.MaxDefinitionBytes)+"\n")...)},
		"README.md":        {Data: []byte("not a definition")},
		"nested/inner.yml": {Data: bundleDef("inner")},
	}

	defs, issues, err := cardigann.LoadBundle(fsys)
	require.NoError(t, err)

	var loaded []string
	for _, d := range defs {
		loaded = append(loaded, d.File+"="+d.ID())
		assert.NotEmpty(t, d.YAML)
	}
	assert.Equal(t, []string{"alpha-copy.yml=alpha", "beta.yaml=beta"}, loaded, "name order; nested directories and non-YAML files ignored")

	byFile := map[string]error{}
	for _, i := range issues {
		byFile[i.File] = i.Err
	}
	require.Len(t, byFile, 3)
	assert.ErrorIs(t, byFile["alpha.yml"], cardigann.ErrDuplicateID)
	assert.ErrorIs(t, byFile["broken.yml"], cardigann.ErrInvalidDefinition)
	assert.ErrorIs(t, byFile["huge.yml"], cardigann.ErrDefinitionTooLarge)
}

// unreadableFS is a bundle whose directory cannot be listed.
type unreadableFS struct{}

func (unreadableFS) Open(string) (fs.File, error) { return nil, fs.ErrPermission }

func TestLoadBundleFailsOnlyWhenTheDirectoryCannotBeRead(t *testing.T) {
	defs, issues, err := cardigann.LoadBundle(unreadableFS{})
	require.ErrorIs(t, err, fs.ErrPermission)
	assert.Empty(t, defs)
	assert.Empty(t, issues)
}
