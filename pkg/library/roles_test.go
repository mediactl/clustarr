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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/mediactl/clustarr/pkg/library"
	"github.com/mediactl/clustarr/test/librarytest"
)

// TestTheDatabaseHoldsEachRoleToItsShare is the half of the ui's rule the
// database keeps (design §3.1): connected as the ui's role, the store
// creates an object, patches its spec and deletes it, and cannot write its
// status by any route -- an apply, a patch, an update of the subresource --
// because the status column is not granted to that role. The read role
// reads and follows the feed and writes nothing. Both still move the head
// and the feed when they write, through the trigger that runs as its owner.
// With the status column added to the ui's grant, the three status writes
// succeed and the test fails.
func TestTheDatabaseHoldsEachRoleToItsShare(t *testing.T) {
	pg := librarytest.Start(t)
	dsn := pg.DSN(t)
	admin, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer func() { _ = admin.Close() }()
	for _, stmt := range []string{
		`DROP ROLE IF EXISTS library_ui_test`, `DROP ROLE IF EXISTS library_ro_test`,
		`CREATE ROLE library_ui_test LOGIN PASSWORD 'ui'`, `CREATE ROLE library_ro_test LOGIN PASSWORD 'ro'`,
	} {
		_, err := admin.ExecContext(t.Context(), stmt)
		require.NoError(t, err, stmt)
	}
	manager, err := library.Open(t.Context(), dsn, library.Options{Migrate: true, UIRole: "library_ui_test", ReadRole: "library_ro_test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	as := func(user, password string) *library.Store {
		st, err := library.Open(t.Context(), strings.Replace(dsn, "postgres:postgres@", user+":"+password+"@", 1), library.Options{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = st.Close() })
		return st
	}
	ui, ro := as("library_ui_test", "ui"), as("library_ro_test", "ro")
	sch := issueSchema(t, manager)
	ctx := t.Context()
	withStatus := func(name, title string) *unstructured.Unstructured {
		u := newIssue(name)
		delete(u.Object, "spec")
		u.Object["status"] = map[string]any{"title": title}
		return u
	}

	t.Log("the ui's role: an object's row and its spec, never its status")
	_, err = ui.Create(ctx, newIssue("a"), "clustarr-ui")
	require.NoError(t, err)
	patched, err := ui.Patch(ctx, sch, "default", "a", []byte(`{"spec":{"monitored":false}}`), "clustarr-ui", false)
	require.NoError(t, err)
	assert.EqualValues(t, 2, patched.GetGeneration())
	_, err = manager.Apply(ctx, withStatus("a", "Chapter One"), "catalog", true, true)
	require.NoError(t, err, "the manager's role writes status")

	_, err = ui.Apply(ctx, withStatus("a", "Chapter 1"), "clustarr-ui", true, true)
	require.Error(t, err, "an apply to status as the ui's role")
	assert.Contains(t, err.Error(), "permission denied")
	_, err = ui.Patch(ctx, sch, "default", "a", []byte(`{"status":{"title":"Chapter 1"}}`), "clustarr-ui", true)
	require.Error(t, err, "a patch of status as the ui's role")
	live, err := ui.Get(ctx, sch, "default", "a", false)
	require.NoError(t, err)
	require.NoError(t, unstructured.SetNestedField(live.Object, "Chapter 1", "status", "title"))
	_, err = ui.Update(ctx, live, "clustarr-ui", true)
	require.Error(t, err, "an update of status as the ui's role")
	live, err = manager.Get(ctx, sch, "default", "a", false)
	require.NoError(t, err)
	title, _, _ := unstructured.NestedString(live.Object, "status", "title")
	assert.Equal(t, "Chapter One", title, "the status is as the manager left it")

	t.Log("a patch through the main resource as the ui's role leaves status alone and lands")
	_, err = ui.Patch(ctx, sch, "default", "a", []byte(`{"metadata":{"annotations":{"x":"y"}},"status":{"title":"Chapter 1"}}`), "clustarr-ui", false)
	require.NoError(t, err)

	t.Log("the read role: every read, no write")
	got, err := ro.Get(ctx, sch, "default", "a", true)
	require.NoError(t, err)
	assert.Equal(t, "y", got.GetAnnotations()["x"])
	changes, _, err := ro.Changes(ctx, sch, 0)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	_, err = ro.Create(ctx, newIssue("b"), "x")
	require.Error(t, err)
	_, err = ro.Patch(ctx, sch, "default", "a", []byte(`{"spec":{"monitored":true}}`), "x", false)
	require.Error(t, err)
	require.Error(t, ro.Delete(ctx, sch, "default", "a", library.Preconditions{}))

	t.Log("the ui's role deletes")
	require.NoError(t, ui.Delete(ctx, sch, "default", "a", library.Preconditions{}))
	_, err = manager.Get(ctx, sch, "default", "a", false)
	require.Error(t, err)
}
