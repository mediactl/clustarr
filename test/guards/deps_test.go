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

package guards

// The per-binary deps guards (spec 2026-10-06 §4.5, W5.14): every binary's
// import closure under the build settings its image uses, checked against
// what the package tree (§4.4, as amended for ADR-0016, ADR-0019 and Wave U)
// lets it link. This file is their only home (§4.5, R10).

import (
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mediactl/clustarr/test/guards/depguard"
)

// depsRules is one rule per path, each a prefix match with the same why.
func depsRules(why string, paths ...string) []depguard.Rule {
	out := make([]depguard.Rule, 0, len(paths))
	for _, p := range paths {
		out = append(out, depguard.Rule{Path: p, Why: why})
	}
	return out
}

// depsGraphs memoises depguard.Load per package and environment: several
// guards read the same binary's closure, and `go list -deps` is the slow
// part of every one of them.
var depsGraphs = struct {
	sync.Mutex
	m map[string]depguard.Graph
}{m: map[string]depguard.Graph{}}

// depsGraph is depguard.Load, once per (pkg, env) for the test binary.
func depsGraph(t *testing.T, pkg string, env ...string) depguard.Graph {
	t.Helper()
	key := pkg + "\x00" + strings.Join(env, "\x00")
	depsGraphs.Lock()
	defer depsGraphs.Unlock()
	if g, ok := depsGraphs.m[key]; ok {
		return g
	}
	g := depguard.Load(t, pkg, env...)
	depsGraphs.m[key] = g
	return g
}

// depsDynamicAndNative is what makes a binary dynamic or probes media: the
// static distroless clustarr image (R1) cannot start a binary that links
// purego, and the manager never probes, decodes or analyses (R3). The
// ffprobeexec and execextract paths are gone (R2); the rule keeps them from
// coming back.
var depsDynamicAndNative = depsRules("static distroless image (R1); the manager never probes (R3)",
	"github.com/ebitengine/purego", "github.com/obinnaokechukwu/ffgo", "github.com/shota3506/onnxruntime-purego",
	"github.com/mediactl/par2go", mod+"pkg/par2child", mod+"pkg/ffruntime", mod+"pkg/mediainfo/native",
	mod+"pkg/mediainfo/ffprobeexec", mod+"pkg/subtitles/providers/embedded/native",
	mod+"pkg/subtitles/providers/embedded/execextract", mod+"pkg/segments/decode", mod+"pkg/segments/textdet",
	mod+"pkg/segments/chromaprint", mod+"pkg/segments/frames", mod+"pkg/segments/align")

// depsStorage is the release index's storage: the index agent's alone (R6, R8).
var depsStorage = depsRules("the release index is the index agent's (R6, R8)",
	"modernc.org/sqlite", "modernc.org/libc", "github.com/jackc/pgx", "github.com/go-llsqlite",
	"zombiezen.com/go/sqlite", mod+"pkg/relindex")

// depsEngines is the download engines: the torrent and usenet agents' alone (R6).
var depsEngines = depsRules("the download engines are the engine agents' (R6)",
	"github.com/anacrolix", "github.com/pion", "github.com/Tensai75/nntp", "github.com/javi11",
	"github.com/nwaples/rardecode", "github.com/bodgit/sevenzip", mod+"pkg/download/torrent",
	mod+"pkg/download/usenet", mod+"pkg/socks5")

// depsServers is servers only tests run; the manager's external metrics API
// is hand-rolled (R5).
var depsServers = depsRules("test-only servers; R5's metrics API is hand-rolled",
	"github.com/nats-io/nats-server", "github.com/fergusstrange/embedded-postgres", "k8s.io/apiserver",
	"k8s.io/kube-aggregator", "k8s.io/metrics", "sigs.k8s.io/custom-metrics-apiserver")

