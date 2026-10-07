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

package fileimport

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/release"
)

// videoProbeTimeout bounds probeVideo's probe (the container, then the first
// frame, read in-process by the domain's Prober). A healthy file answers in well under a second, even over a
// network mount; one that hangs -- a stalled mount, a pathological file --
// must not hold the import handler past its delivery's acknowledgement
// deadline, ConsumerImportFile's BackOff[0] (30s; HeartbeatInterval says
// why not its AckWait), so a timeout is a probe failure like any other and
// the file imports under its name. probeVideo heartbeats immediately before
// it probes, and TestTheImportFitsTheFileConsumersAckDeadline holds this
// and HeartbeatInterval to that deadline together.
const videoProbeTimeout = 15 * time.Second

// parseMediaFile parses a media file's name, and when that names nothing
// -- an obfuscated post's "2ef6f194995e4a11b055d0f2354ef0ba.mp4", the
// first grab on the owner's cluster (2026-09-24) -- the release title the
// Download carries: Radarr's ImportDecisionMaker falls back from the
// file's name (FileMovieInfo) to the download client item's title, and
// Sonarr does the same for a single episode. The Download already names
// the item, so the parse serves the quality, revision, group and
// languages, which the release title carries as well as any file name.
// An empty releaseTitle (a pack, whose title names none of its files) is
// no fallback, and the file's own parse error is the one reported.
func parseMediaFile(srcPath, releaseTitle string, kind commonv1.MediaKind) (*release.ParsedRelease, error) {
	p, err := release.ParsePath(srcPath, release.Options{Kind: kind})
	if err == nil || releaseTitle == "" {
		return p, err
	}
	fp, ferr := release.Parse(releaseTitle, release.Options{Kind: kind})
	if ferr != nil {
		return nil, err
	}
	return fp, nil
}

// probeVideo probes a video file an import is about to judge: its
// technical description corrects the name-derived quality
// (quality.AugmentFromMediaInfo) and names the file for its codec and
// dynamic range (catalogctx.File). A probe failure never fails the import
// -- an unprobeable file imports under its name-derived quality and its
// source extension, exactly as it did before imports probed, since every
// MediaInfo block of a preset is optional -- so it is logged and nil
// returned. The probe gets videoProbeTimeout, and running out of it is
// such a failure. It heartbeats on m immediately before it probes, so the
// probe's whole bound lies inside the delivery's ack deadline however long
// the file loop has gone since its last beat; a failed heartbeat is the
// error, which aborts the import as the loop's own heartbeat failure does.
// It probes through w.Prober, the import domain's one prober (spec
// 2026-10-06 §6.6); a worker with none probes nothing and returns nil, nil,
// so the file imports under its name-derived quality.
func (w *Worker) probeVideo(ctx context.Context, m events.Message, srcPath, rel string) (*commonv1.MediaInfo, error) {
	if w.Prober == nil {
		return nil, nil
	}
	if err := heartbeat(ctx, m); err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, videoProbeTimeout)
	defer cancel()
	mi, _, err := w.Prober.Probe(pctx, srcPath)
	if err != nil {
		logging.FromContext(ctx).Warn("fileimport: could not probe the file; importing it under its name-derived quality",
			"source", rel, "error", err)
		return nil, nil
	}
	return mi, nil
}

// importText is s as a MediaFile spec field bounded at maxBytes holds it:
// the runes a server-side apply cannot carry replaced, then cut on a rune
// boundary (loop spec §2.11.2). A Download's release fields are unbounded,
// and the apiserver refuses an over-long one whole, which would fail the
// import.
func importText(s string, maxBytes int) string {
	return k8s.ClampText(k8s.SanitizeText(s), maxBytes)
}

// relPath renders srcPath relative to root, matching
// ImportedFile.SourcePath's documented contract ("the path the file had
// inside the download"). It falls back to the absolute path when the two
// are unrelated, mirroring app/import/worker/rescan.relPath.
func relPath(root, path string) string {
	if root == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "" || rel == "." || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

// capMatchedFormats caps the matched-formats list at MediaFileSpec.
// MatchedFormats' own MaxItems (200), keeping the highest-scoring surprise
// out of the list is not this function's job -- catalogue.Profile.Score
// already returns every format that matched, and 200 formats matching one
// release would itself be a catalogue bug, not a normal case this needs to
// handle gracefully beyond not exceeding the CRD's cap.
func capMatchedFormats(matched []string) []string {
	const maxMatchedFormats = 200
	if len(matched) <= maxMatchedFormats {
		return matched
	}
	return matched[:maxMatchedFormats]
}
