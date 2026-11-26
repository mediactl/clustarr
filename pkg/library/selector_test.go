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
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/test/librarytest"
)

// listRows is how many rows the database has returned to lists of a kind.
func listRows(t *testing.T, kind string) int {
	t.Helper()
	m := &dto.Metric{}
	require.NoError(t, metrics.LibraryListRows.WithLabelValues(kind).Write(m))
	return int(m.GetCounter().GetValue())
}

// selectorIssues is the library the selector tests list: forty Issues of
// three Comics, some labelled, some with a download phase. It answers each
// Issue's selectable fields and labels as the test itself knows them, so
// what a selector should pick is worked out with none of the store's code.
type selectorIssue struct {
	name   string
	labels map[string]string
	fields fields.Set
}

func selectorIssues() []selectorIssue {
	comics := []string{"saga", "monstress", "paper-girls"}
	phases := []string{"", "", "", "Queued", "Downloading"}
	var out []selectorIssue
	for i := range 40 {
		is := selectorIssue{
			name:   fmt.Sprintf("issue-%03d", i),
			labels: map[string]string{},
			fields: fields.Set{"spec.comicRef": comics[i%len(comics)], "status.downloadPhase": phases[i%len(phases)]},
		}
		// Two names a collation that weighs punctuation last and a
		// comparison of bytes put in different places.
		switch i {
		case 7:
			is.name = "issue0zero"
		case 8:
			is.name = "issue.dot"
		}
		is.fields["metadata.name"], is.fields["metadata.namespace"] = is.name, "default"
		if i%4 == 0 {
			is.labels["tier"] = "gold"
		}
		if i%6 == 0 {
			is.labels["tier"] = "silver"
		}
		if i%5 == 0 {
			is.labels["held"] = "true"
		}
		out = append(out, is)
	}
	return out
}

func (is selectorIssue) object() map[string]any {
	lbl := map[string]any{}
	for k, v := range is.labels {
		lbl[k] = v
	}
	return map[string]any{
		"apiVersion": "catalog.clustarr.io/v1alpha1", "kind": "Issue",
		"metadata": map[string]any{"name": is.name, "namespace": "default", "labels": lbl},
		"spec":     map[string]any{"comicRef": is.fields["spec.comicRef"], "number": "1", "calculatedNumberCentis": int64(100)},
	}
}

// selectorCase is one list: its selectors, and whether the database can
// apply all of it (pushed), so that it returns the answer's rows and no
// other.
type selectorCase struct {
	field, label string
	pushed       bool
}

var selectorCases = []selectorCase{
	{field: "spec.comicRef=saga", pushed: true},
	{field: "spec.comicRef==monstress", pushed: true},
	{field: "spec.comicRef=nobody", pushed: true},
	{field: "status.downloadPhase=Downloading", pushed: true},
	{field: "status.downloadPhase=", pushed: true}, // an absent field is the empty string
	{field: "status.downloadPhase!=", pushed: true},
	{field: "status.downloadPhase!=Queued", pushed: true},
	{field: "spec.comicRef=saga,status.downloadPhase=Queued", pushed: true},
	{field: "metadata.name=issue-011", pushed: true},
	{field: "metadata.name=issue0zero", pushed: true},
	{field: "metadata.namespace=default,spec.comicRef=paper-girls", pushed: true},
	{field: "metadata.namespace=elsewhere", pushed: true},
	{label: "tier=gold", pushed: true},
	{label: "tier in (gold,silver)", pushed: true},
	{label: "held", pushed: true},
	{label: "tier=silver,held", pushed: true},
	{label: "tier=gold", field: "status.downloadPhase=Downloading", pushed: true},
	{label: "tier!=gold"},
	{label: "tier notin (gold)"},
	{label: "!held"},
	{label: "held,tier!=silver"},
	{},
}

// want is the names a case should answer with, in the apiserver's order.
func (c selectorCase) want(t *testing.T, all []selectorIssue) []string {
	t.Helper()
	fsel, err := fields.ParseSelector(c.field)
	require.NoError(t, err)
	lsel, err := labels.Parse(c.label)
	require.NoError(t, err)
	var names []string
	for _, is := range all {
		if fsel.Matches(is.fields) && lsel.Matches(labels.Set(is.labels)) {
			names = append(names, is.name)
		}
	}
	sort.Strings(names)
	return names
}

func (c selectorCase) options(t *testing.T) []client.ListOption {
	t.Helper()
	var opts []client.ListOption
	if c.field != "" {
		sel, err := fields.ParseSelector(c.field)
		require.NoError(t, err)
		opts = append(opts, client.MatchingFieldsSelector{Selector: sel})
	}
	if c.label != "" {
		sel, err := labels.Parse(c.label)
		require.NoError(t, err)
		opts = append(opts, client.MatchingLabelsSelector{Selector: sel})
	}
	return opts
}