// The manager and the ui ship in the distroless static clustarr image
// (images/Dockerfile.clustarr), which has no dynamic loader: purego (ffgo's
// and par2go's way of loading their libraries) makes the Go linker emit a
// dynamically linked executable, and the image's binaries failed to start
// with "exec /clustarr: no such file or directory" (2026-10-01). Carries
// cmd/clustarr's TestClustarrNeverLinksADynamicLoader (spec §4.5.1, W5.14).
func TestManagerNeverLinksADynamicLoader(t *testing.T) {
	g := depsGraph(t, mod+"cmd/manager", linuxStatic...)
	g.Deny(t, depsDynamicAndNative)
	g.Require(t, "sigs.k8s.io/controller-runtime/pkg/manager", "the manager is a controller-runtime manager")
}

// The ui's half of the static image (spec §4.5.2, W5.14).
func TestUINeverLinksADynamicLoader(t *testing.T) {
	g := depsGraph(t, mod+"cmd/ui", linuxStatic...)
	g.Deny(t, depsDynamicAndNative)
	g.Require(t, mod+"ui", "anchor: the web UI")
}

// The manager is the control plane: reconcilers, planners, the remediation
// loop, dispatch and intake (ADR-0018, ADR-0019). It links no worker, agent,
// engine, index storage or ui code, and every app/ package it links is one
// the package tree (spec §4.4, as amended) gives it (spec §4.5.1, W5.14,
// A7.1). An unclassified package is a decision to make, not a pattern to
// widen.
func TestManagerLinksNoAgentCode(t *testing.T) {
	g := depsGraph(t, mod+"cmd/manager", linuxStatic...)
	g.Deny(t, slices.Concat(depsStorage, depsEngines, depsServers,
		depsRules("rendering and subtitle post-processing are agent work", "golang.org/x/image", "github.com/asticode",
			mod+"pkg/overlay/render", mod+"pkg/subtitles/postprocess"),
		depsRules("R6: clients the manager never calls",
			mod+"pkg/subtitles/providers/opensubtitlescom", mod+"pkg/subtitles/providers/gestdown",
			mod+"pkg/subtitles/providers/subdl", mod+"pkg/subtitles/providers/subsource",
			mod+"pkg/subtitles/providers/internal",
			mod+"pkg/importlist/arr", mod+"pkg/importlist/custom", mod+"pkg/importlist/imdbcsv",
			mod+"pkg/importlist/mdblist", mod+"pkg/importlist/plex", mod+"pkg/importlist/stevenlu",
			mod+"pkg/importlist/tmdb"),
		depsRules("R6: agent and ui code", mod+"app/catalog/metadata", mod+"app/indexer/search",
			mod+"app/indexer/query", mod+"app/indexer/facade", mod+"app/indexer/download",
			mod+"app/indexer/clientcache", mod+"app/indexer/health", mod+"app/indexer/blocklist",
			mod+"app/grab/engine", mod+"app/caption/providerset/build", mod+"app/segments", mod+"ui", mod+"test",
			"github.com/a-h/templ", "github.com/Oudwins/tailwind-merge-go"),
		[]depguard.Rule{{
			Regexp: regexp.MustCompile(`^github\.com/mediactl/clustarr/app/[a-z]+/(worker|agent)(/|$)`),
			// The history sink and DLQ projector are the manager's (§4.4).
			Except: []string{mod + "app/catalog/worker/history"},
			Why:    "R6: worker and agent code",
		}},
	))
	for _, p := range []string{
		"app/catalog/controller/movie", "app/grab/controller/downloadclient", "app/autoscale/controller",
		"app/transcode/admission", "app/transcode/controller/pool", "app/remediation", "app/dispatch",
		"app/intake", "app/catalog/worker/history", "app/maintenance",
	} {
		g.Require(t, mod+p, "anchor: a renamed package must not make this guard vacuous")
	}
	g.AllowApp(t, []*regexp.Regexp{
		regexp.MustCompile(`^app/[a-z]+/manager$`),
		regexp.MustCompile(`^app/[a-z]+/controller(/.+)?$`),
		regexp.MustCompile(`^app/[a-z]+/status$`),
		// The control plane's own trees (ADR-0016, ADR-0018, ADR-0019, ADR-0020).
		regexp.MustCompile(`^app/(autoscale|remediation|dispatch|intake|maintenance)(/.+)?$`),
		// segmentplan, not segmenting (deleted with the fold); grabplan and
		// searchplan are the item stages' planners, itemread their cache
		// reads, metadatainputs and metadataset the metadata stage's ask and
		// its one renderer, seriessearch the Series key's index of planned
		// Searches (Wave S); worker/history is the history sink (§4.4).
		regexp.MustCompile(`^app/catalog/(artwork|overlayplan|grabsource|markers|segmentplan|history|history/replay|delay|searchoutcome|episodeorder|grabplan|searchplan|itemread|metadatainputs|metadataset|seriessearch|worker/history)$`),
		regexp.MustCompile(`^app/import/(scanprogress|mediafilespec|mediafilespec/specwrite|importliststate|importtarget|importplan|manager/scanapply)$`),
		regexp.MustCompile(`^app/indexer/(clients|proxy|limits|rssschedule|bundle|bundle/embedded|status/statuspatch)$`),
		regexp.MustCompile(`^app/grab/lifecycle$`),
		// task, jobspec and grafttask are shared with cmd/transcode; the
		// planners and admission are the manager's (§4.4).
		regexp.MustCompile(`^app/transcode/(jobspec|task|grafttask|transcodeplan|graftplan|admission)$`),
		regexp.MustCompile(`^app/caption/(providerset|throttle|itemindex|datapath|subtitleplan)$`),
		// Live TV runs in the manager beside its controllers (Live TV design
		// 2026-10-07, §5): the tuner serves each block's HDHomeRun device and
		// guide, the relay shares one upstream per channel, and index is the
		// controllers' field indexes; leaderendpoint publishes the leader as
		// the livetv Service's endpoint (control-plane placement design §5.4).
		regexp.MustCompile(`^app/livetv/(index|leaderendpoint|relay|tuner)$`),
	})
}

