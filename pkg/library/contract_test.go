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

package library_test

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/library"
	"github.com/mediactl/clustarr/test/librarytest"
)

// The statements below are cluster-plex's (github.com/mediactl/clusterplex,
// pkg/clustarrlib), copied word for word: it reads the library store's
// tables itself, with no code of this module, so the tables are a contract
// between the two repositories (design §3.7). A change to a table, a column
// or the feed that breaks one of these breaks cluster-plex's projection of
// the library into Plex: change both, and ship them together.
const (
	plexObjectSQL = `CASE WHEN o.status IS NULL THEN o.body ELSE o.body || jsonb_build_object('status', o.status) END`
	plexHeadSQL   = `SELECT seq FROM library.head WHERE kind = $1`
	plexListSQL   = `SELECT o.version, ` + plexObjectSQL + ` FROM library.issue o WHERE o.namespace = $1`
	plexFeedSQL   = `
WITH h AS (SELECT seq, floor FROM library.head WHERE kind = $1),
w AS (
	SELECT DISTINCT ON (namespace, name) seq, namespace, name, op, body
	FROM library.change
	WHERE kind = $1 AND seq > $2 AND seq <= $2 + $3 AND ($4 = '' OR namespace = $4)
	ORDER BY namespace, name, seq DESC
)
SELECT h.seq, h.floor, w.seq, w.op::text, w.body, o.version, ` + plexObjectSQL + `
FROM h LEFT JOIN w ON true
LEFT JOIN library.issue o ON o.namespace = w.namespace AND o.name = w.name
ORDER BY w.seq`
)

// TestTheTablesAreTheContractClusterPlexReads runs cluster-plex's own
// statements against a store this module wrote: its list, the head it takes
// the list's version from, and its read of the feed after a create, an
// apply to status, and a delete.
func TestTheTablesAreTheContractClusterPlexReads(t *testing.T) {
	st := librarytest.Start(t).Store(t, nil)
	sch := issueSchema(t, st)
	ctx := t.Context()
	for _, n := range []string{"a", "b"} {
		_, err := st.Create(ctx, newIssue(n), "clustarr")
		require.NoError(t, err)
	}
	applied := newIssue("a")
	delete(applied.Object, "spec")
	applied.Object["status"] = map[string]any{"title": "Chapter One"}
	_, err := st.Apply(ctx, applied, "catalog", true, true)
	require.NoError(t, err)
	require.NoError(t, st.Delete(ctx, sch, "default", "b", library.Preconditions{}))
	db := st.DB()

	var head int64
	require.NoError(t, db.QueryRowContext(ctx, plexHeadSQL, "Issue").Scan(&head))
	assert.EqualValues(t, 4, head, "two creates, an apply, a delete")

	rows, err := db.QueryContext(ctx, plexListSQL, "default")
	require.NoError(t, err)
	type object struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name string `json:"name"`
			UID  string `json:"uid"`
		} `json:"metadata"`
		Spec   map[string]any `json:"spec"`
		Status struct {
			Title string `json:"title"`
		} `json:"status"`
	}
	var listed []object
	for rows.Next() {
		var (
			version int64
			body    []byte
			o       object
		)
		require.NoError(t, rows.Scan(&version, &body))
		require.NoError(t, json.Unmarshal(body, &o))
		assert.EqualValues(t, 3, version, "the apply's sequence number is the object's version")
		listed = append(listed, o)
	}
	require.NoError(t, rows.Err())
	require.Len(t, listed, 1)
	assert.Equal(t, "Issue", listed[0].Kind)
	assert.Equal(t, "a", listed[0].Metadata.Name)
	assert.NotEmpty(t, listed[0].Metadata.UID)
	assert.Equal(t, "saga", listed[0].Spec["comicRef"])
	assert.Equal(t, "Chapter One", listed[0].Status.Title, "the status is laid into the object")

	feed, err := db.QueryContext(ctx, plexFeedSQL, "Issue", int64(0), int64(1000), "default")
	require.NoError(t, err)
	type change struct {
		op      string
		name    string
		version sql.NullInt64
	}
	var got []change
	for feed.Next() {
		var (
			headSeq, floor int64
			seq, version   sql.NullInt64
			op             sql.NullString
			last, body     []byte
		)
		require.NoError(t, feed.Scan(&headSeq, &floor, &seq, &op, &last, &version, &body))
		assert.EqualValues(t, 4, headSeq)
		assert.Zero(t, floor)
		require.True(t, seq.Valid)
		var o object
		if version.Valid {
			require.NoError(t, json.Unmarshal(body, &o))
		} else {
			require.NoError(t, json.Unmarshal(last, &o), "a delete carries the object as it was")
		}
		got = append(got, change{op: op.String, name: o.Metadata.Name, version: version})
	}
	require.NoError(t, feed.Err())
	assert.Equal(t, []change{
		{op: "M", name: "a", version: sql.NullInt64{Int64: 3, Valid: true}},
		{op: "D", name: "b"},
	}, got, "one change an object, in the order of their versions: the changed one as it stands, the deleted one as it was")
}