// TestAListBySelectorReadsTheRowsItAnswersWith is the transcode ledger's
// rebuild on talos, 2026-11-26: seven lists of the MediaFiles by phase,
// each of which read all 33,822 rows from the database, 205 MB, to keep one
// or two, every five minutes -- the manager's live heap rose from 400 to
// 645 MiB each time. A list answers what its selectors pick, in the
// apiserver's order, and when the database can apply the whole selection it
// returns those rows and no other.
func TestAListBySelectorReadsTheRowsItAnswersWith(t *testing.T) {
	st := librarytest.Start(t).Store(t, nil)
	sch := issueSchema(t, st)
	all := selectorIssues()
	for _, is := range all {
		_, err := st.Create(t.Context(), &unstructured.Unstructured{Object: is.object()}, "clustarr")
		require.NoError(t, err)
		if phase := is.fields["status.downloadPhase"]; phase != "" {
			_, err := st.Patch(t.Context(), sch, "default", is.name, []byte(`{"status":{"downloadPhase":"`+phase+`"}}`), "catalog", true)
			require.NoError(t, err)
		}
	}
	scheme := catalogScheme(t)

	for _, c := range selectorCases {
		t.Run(c.field+"|"+c.label, func(t *testing.T) {
			want := c.want(t, all)
			before := listRows(t, "Issue")
			var list catalogv1.IssueList
			require.NoError(t, st.ListInto(t.Context(), sch, scheme, &list, c.options(t)...))
			read := listRows(t, "Issue") - before
			var got []string
			for i := range list.Items {
				got = append(got, list.Items[i].Name)
			}
			assert.Equal(t, want, got, "the Issues the selectors pick, in the order of their keys")
			if c.pushed {
				assert.Equal(t, len(want), read, "the database returns the rows of the answer and no other")
			} else {
				assert.GreaterOrEqual(t, read, len(want))
			}

			// The informer's list is the same read.
			lw, err := st.ListerWatcher(sch, scheme, "")
			require.NoError(t, err)
			before = listRows(t, "Issue")
			obj, err := lw.ListWithContext(t.Context(), metav1.ListOptions{FieldSelector: c.field, LabelSelector: c.label})
			require.NoError(t, err)
			got = got[:0]
			for _, it := range obj.(*catalogv1.IssueList).Items {
				got = append(got, it.Name)
			}
			assert.ElementsMatch(t, want, got)
			if c.pushed {
				assert.Equal(t, len(want), listRows(t, "Issue")-before)
			}
		})
	}
}

// TestEverySelectableFieldHasTheIndexItsListUses holds the two halves of
// the list by a selectable field together: the migration makes an index for
// every such field of every kind, and the condition a list sends is the
// index's expression to the letter, which is what lets the planner use it.
// A selectable field with no index is a list that reads every row's status
// to answer (458 ms for the MediaFiles of talos, against the lookup it is
// with one).
func TestEverySelectableFieldHasTheIndexItsListUses(t *testing.T) {
	st := librarytest.Start(t).Store(t, nil)
	for _, sch := range st.Schemas().Sorted() {
		names := sch.SelectableIndexNames()
		for _, path := range sch.SelectableFields {
			t.Run(sch.GVK.Kind+"/"+path, func(t *testing.T) {
				name, ok := names[path]
				require.True(t, ok, "a selectable field the database cannot filter by: is it a string, a boolean or an integer?")
				var def string
				require.NoError(t, st.DB().QueryRowContext(t.Context(),
					`SELECT indexdef FROM pg_indexes WHERE schemaname = 'library' AND tablename = $1 AND indexname = $2`, sch.Table, name).Scan(&def),
					"the migration made no index for the field")

				sel, err := fields.ParseSelector(path + "=some-value")
				require.NoError(t, err)
				conds, args := sch.ListConditions(sel, nil)
				require.Len(t, conds, 1)
				where := conds[0]
				for i, a := range args {
					where = strings.ReplaceAll(where, "$"+strconv.Itoa(i+1), "'"+a.(string)+"'")
				}
				tx, err := st.DB().BeginTx(t.Context(), nil)
				require.NoError(t, err)
				defer func() { _ = tx.Rollback() }()
				// An empty table is read whole whatever its indexes; with
				// that plan priced out, the planner shows whether it can
				// use the index at all.
				_, err = tx.ExecContext(t.Context(), `SET LOCAL enable_seqscan = off`)
				require.NoError(t, err)
				rows, err := tx.QueryContext(t.Context(), `EXPLAIN SELECT namespace, name FROM library.`+sch.Table+` WHERE `+where)
				require.NoError(t, err)
				defer func() { _ = rows.Close() }()
				var plan []string
				for rows.Next() {
					var line string
					require.NoError(t, rows.Scan(&line))
					plan = append(plan, line)
				}
				require.NoError(t, rows.Err())
				assert.Contains(t, strings.Join(plan, "\n"), name, "the list's condition is not the index's expression")
			})
		}
	}
}