// The manager reaches a metadata client only to probe a MetadataProvider
// (spec §4.5.1, §13 OD10): the clients are the metadata agent's, and extid
// is a constants leaf the comic controller imports.
func TestManagerLinksMetadataClientsOnlyToProbeThem(t *testing.T) {
	g := depsGraph(t, mod+"cmd/manager", linuxStatic...)
	clients := mod + "pkg/metadata/clients/"
	g.Require(t, mod+"app/catalog/controller/metadataprovider", "anchor: the probe")
	g.Require(t, clients+"tmdb", "anchor: a client the probe reaches")
	g.OnlyImportedBy(t,
		func(p string) bool { return strings.HasPrefix(p, clients) && p != clients+"extid" },
		func(p string) bool {
			return p == mod+"app/catalog/controller/metadataprovider" || strings.HasPrefix(p, clients)
		},
		"the manager reaches a metadata client only to probe a MetadataProvider (§13 OD10)")
}

// The ui is a read-only view (R6, spec §4.5.2): no app/ code, no metadata
// client (it asks the gateway over RPC), nothing that writes status or runs
// a controller-runtime manager.
func TestUILinksNoAppOrManagerCode(t *testing.T) {
	g := depsGraph(t, mod+"cmd/ui", linuxStatic...)
	g.Deny(t, slices.Concat(depsStorage, depsEngines,
		depsRules("R6", mod+"app", mod+"pkg/metadata/clients",
			"sigs.k8s.io/controller-runtime/pkg/builder", "sigs.k8s.io/controller-runtime/pkg/leaderelection",
			"sigs.k8s.io/controller-runtime/pkg/webhook", "sigs.k8s.io/controller-runtime/pkg/cluster",
			"github.com/nats-io/nats-server", "github.com/fergusstrange/embedded-postgres", "k8s.io/apiserver"),
		[]depguard.Rule{
			{Path: mod + "pkg/k8s", Exact: true, Why: "PatchStatus and Apply live there; pkg/k8s/conditions is allowed"},
			{Path: "sigs.k8s.io/controller-runtime", Exact: true, Why: "the root re-exports the manager"},
			{Path: "sigs.k8s.io/controller-runtime/pkg/manager", Exact: true, Why: "the ui runs no manager"},
		}))
	g.Require(t, mod+"ui", "anchor")
	g.Require(t, mod+"ui/plex", "anchor")
}

