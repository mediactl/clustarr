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
		"alpha-copy.yml":   {Data: bundleDef("alpha")}, // sorts first, but alpha.yml is named after the id and wins
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
	assert.Equal(t, []string{"alpha.yml=alpha", "beta.yaml=beta"}, loaded, "name order; nested directories and non-YAML files ignored")

	byFile := map[string]error{}
	for _, i := range issues {
		byFile[i.File] = i.Err
	}
	require.Len(t, byFile, 3)
	assert.ErrorIs(t, byFile["alpha-copy.yml"], cardigann.ErrDuplicateID)
	assert.ErrorIs(t, byFile["broken.yml"], cardigann.ErrInvalidDefinition)
	assert.ErrorIs(t, byFile["huge.yml"], cardigann.ErrDefinitionTooLarge)
}

// TestLoadBundleKeepsTheFileNamedAfterTheID: a renamed upstream definition
// leaves its old file declaring the same id (Prowlarr's btsate.yml beside
// btstate.yml). The file named after the id loads whichever sorts first;
// with none so named, name order decides.
func TestLoadBundleKeepsTheFileNamedAfterTheID(t *testing.T) {
	fsys := fstest.MapFS{
		"btsate.yml":  {Data: bundleDef("btstate")},
		"btstate.yml": {Data: bundleDef("btstate")},
		"one.yml":     {Data: bundleDef("shared")},
		"two.yml":     {Data: bundleDef("shared")},
	}
	defs, issues, err := cardigann.LoadBundle(fsys)
	require.NoError(t, err)
	var loaded []string
	for _, d := range defs {
		loaded = append(loaded, d.File)
	}
	assert.Equal(t, []string{"btstate.yml", "one.yml"}, loaded)
	require.Len(t, issues, 2)
	assert.Equal(t, "btsate.yml", issues[0].File)
	assert.ErrorIs(t, issues[0], cardigann.ErrDuplicateID)
	assert.Contains(t, issues[0].Error(), "provided by btstate.yml")
	assert.Equal(t, "two.yml", issues[1].File)
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

// TestObjectName pins the IndexerDefinition name a bundled definition gets:
// hack/sync-cardigann's manifests and indexarr's bundle loader both name
// objects through it, so they converge on one object per definition.
func TestObjectName(t *testing.T) {
	for in, want := range map[string]string{
		"1337x":           "1337x",
		"Bittorrentfiles": "bittorrentfiles",
		"a_b.c":           "a-b.c",
		"--x--":           "x",
		"___":             "",
	} {
		if got := cardigann.ObjectName(in); got != want {
			t.Errorf("ObjectName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", 300)
	if got := cardigann.ObjectName(long); len(got) != 253 {
		t.Errorf("ObjectName of a 300-character id is %d characters, want 253", len(got))
	}
}
