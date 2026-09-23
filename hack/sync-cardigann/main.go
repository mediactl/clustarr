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

// Command sync-cardigann fetches the Cardigann v11 indexer definitions from
// Prowlarr/Indexers at a pinned commit, keeps the ones pkg/cardigann loads
// (cardigann.LoadBundle: schema-valid, an encoding and certificates the
// engine can honour, under the 1 MiB IndexerDefinition cap, unique ids), and
// writes them to an empty directory: as the raw upstream YAML (-format
// yaml), a bundle directory for cardigann.LoadBundle, or as
// IndexerDefinition manifests (-format crd) for `kubectl apply -f`. A
// SOURCE.txt beside them records the commit, the licence position and every
// file refused, with the reason.
//
//	go run ./hack/sync-cardigann -out /tmp/cardigann            # fetch the pinned commit
//	go run ./hack/sync-cardigann -out defs -format crd          # IndexerDefinition manifests
//	go run ./hack/sync-cardigann -src ~/src/Indexers -out defs  # from a local checkout
//
// It is the one network fetch for the corpus and it runs here, by hand,
// never inside `go test`.
//
// # Why the corpus is not vendored (ruling R-13)
//
// R-13 allows vendoring the upstream definitions only under a
// GPL-3.0-compatible licence. Verified on 2026-09-23 against the pinned
// commit, 0339aa920bb468cce384a2c4b5ed2932fcfe4b73 (committed
// 2026-09-23T04:53:53+02:00, "jackett indexers as of c24c9dd..."):
//
//   - The tree has no LICENSE (or COPYING) file, and the GitHub API reports
//     `"license": null` for Prowlarr/Indexers (GET /repos/Prowlarr/Indexers;
//     GET /repos/Prowlarr/Indexers/license is a 404). README.md and
//     CONTRIBUTING.md state no licence.
//   - The repository once had one. Its root commit is Jackett's own
//     (cdc829ce, 2015-04-13, Matthew Little, "Initial commit"), which adds a
//     340-line LICENSE carrying the GNU GPL version 2 text. Commit 69e39d88
//     (2020-10-19, ta264, also titled "Initial commit") deleted it together
//     with the rest of Jackett's tree when the definitions moved to
//     definitions/v1..v11. The definitions are still synced daily from
//     Jackett (README: "synced upstream with Jackett").
//   - Jackett's LICENSE today is that same GPL v2 text, byte for byte (md5
//     2c1c00f9d3ed9e24fa69b932b7e7aff2 for both), and GitHub reports it as
//     GPL-2.0. Neither it nor Jackett's README grants "or (at your option)
//     any later version".
//
// A file under no licence grants nothing, and GPL-2.0-only is not
// compatible with GPL-3.0 (the FSF's compatibility matrix), so Clustarr --
// GPL-3.0-or-later -- cannot embed these files in its binary or its source
// tree. This tool fetches them onto an operator's machine for their own
// cluster; its output is theirs, not part of Clustarr. Should the upstream
// licence change, vendoring is: run this with -out indexarr/definitions,
// embed that directory, and hand the embed.FS to cardigann.LoadBundle.
//
// At the pinned commit definitions/v11 holds 557 definitions, 3,595,106
// bytes, and its schema.json is byte-identical to the one pkg/cardigann
// embeds; SOURCE.txt reports how many the engine accepts.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

// pinnedCommit is the Prowlarr/Indexers commit this tool fetches by default.
// Move it deliberately: a new commit can carry definitions the engine does
// not support, and SOURCE.txt is where that shows.
const pinnedCommit = "0339aa920bb468cce384a2c4b5ed2932fcfe4b73"

// archiveURL is GitHub's tarball endpoint; %s is the commit.
const archiveURL = "https://codeload.github.com/Prowlarr/Indexers/tar.gz/%s"

func main() {
	var opts options
	flag.StringVar(&opts.Commit, "commit", pinnedCommit, "Prowlarr/Indexers commit to fetch")
	flag.StringVar(&opts.URL, "url", archiveURL, "tarball URL; %s is replaced by -commit")
	flag.StringVar(&opts.Src, "src", "", "read a local Prowlarr/Indexers checkout instead of fetching (its definitions/v11)")
	flag.StringVar(&opts.Out, "out", "", "output directory; must not exist or be empty (required)")
	flag.StringVar(&opts.Format, "format", formatYAML, "yaml (raw definitions, a cardigann.LoadBundle directory) or crd (IndexerDefinition manifests)")
	flag.Parse()

	if opts.Out == "" {
		fmt.Fprintln(os.Stderr, "sync-cardigann: -out is required")
		flag.Usage()
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, licenceNotice)

	rep, err := run(opts, &http.Client{Timeout: 5 * time.Minute})
	if err != nil {
		fmt.Fprintln(os.Stderr, "sync-cardigann:", err)
		os.Exit(1)
	}
	fmt.Println(rep.Summary())
}