// R13: the agent builds with cgo, so seeding torrents keep the SQLite piece
// completion. The chain go-llsqlite/adapter <- anacrolix/torrent/storage
// exists only under cgo, so a cgo-free agent fails here before every
// seeding torrent would rehash (spec §4.5.3, W5.14).
func TestAgentKeepsTheCgoPieceCompletion(t *testing.T) {
	g := depsGraph(t, mod+"cmd/agent", linuxCgo...)
	g.Require(t, mod+"app/grab/engine/torrent", "anchor")
	g.Require(t, "github.com/go-llsqlite/adapter", "a cgo-free agent would rehash every seeding torrent (R13)")
}

// The agent registers no manager, runs no transcode or segment analysis and
// carries none of the ui or the test servers (R6, spec §4.5.3, W5.14).
func TestAgentLinksNoManagerRegistration(t *testing.T) {
	g := depsGraph(t, mod+"cmd/agent", linuxCgo...)
	g.Deny(t, slices.Concat(
		[]depguard.Rule{{Regexp: regexp.MustCompile(`^github\.com/mediactl/clustarr/app/[a-z]+/manager$`), Why: "R6: manager registration"}},
		depsRules("R6", mod+"app/autoscale", mod+"app/transcode/controller", mod+"app/transcode/worker", mod+"app/segments",
			mod+"pkg/segments/decode", mod+"pkg/segments/textdet", mod+"pkg/segments/chromaprint",
			mod+"pkg/segments/frames", mod+"pkg/segments/align",
			"github.com/shota3506/onnxruntime-purego", mod+"ui", mod+"test", "github.com/a-h/templ",
			"github.com/Oudwins/tailwind-merge-go", "github.com/nats-io/nats-server",
			"github.com/fergusstrange/embedded-postgres", "k8s.io/apiserver", "sigs.k8s.io/custom-metrics-apiserver"),
	))
	g.Require(t, mod+"internal/cli/agent", "anchor: the agent's command")
}

// depsAgentRoots are the agent's domain registrations: every domain the
// agent binary can run, so a scan of its closure covers each of them.
var depsAgentRoots = []string{
	"app/catalog/agent/catalog", "app/catalog/agent/events", "app/catalog/agent/metadata", "app/import/agent",
	"app/indexer/agent", "app/grab/agent/torrent", "app/grab/agent/usenet", "app/caption/agent",
}

// Agents never plan: they answer tasks and report over NATS, and the
// manager plans, admits and writes (ADR-0019 A7.1, agents design §9.2,
// split spec §4.4's amendment). No agent root links a planner, the loop,
// dispatch or intake, or the manager's status and spec writers. The
// transcode planners and admission, the segment and subtitle planners are
// the tree's [M] planners beside A7.1's list.
func TestAgentLinksNoPlanner(t *testing.T) {
	g := depsGraph(t, mod+"cmd/agent", linuxCgo...)
	for _, p := range depsAgentRoots {
		g.Require(t, mod+p, "anchor: every agent domain is in the scan")
	}
	g.Deny(t, slices.Concat(
		depsRules("agents never plan (ADR-0019 §9.2)",
			mod+"app/catalog/grabplan", mod+"app/catalog/searchplan", mod+"app/grab/lifecycle",
			mod+"app/import/importplan", mod+"app/import/manager", mod+"app/intake", mod+"app/dispatch",
			mod+"app/remediation", mod+"app/import/mediafilespec/specwrite", mod+"app/indexer/status/statuspatch"),
		depsRules("the manager's planners (split spec §4.4: [M])",
			mod+"app/catalog/segmentplan", mod+"app/caption/subtitleplan", mod+"app/transcode/transcodeplan",
			mod+"app/transcode/graftplan", mod+"app/transcode/admission"),
	))
}

