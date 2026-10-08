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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
)

// retiredBrand matches the six retired service brands and the transcode
// service's old name, case-insensitively, as hack/brand-gate.sh does; like
// the gate's pattern it spells none of them, so this file passes the gate.
var retiredBrand = regexp.MustCompile(`(?i)(catalog|import|index|grab|caption|segment)a[r]r|squas[h]`)

// topologyNames is every name and subject the bus carries: each stream,
// consumer (the dead-letter watchers included), filter, bucket and object
// store of Default() with its description and metadata; the dynamic pool and
// engine durables; every subject, key and Msg-Id builder's output on
// brand-free arguments; the RPC subjects and queue groups; and where each
// static consumer's dead letters land.
func topologyNames(t *testing.T) []string {
	t.Helper()
	top := events.Default()
	var out []string
	add := func(s ...string) { out = append(out, s...) }
	for _, s := range top.Streams {
		add(s.Name, s.Description)
		add(s.Subjects...)
	}
	for _, c := range append(top.Consumers,
		events.TranscodeTaskConsumer("6f1c-uid", string(catalogv1alpha1.TranscodeClassNVIDIA)),
		events.EngineConsumer("qbit", 0),
	) {
		add(c.Name, c.Stream, c.Description)
		add(c.Filters...)
		if c.Stream != events.StreamAdvisories {
			subject, env := events.DeadLetterEnvelope(&events.Envelope{ID: "id-1"}, c.Filters[0], 3, c.Name, "boom")
			add(subject, env.ID)
		}
	}
	for _, b := range top.Buckets {
		add(b.Name, b.Description)
	}
	for _, o := range top.ObjectStores {
		add(o.Name, o.Description)
		for k, v := range o.Metadata {
			add(k, v)
		}
	}
	for _, r := range top.Retired {
		add(r.Stream, r.Durable, r.Purge)
	}
	add(events.TaskEventSubjects(top)...)

	const uid, key = "u1", "media-heat-1995"
	work := []string{
		events.WorkSearchSubject(events.PriorityHigh, key),
		events.WorkMetadataSubject(events.PriorityNormal, key),
		events.WorkSegmentsPlanSubject(key),
		events.WorkSegmentsAnalyzeSubject(key),
		events.WorkMarkersSubject(key),
		events.WorkWantedScanSubject("media"),
		events.WorkScanSubject("movies"),
		events.WorkListSubject("watchlist"),
		events.WorkFileImportSubject(uid),
		events.WorkImportInspectSubject(uid),
		events.WorkImportExecuteSubject(uid),
		events.SubjectImportRecycleSweep,
		events.SubjectImportRecycleFiles(uid),
		events.WorkRSSSubject(uid),
		events.WorkFetchSubject(events.PriorityLow, uid, "en"),
		events.WorkArtworkFetchSubject(key),
		events.WorkArtworkRenderSubject(key),
		events.WorkTranscodeTaskSubject("p1", "cpu", uid),
		events.WorkProbeSubject(events.PriorityHigh, key),
		events.WorkEngineSubject("qbit", 0, uid),
	}
	add(work...)
	for _, w := range work {
		hold, err := events.ScheduleSubject(w)
		require.NoError(t, err, w)
		add(hold)
	}
	add(
		events.CatalogItemSubject("movie", events.ActionAdded, uid),
		events.CatalogReleaseSubject(events.ActionGrabbed, uid),
		events.CatalogMediaFileSubject(events.ActionImported, uid),
		events.CatalogImportListSyncedSubject(uid),
		events.ReleaseSubject("torrent", "tracker", 2000),
		events.IndexerEventSubject(events.ActionDisabled, uid),
		events.DownloadEventSubject(events.ActionFailed, uid),
		events.TranscodeEventSubject(events.ActionSucceeded, uid),
		events.SubtitleEventSubject(events.ActionDownloaded, uid),
		events.WorkFetchSubjectAnyPriority(uid, "en"),
		events.WorkTranscodeTaskSubjectAnyPool(uid),
		events.FilterTranscodeTasks("p1", "cpu"),
		events.TranscodeTaskConsumerName("p1", "cpu"),
		events.ProgressDownloadSubject(uid),
		events.IntakeCandidateSubject("Movie", uid),
		events.IntakeScanSubject("media", uid),
		events.WorkEngineResyncSubject("qbit", 0),
		events.WorkEngineIDSubject("qbit", 0, "dl1"),
		events.EngineConsumerName("qbit", 0),
		events.PresenceKey("catalog", "pod-0"),
		events.DownloadProgressKey(uid), events.TranscodeProgressKey(uid),
		events.ProbeKey(uid), events.RecordKey(uid), events.RecordSubKey(uid, "en"),
		events.TranscodeLeaseKey(uid), events.ExclusionKey("imdb", "tt0113277"),
		events.MediaKey("Movie", "media", "heat"),
		events.MsgIDForProbe(uid, "h", 1, 1),
		events.MsgIDForMarkers(uid, 1),
		events.MsgIDForMarkersAt(uid, 1, time.Unix(0, 0)),
		events.MsgIDForSubtitleFetch(uid, "en", 1),
		events.MsgIDForTranscodeTask(uid, 1),
		events.MsgIDForEngineCommand(uid, 1),
		events.MsgIDForEngineCommandAfterBoot(uid, 1, "boot"),
		events.MsgIDForEngineResync(uid, 0, 1),
		events.MsgIDForEngineRemoveID(uid, 0, "dl1"),
		events.MsgIDForImport(uid, "inspect", 1),
		events.MsgIDForSearch(uid, 1),
		events.MsgIDForSeasonSearch(uid, 1, 1),
		events.MsgIDForCandidate(events.CandidateMsgOriginRSS, uid, "tracker", "guid"),
		events.MsgIDForScanObservation(uid, "/data/movies/heat.mkv", "fp"),
		events.MsgIDForObject(uid, 1, "search"),
		events.RPCIndexSearch, events.RPCIndexDownload, events.RPCIndexQuery, events.RPCIndexBlocklist,
		events.RPCMetadataLookup, events.RPCMetadataSearch, events.RPCMetadataResolve, events.RPCMetadataExtras,
		events.QueueGroupIndex, events.QueueGroupCatalog,
		events.SlotsEnv,
	)
	return out
}

