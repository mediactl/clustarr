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

package events_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

// declaredHere is every package-level func, const and var pkg/events
// declares in its non-test files.
func declaredHere(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	out := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		for _, d := range af.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					out[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if v, ok := s.(*ast.ValueSpec); ok {
						for _, n := range v.Names {
							out[n.Name] = true
						}
					}
				}
			}
		}
	}
	require.NotEmpty(t, out)
	require.True(t, out["Default"], "the scan sees this package's declarations")
	return out
}

// TestTheFoldsRetiredBusNamesAreGone (loop spec §4.13, §7.4): every bus name
// keyed by an object release N+1 removes is gone. Under the cold cutover the
// results durables are gone too, not retired: Topology.Retired is empty and
// names nothing, so no constant stays for it. The old grab path's NATS names
// (ADR-0019 A9.2, which lands with F9.4) are gone with them.
func TestTheFoldsRetiredBusNamesAreGone(t *testing.T) {
	d := declaredHere(t)
	for _, name := range []string{
		// F9.4: the TranscodeJob- and SubtitleRequest-keyed names.
		"TranscodeJobSubject", "ProgressTranscodeSubject", "WorkTranscodeResultSubject",
		"WorkTranscodeTaskSubjectAnyProfile", "MsgIDForTranscodeEvent", "MsgIDForTranscodeJobTask",
		"MsgIDForSubtitle", "MsgIDForForcedSubtitle",
		// F3.4, F6.6: the results durables, deleted by the cutover runbook.
		"FilterTranscodeResults", "ConsumerTranscodeResults",
		"FilterCatalogSegmentsResult", "ConsumerCatalogSegmentsResult", "WorkSegmentsResultSubject",
		// A9.2: the grab path's durables, subjects, keys and buckets.
		"ConsumerCatalogGrab", "ConsumerCatalogRedownload", "FilterCatalogGrab", "WorkGrabSubject",
		"BucketLeases", "BucketPending", "LeaseKey", "PendingKey",
		"FilterDownloadFailed", "FilterDownloadBlocklisted",
	} {
		assert.False(t, d[name], "pkg/events still declares %s", name)
	}
	results := regexp.MustCompile(`^(Consumer|Filter)\w*Results?$`)
	for name := range d {
		assert.False(t, results.MatchString(name), "%s: a results durable or filter is back", name)
	}
	assert.Empty(t, events.Default().Retired)
}

// TestTheTranscodeAndSubtitleBuildersAreKeyedByTheFile: the builders the
// loop uses are keyed by the MediaFile's UID, and their subjects are
// unchanged (R7): history (§5.16), the withdrawal's purge (§5.10 step 3) and
// the lease (§4.12).
func TestTheTranscodeAndSubtitleBuildersAreKeyedByTheFile(t *testing.T) {
	assert.Equal(t, "clustarr.evt.transcode.job.succeeded.f1", events.TranscodeEventSubject("succeeded", "f1"))
	assert.Equal(t, "clustarr.evt.subtitle.subtitle.downloaded.f1", events.SubtitleEventSubject("downloaded", "f1"))
	assert.Equal(t, "clustarr.work.transcode.task.p1.cpu.f1", events.WorkTranscodeTaskSubject("p1", "cpu", "f1"))
	assert.Equal(t, "clustarr.work.transcode.task.*.*.f1", events.WorkTranscodeTaskSubjectAnyPool("f1"))
	assert.Equal(t, "lease.f1", events.TranscodeLeaseKey("f1"))
	assert.Equal(t, "transcode/f1/3", events.MsgIDForTranscodeTask("f1", 3))
	assert.Equal(t, "clustarr.work.caption.fetch.high.f1.en", events.WorkFetchSubject(events.PriorityHigh, "f1", "en"))
	top := events.Default()
	for _, subject := range []string{events.TranscodeEventSubject("succeeded", "f1"), events.SubtitleEventSubject("downloaded", "f1")} {
		s, ok := top.StreamForSubject(subject)
		assert.True(t, ok, subject)
		assert.Equal(t, events.StreamEvents, s.Name, "%s is history", subject)
	}
}
