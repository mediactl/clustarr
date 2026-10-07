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

package schema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

func TestScanTaskEncodeDecodeRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   schema.ScanTask
	}{
		{
			name: "incremental scan of a whole root folder",
			in: schema.ScanTask{
				LibraryScanRef: schema.Ref{Namespace: "media", Name: "movies-2026-09-18t00-00-00z", UID: "u1"},
				RootFolderRef:  schema.Ref{Namespace: "media", Name: "movies"},
				Path:           "/data/media/movies",
				Mode:           "incremental",
			},
		},
		{
			name: "full dry-run scan of a subpath",
			in: schema.ScanTask{
				LibraryScanRef: schema.Ref{Namespace: "media", Name: "movies-manual", UID: "u2"},
				RootFolderRef:  schema.Ref{Namespace: "media", Name: "movies"},
				Path:           "/data/media/movies/Heat (1995)",
				Mode:           "full",
				DryRun:         true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, data, err := schema.Encode(tc.in)
			require.NoError(t, err)
			assert.Equal(t, "importarr.ScanTask.v1", name)

			var out schema.ScanTask
			require.NoError(t, schema.Decode(name, data, &out))
			assert.Equal(t, tc.in, out)
		})
	}
}

// A ScanTask must never be decoded from a message carrying another payload's
// schema header: the bus versions payloads by header, not by subject.
func TestScanTaskRejectsAForeignSchemaHeader(t *testing.T) {
	_, data, err := schema.Encode(schema.ScanTask{RootFolderRef: schema.Ref{Name: "movies"}})
	require.NoError(t, err)

	var out schema.ScanTask
	assert.Error(t, schema.Decode("importarr.ScanTask.v2", data, &out))
}

func TestListTaskEncodeDecodeRoundTrip(t *testing.T) {
	in := schema.ListTask{
		ListRef: schema.Ref{Namespace: "media", Name: "trakt-watchlist", UID: "u1"},
	}
	name, data, err := schema.Encode(in)
	require.NoError(t, err)
	assert.Equal(t, "importarr.ListTask.v1", name)

	var out schema.ListTask
	require.NoError(t, schema.Decode(name, data, &out))
	assert.Equal(t, in, out)
}

// A ListTask must never be decoded from a message carrying another payload's
// schema header, same as ScanTask above.
func TestListTaskRejectsAForeignSchemaHeader(t *testing.T) {
	_, data, err := schema.Encode(schema.ListTask{ListRef: schema.Ref{Name: "trakt-watchlist"}})
	require.NoError(t, err)

	var out schema.ListTask
	assert.Error(t, schema.Decode("importarr.ListTask.v2", data, &out))
}