// From §11.1 step 11 the agent probes and extracts in-process: the import
// and caption domains run FFmpeg through ffgo, never an ffmpeg or ffprobe
// executable (R2, spec §4.5.3, W8.10).
func TestAgentProbesAndExtractsInProcess(t *testing.T) {
	g := depsGraph(t, mod+"cmd/agent", linuxCgo...)
	g.Require(t, "github.com/obinnaokechukwu/ffgo", "the import and caption domains run FFmpeg in-process (R2)")
	g.Require(t, mod+"pkg/mediainfo/native", "the import domain's probe (§6.6)")
	g.Require(t, mod+"pkg/subtitles/providers/embedded/native", "the caption domain's extraction (§7.3)")
	g.Require(t, mod+"pkg/ffruntime", "the runtime gate (§7.4)")
	g.Deny(t, depsRules("R2: no ffmpeg or ffprobe executable", mod+"pkg/mediainfo/ffprobeexec",
		mod+"pkg/subtitles/providers/embedded/execextract", mod+"test"))
}

// The usenet engine repairs through par2go in a child of the agent, not a
// par2 executable (spec §4.5.3, §8.4, §8.9 guard 6, W9.6).
func TestAgentRepairsWithPar2go(t *testing.T) {
	g := depsGraph(t, mod+"cmd/agent", linuxCgo...)
	g.Require(t, mod+"pkg/par2child", "the usenet engine's repair child (§8.4)")
	g.Require(t, "github.com/mediactl/par2go", "par2cmdline-turbo as a library (R2)")
}

// pkg/par2child takes par2go and the standard library only, so any binary
// can take it or refuse it (spec §8.4, §8.9 guard 6, W9.3).
func TestPar2childLinksNoClustarrCode(t *testing.T) {
	g := depsGraph(t, mod+"pkg/par2child", linuxCgo...)
	for _, p := range g.Packages() {
		if strings.HasPrefix(p, mod) && p != mod+"pkg/par2child" {
			t.Errorf("pkg/par2child links %s:\n  %s", p, strings.Join(g.Chain(p), "\n  <- "))
		}
	}
	g.Require(t, "github.com/mediactl/par2go", "anchor")
}

// depsCredentialLess is what neither credential-less native binary links:
// markers' segment pods and transcode's pool and graft pods hold no
// Kubernetes token (ADR-0018, ADR-0019), so no client, no pkg/k8s, no
// pkg/obs (it links controller-runtime), no ui, and nothing of the agents'.
var depsCredentialLess = slices.Concat(
	depsRules("credential-less pod: no Kubernetes client", "k8s.io/client-go", mod+"pkg/k8s"),
	[]depguard.Rule{{Path: mod + "pkg/obs", Exact: true, Why: "pkg/obs links controller-runtime"}},
	depsRules("not this binary's work (R6)", mod+"ui", "github.com/nats-io/nats-server", "modernc.org/sqlite",
		"github.com/jackc/pgx", "github.com/anacrolix", "github.com/mediactl/par2go", mod+"pkg/par2child",
		mod+"pkg/mediainfo/ffprobeexec", mod+"pkg/subtitles/providers/embedded/execextract"),
)

// markers is a credential-less segment analyser (spec §4.5.4, W5.14,
// W7.9): no Kubernetes client or API types (§7.2.9 dropped them), no
// transcode code, and only app/segments/worker of app/. pkg/busconn is
// allowed (Wave N ruling: its checkers are a plain func type).
func TestMarkersImportsNoKubernetesClient(t *testing.T) {
	g := depsGraph(t, mod+"cmd/markers", linuxStatic...)
	g.Deny(t, slices.Concat(depsCredentialLess,
		depsRules("credential-less pod: no manager, client or API types (§7.2.9)",
			"sigs.k8s.io/controller-runtime", "k8s.io/api"),
		depsRules("not markers' work (R6)", mod+"app/transcode"),
	))
	g.AllowApp(t, []*regexp.Regexp{regexp.MustCompile(`^app/segments/worker$`)})
	g.Require(t, mod+"app/segments/worker", "anchor")
	g.Require(t, "github.com/obinnaokechukwu/ffgo", "the decoder")
	g.Require(t, mod+"pkg/ffruntime", "the runtime gate (§7.4)")
	g.Require(t, "github.com/shota3506/onnxruntime-purego/onnxruntime", "the text detector")
}