// TestNoTopologyNameIsBranded (Wave U U4): every stream, consumer, filter,
// subject builder output, RPC subject and queue group is unbranded, and so is
// every string constant pkg/events declares -- a name the builders above do
// not reach (a schema value, a header) included.
func TestNoTopologyNameIsBranded(t *testing.T) {
	names := topologyNames(t)
	require.Greater(t, len(names), 200, "the walk reaches the topology")
	for _, n := range names {
		assert.False(t, retiredBrand.MatchString(n), "%q names a retired brand", n)
	}

	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	consts := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		for _, d := range af.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || (g.Tok != token.CONST && g.Tok != token.VAR) {
				continue
			}
			ast.Inspect(g, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				v, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				consts++
				assert.False(t, retiredBrand.MatchString(v), "%s: %q names a retired brand", f, v)
				return true
			})
		}
	}
	assert.Greater(t, consts, 50, "the scan reaches the package's string constants")
}

// TestTranscodePoolDurableNamesAreKVSafe (Wave U U4, R-U2): a pool's durable
// is "transcode-pool-<profile uid>-<class>", a legal durable name and KV key
// whatever the profile UID holds, one per (profile, class), and its prefix is
// the one the admission sweep deletes by, which no shipped durable carries.
func TestTranscodePoolDurableNamesAreKVSafe(t *testing.T) {
	classes := []string{
		string(catalogv1alpha1.TranscodeClassCPU),
		string(catalogv1alpha1.TranscodeClassNVIDIA),
		string(catalogv1alpha1.TranscodeClassIntel),
	}
	profiles := []string{
		"6f1c0d7e-2f1d-4c8e-9a3b-6d7e8f901234", "6f1c0d7e-2f1d-4c8e-9a3b-6d7e8f901235",
		"a.b", "a/b", "a:b", "a b", "a-b", "a--b", "a-2eb", "Amélie", "", "-", "x*", "x>",
	}
	seen := map[string]string{}
	for _, p := range profiles {
		for _, class := range classes {
			name := events.TranscodeTaskConsumerName(p, class)
			id := fmt.Sprintf("%q/%s", p, class)
			prev, clash := seen[name]
			require.False(t, clash, "%s and %s share the durable %s", prev, id, name)
			seen[name] = id
			assert.True(t, strings.HasPrefix(name, events.TranscodePoolDurablePrefix), name)
			assert.Equal(t, "transcode-pool-", events.TranscodePoolDurablePrefix)
			assert.Regexp(t, consumerNameGrammar, name)
			assert.True(t, events.ValidKVKey(name), "%s is no KV key", name)

			spec := events.TranscodeTaskConsumer(p, class)
			assert.Equal(t, name, spec.Name)
			assert.True(t, events.SubjectMatches(spec.Filters[0], events.WorkTranscodeTaskSubject(p, class, "f1")), id)
		}
	}
	for _, c := range events.Default().Consumers {
		assert.False(t, strings.HasPrefix(c.Name, events.TranscodePoolDurablePrefix),
			"%s: the admission sweep would delete a shipped durable", c.Name)
	}
}