// TestSelectorListsMatchTheAPIServer puts the same MediaFiles in a real
// apiserver and in the store and lists both by the fields and labels the
// manager selects on -- the transcode ledger's phases among them, and the
// one selectable field that is not a string -- which must agree on the
// names and on their order. The apiserver is the authority on what a
// selector means: an absent field that is the empty string, a boolean that
// is "true", a list in the order of its keys.
func TestSelectorListsMatchTheAPIServer(t *testing.T) {
	a, s := startDifferential(t, "schemas/catalog.clustarr.io_mediafiles.yaml")
	type file struct {
		name, kind, ref string
		labels          map[string]any
		status          map[string]any
	}
	files := []file{
		{name: "b-running", kind: "episode", ref: "show-s01e01", labels: map[string]any{"tier": "gold"},
			status: map[string]any{"transcode": map[string]any{"phase": "Running"}, "naming": map[string]any{"current": true}}},
		{name: "a-planned", kind: "episode", ref: "show-s01e02", labels: map[string]any{"tier": "silver", "held": "true"},
			status: map[string]any{"transcode": map[string]any{"phase": "Planned"}, "naming": map[string]any{"current": false}}},
		{name: "c-dashed", kind: "movie", ref: "film",
			status: map[string]any{"transcode": map[string]any{"phase": "Succeeded"}, "graft": map[string]any{"phase": "Queued"}}},
		{name: "c.dotted", kind: "movie", ref: "film", labels: map[string]any{"held": "true"},
			status: map[string]any{"graft": map[string]any{"phase": "Running"}, "naming": map[string]any{"current": true}}},
		{name: "d-bare", kind: "album", ref: "record"},
		{name: "e-empty-status", kind: "episode", ref: "show-s01e01", labels: map[string]any{"tier": "gold"}, status: map[string]any{"probeHash": "abc"}},
	}
	for _, f := range files {
		obj := map[string]any{
			"apiVersion": "catalog.clustarr.io/v1alpha1", "kind": "MediaFile",
			"metadata": map[string]any{"name": f.name, "namespace": diffNamespace},
			"spec":     map[string]any{"mediaRef": map[string]any{"kind": f.kind, "name": f.ref}, "path": "/data/media/" + f.name + ".mkv"},
		}
		if f.labels != nil {
			obj["metadata"].(map[string]any)["labels"] = f.labels
		}
		both(t, a, s, f.name, func(x side) (*unstructured.Unstructured, error) { return x.create(obj, "clustarr") })
		if f.status != nil {
			both(t, a, s, f.name, func(x side) (*unstructured.Unstructured, error) {
				return x.patch(f.name, map[string]any{"status": f.status}, "catalog", true)
			})
		}
	}

	scheme := catalogScheme(t)
	for _, c := range []struct{ field, label string }{
		{},
		{field: "status.transcode.phase=Running"},
		{field: "status.transcode.phase=Planned"},
		{field: "status.transcode.phase=Pending"},
		{field: "status.transcode.phase="},
		{field: "status.transcode.phase!="},
		{field: "status.transcode.phase!=Running"},
		{field: "status.graft.phase=Queued"},
		{field: "status.graft.phase=Running"},
		{field: "status.naming.current=true"},
		{field: "status.naming.current=false"},
		{field: "status.naming.current="},
		{field: "status.naming.current!=true"},
		{field: "spec.mediaRef.kind=episode"},
		{field: "spec.mediaRef.kind=movie,spec.mediaRef.name=film"},
		{field: "spec.mediaRef.name=show-s01e01,status.transcode.phase=Running"},
		{field: "metadata.name=c-dashed"},
		{field: "metadata.name=nobody"},
		{label: "tier=gold"},
		{label: "tier in (gold,silver)"},
		{label: "tier notin (gold)"},
		{label: "held"},
		{label: "!held"},
		{label: "tier!=gold"},
		{label: "tier=gold", field: "status.transcode.phase=Running"},
	} {
		t.Run(c.field+"|"+c.label, func(t *testing.T) {
			fromAPI, err := a.ri.List(t.Context(), metav1.ListOptions{FieldSelector: c.field, LabelSelector: c.label})
			require.NoError(t, err)
			var want []string
			for _, it := range fromAPI.Items {
				want = append(want, it.GetName())
			}

			var typed catalogv1.MediaFileList
			require.NoError(t, s.st.ListInto(t.Context(), s.sch, scheme, &typed, selectorCase{field: c.field, label: c.label}.options(t)...))
			var got []string
			for i := range typed.Items {
				got = append(got, typed.Items[i].Name)
			}
			assert.Equal(t, want, got, "the store's list against the apiserver's")
		})
	}

	t.Log("a field the kind does not offer is refused by both")
	_, errA := a.ri.List(t.Context(), metav1.ListOptions{FieldSelector: "status.probeHash=abc"})
	sel, err := fields.ParseSelector("status.probeHash=abc")
	require.NoError(t, err)
	var typed catalogv1.MediaFileList
	errS := s.st.ListInto(t.Context(), s.sch, scheme, &typed, client.MatchingFieldsSelector{Selector: sel})
	require.Error(t, errA)
	assert.Equal(t, errorKind(errA), errorKind(errS))
}