// transcode is the credential-less pool and graft binary (was the
// transcode worker binary's TestBinaryImportsNoKubernetesClient, widened;
// spec §4.5.5, W5.14). It keeps the catalog and transcode API types, which
// import controller-runtime's pkg/scheme and nothing else of it (§13 OD11).
func TestTranscodeImportsNoKubernetesClient(t *testing.T) {
	g := depsGraph(t, mod+"cmd/transcode", linuxStatic...)
	g.Deny(t, slices.Concat(depsCredentialLess,
		[]depguard.Rule{{
			Path: "sigs.k8s.io/controller-runtime", Except: []string{"sigs.k8s.io/controller-runtime/pkg/scheme"},
			Why: "api/{catalog,transcode}/v1alpha1 import pkg/scheme; nothing else (§13 OD11)",
		}},
		depsRules("not transcode's work (R6)", mod+"app/transcode/controller", "github.com/shota3506/onnxruntime-purego",
			mod+"app/segments", mod+"pkg/segments/decode", mod+"pkg/segments/textdet"),
	))
	g.AllowApp(t, []*regexp.Regexp{regexp.MustCompile(`^app/transcode/(worker(/inprocess|/graft)?|jobspec|task|grafttask)$`)})
	g.Require(t, mod+"app/transcode/worker/inprocess", "anchor")
	g.Require(t, "github.com/obinnaokechukwu/ffgo", "the in-process engine")
	g.Require(t, mod+"pkg/ffruntime", "the runtime gate (§7.4)")
}

// busconn.Shutdown is the only shutdown path in every binary (ADR-0020,
// Wave N N1), so markers and transcode link pkg/busconn; its health
// checkers are a plain func type precisely so that linking it drags in no
// controller-runtime (Wave N ruling). This holds both halves: busconn
// itself links none, and neither credential-less binary links any beyond
// the API types' pkg/scheme.
func TestMarkersAndTranscodeLinkNoControllerRuntime(t *testing.T) {
	busconn := depsGraph(t, mod+"pkg/busconn", linuxStatic...)
	busconn.Deny(t, depsRules("busconn's checkers are a plain func type (Wave N)", "sigs.k8s.io/controller-runtime",
		"k8s.io/client-go", mod+"pkg/k8s"))
	busconn.Require(t, "github.com/nats-io/nats.go", "anchor: busconn dials NATS")
	for bin, except := range map[string][]string{
		"cmd/markers":   nil,
		"cmd/transcode": {"sigs.k8s.io/controller-runtime/pkg/scheme"},
	} {
		g := depsGraph(t, mod+bin, linuxStatic...)
		g.Require(t, mod+"pkg/busconn", "busconn.Shutdown is the only shutdown path (ADR-0020)")
		g.Deny(t, []depguard.Rule{{Path: "sigs.k8s.io/controller-runtime", Except: except, Why: "a credential-less pod runs no controller-runtime"}})
	}
}

// The MediaFile domain the remediation loop calls runs in the manager,
// which never probes (R3): it plans a probe and reads its record, so it
// links no prober (F3.4, which replaced the MediaFile reconciler's
// TestTheMediaFileReconcilerLinksNoProbe, its list unchanged). The loop's
// probe planner is held with it.
func TestTheMediaFileDomainLinksNoProbe(t *testing.T) {
	for _, root := range []string{"app/catalog/controller/mediafile", "app/remediation/probe"} {
		g := depsGraph(t, mod+root, linuxStatic...)
		g.Require(t, mod+"pkg/mediainfo", "anchor: the domain reads the probe's model")
		g.Deny(t, depsRules("the manager never probes (R3)",
			mod+"pkg/mediainfo/ffprobeexec", mod+"pkg/mediainfo/native", mod+"app/import/worker/probe",
			"github.com/obinnaokechukwu/ffgo", "github.com/ebitengine/purego"))
	}
}
