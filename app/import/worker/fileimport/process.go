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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/release"
)

// videoProbeTimeout bounds probeVideo's probe (the container, then the first
// frame, read in-process by the domain's Prober). A healthy file answers in well under a second, even over a
// network mount; one that hangs -- a stalled mount, a pathological file, a
// node busy reading other files end to end -- must not hold the import
// handler past its delivery's acknowledgement deadline,
// ConsumerImportFile's BackOff[0] (30s; HeartbeatInterval says why not its
// AckWait), so the probe is given up on and the file left unjudged
// (errUnjudged), for the import's retry. probeVideo heartbeats immediately
// before it probes, and TestTheImportFitsTheFileConsumersAckDeadline holds
// this and HeartbeatInterval to that deadline together.
const videoProbeTimeout = 15 * time.Second

// errUnjudged is a video file the import ran out of time to judge: its
// probe, or its end-to-end read, hit its deadline. Nothing is known of the
// file, so it is neither imported nor the release's fault: the caller
// rejects it as transient (notMediaRejection), the import is inspected
// again on its retry ladder and held for a person after the last.
//
// Until 2026-11-27 a probe out of time imported the file under its name
// with no end-to-end read at all. On talos that night eleven single-episode
// downloads of one Gosick release arrived with fourteen to sixteen of their
// twenty-four minutes zeroed; ten were refused, and S01E22's probe timed
// out while the two import pods read the others end to end, so it went into
// the library unread (owner, 2026-11-27: "Do all 3").
var errUnjudged = errors.New("could not be judged")

// hashedNameRegex is a file name that is nothing but hexadecimal digits, as
// an obfuscating uploader leaves it.
var hashedNameRegex = regexp.MustCompile(`^[0-9a-fA-F]{24,}$`)

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
	var (
		p   *release.ParsedRelease
		err error
	)
	if base := strings.TrimSuffix(filepath.Base(srcPath), filepath.Ext(srcPath)); hashedNameRegex.MatchString(base) {
		// An obfuscated name is no name: read as one, "5e21e3e90faf...mkv"
		// was S05E21 and "00e74c75a59a...mkv" S00E74 (talos, 2026-11-24,
		// two files of hashed packs), and a hash that happened to spell an
		// episode the series has would have been imported as it.
		err = fmt.Errorf("release: %q is a hash, not a name", base)
	} else {
		p, err = release.ParsePath(srcPath, release.Options{Kind: kind})
	}
	if err == nil || releaseTitle == "" {
		return p, err
	}
	fp, ferr := release.Parse(releaseTitle, release.Options{Kind: kind})
	if ferr != nil {
		return nil, err
	}
	return fp, nil
}

// fillFromRelease gives a file whose name names no source -- one named for
// its episode alone, "S01E01 - Title.mkv", as a season pack's files often
// are -- the quality, revision, group and languages releaseTitle names, as
// Sonarr aggregates a pack's files from the download client item's title.
// Such a name is no release name, so the group it seems to carry is none
// either (A Sign of Affection's "[PRiVATE]-[EnCrYpTnZb]-[S01E01-...]" read
// as PRiVATE, talos 2026-10-27); every file of a pack is the release's.
// The probe still corrects the resolution (readVideo), and a name that
// names its own source keeps everything it names.
func fillFromRelease(p *release.ParsedRelease, releaseTitle string, kind commonv1.MediaKind) {
	if (p.Quality.Source != "" && p.Quality.Source != commonv1.SourceUnknown) || releaseTitle == "" {
		return
	}
	r, err := release.Parse(releaseTitle, release.Options{Kind: kind})
	if err != nil || r.Quality.Source == "" || r.Quality.Source == commonv1.SourceUnknown {
		return
	}
	p.Quality, p.Revision, p.Group = r.Quality, r.Revision, r.Group
	if p.LanguageUnknown {
		p.Languages, p.LanguageUnknown, p.DualAudio, p.NamesOriginal = r.Languages, r.LanguageUnknown, r.DualAudio, r.NamesOriginal
	}
}

// takeNumbering gives p the episode numbering r carries, and the release
// type that numbering makes, keeping everything else p's name says.
func takeNumbering(p, r *release.ParsedRelease) {
	p.Seasons, p.Episodes, p.Absolute, p.AirDate = slices.Clone(r.Seasons), slices.Clone(r.Episodes), slices.Clone(r.Absolute), r.AirDate
	p.FullSeason, p.MultiSeason, p.Partial, p.Special = r.FullSeason, r.MultiSeason, r.Partial, r.Special
	p.ReleaseType = r.ReleaseType
}

// probeVideo probes a video file an import is about to judge: its
// technical description corrects the name-derived quality
// (quality.AugmentFromMediaInfo) and names the file for its codec and
// dynamic range (catalogctx.File). A file the prober found is no media
// (mediainfo.ErrNotMedia: empty, or bytes no demuxer reads), or one whose
// bytes have holes (mediainfo.ErrDamaged, videoHealth), is notMedia,
// which the caller refuses as the release's fault: imported by name, a fake
// release's encrypted "remuxes" and empty and zero-headed downloads reached
// the library on talos (2026-10-15). A probe that ran out of
// videoProbeTimeout, or an end-to-end read out of its own bound, leaves the
// file unjudged (errUnjudged, returned as notMedia is): the caller rejects
// it as transient and the import is retried. Any other probe failure never
// fails the import -- the file imports under its name-derived quality and
// its source extension, as it did before imports probed, since every
// MediaInfo block of a preset is optional -- but it is still read end to
// end first: a header the prober cannot describe says nothing of the bytes
// behind it. It heartbeats on m immediately before it probes, so the
// probe's whole bound lies inside the delivery's ack deadline however long
// the file loop has gone since its last beat; a failed heartbeat is the
// error, which aborts the import as the loop's own heartbeat failure does.
// It probes through w.Prober, the import domain's one prober (spec
// 2026-10-06 §6.6); a worker with none probes nothing and returns nil, nil,
// so the file imports under its name-derived quality. The raw probe comes
// back beside the summary for what the summary leaves out: the container's
// own tags (mediainfo.FormatTag), which name a hashed pack file.
func (w *Worker) probeVideo(ctx context.Context, m events.Message, srcPath, rel string) (mi *commonv1.MediaInfo, raw *mediainfo.Raw, notMedia, err error) {
	if w.Prober == nil {
		return nil, nil, nil, nil
	}
	if err := heartbeat(ctx, m); err != nil {
		return nil, nil, nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, videoProbeTimeout)
	defer cancel()
	mi, raw, perr := w.Prober.Probe(pctx, srcPath)
	switch {
	case errors.Is(perr, mediainfo.ErrNotMedia):
		logging.FromContext(ctx).Warn("fileimport: the file is not media; refusing it", "source", rel, "err", perr)
		return nil, nil, perr, nil
	case perr != nil && ctx.Err() != nil:
		// The handler's own context ended, not the probe's bound: the
		// delivery is redelivered, as any walk cut short is.
		return nil, nil, nil, ctx.Err()
	case perr != nil && outOfTime(pctx, perr):
		logging.FromContext(ctx).Warn("fileimport: the probe ran out of time; leaving the file unjudged for the import's retry",
			"source", rel, "err", perr, "bound", videoProbeTimeout)
		return nil, nil, fmt.Errorf("%w: its probe did not answer in %s", errUnjudged, videoProbeTimeout), nil
	case perr != nil:
		logging.FromContext(ctx).Warn("fileimport: could not probe the file; reading it end to end, then importing it under its name-derived quality",
			"source", rel, "err", perr)
		mi, raw = nil, nil
	}
	refused, err := w.videoHealth(ctx, m, srcPath, rel)
	if err != nil || refused != nil {
		return nil, nil, refused, err
	}
	return mi, raw, nil, nil
}

// outOfTime reports whether err is bound's deadline: the bound has passed,
// or the prober says the call ran out of its time (it may answer a moment
// before the context's own timer fires).
func outOfTime(bound context.Context, err error) bool {
	return errors.Is(bound.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, mediainfo.ErrProbeAbandoned)
}

// healthTimeout bounds videoHealth's read of a file of size bytes: a minute,
// and a second for every 8 MiB, so a file on a mount that has stalled is
// given up on, and one that is merely large is read out.
func healthTimeout(size int64) time.Duration {
	return time.Minute + time.Duration(size>>23)*time.Second
}

// videoHealth reads a probed video file end to end (mediainfo.HealthProber)
// and returns mediainfo.ErrDamaged for one with parts of its timeline gone,
// which the caller refuses as the release's fault, as it refuses a file
// that is no media. A header probes whole whatever the bytes behind it
// hold: two episodes of Fena: Pirate Princess came off usenet with 17% and
// 40% of their articles never fetched (talos, 2026-11-17), were imported
// under their names, and were transcoded with a quarter and more of their
// frames gone (owner, 2026-11-17: "We should verify the video files are
// healthy before considering a release ready for transfer and next
// steps"). The read is the whole file's bytes, so it heartbeats on m every
// HeartbeatInterval while it runs; a failed heartbeat ends it and is the
// error. A read that ran out of healthTimeout leaves the file unjudged
// (errUnjudged), for the import's retry: it imported unread until
// 2026-11-27, which is the hole a busy node opens. A file the read cannot
// open as media is refused as one the probe found so (it is reached when
// the probe failed some other way). Any other read that could not finish
// -- a prober that does not read files so -- judges nothing, and the file
// imports as it did.
func (w *Worker) videoHealth(ctx context.Context, m events.Message, srcPath, rel string) (refused, err error) {
	hp, ok := w.Prober.(mediainfo.HealthProber)
	if !ok {
		return nil, nil
	}
	st, err := os.Stat(srcPath)
	if err != nil {
		return nil, nil //nolint:nilerr // the import's own read of the file reports it.
	}
	if err := heartbeat(ctx, m); err != nil {
		return nil, err
	}
	hctx, cancel := context.WithTimeout(ctx, healthTimeout(st.Size()))
	defer cancel()
	stop, beat := make(chan struct{}), make(chan error, 1)
	go func() {
		t := time.NewTicker(HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				beat <- nil
				return
			case <-t.C:
				if err := heartbeat(ctx, m); err != nil {
					cancel()
					beat <- err
					return
				}
			}
		}
	}()
	started := time.Now()
	h, herr := hp.Health(hctx, srcPath)
	close(stop)
	if err := <-beat; err != nil {
		return nil, err
	}
	log := logging.FromContext(ctx)
	switch {
	case herr != nil && ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(herr, mediainfo.ErrNotMedia):
		log.Warn("fileimport: the file is not media; refusing it", "source", rel, "err", herr)
		return herr, nil
	case herr != nil && outOfTime(hctx, herr):
		bound := healthTimeout(st.Size())
		log.Warn("fileimport: the end-to-end read ran out of time; leaving the file unjudged for the import's retry",
			"source", rel, "err", herr, "bound", bound, "took", time.Since(started).Round(time.Millisecond))
		return fmt.Errorf("%w: reading it end to end did not finish in %s", errUnjudged, bound), nil
	case herr != nil:
		log.Warn("fileimport: could not read the file end to end; importing it unjudged", "source", rel, "err", herr)
		return nil, nil
	case h.Damaged():
		log.Warn("fileimport: the file is damaged; refusing it", "source", rel, "err", h.Err(), "took", time.Since(started).Round(time.Millisecond))
		return h.Err(), nil
	}
	log.Debug("fileimport: read the file end to end", "source", rel, "judged", h.Judged, "holes", h.Holes,
		"missing", h.Missing, "took", time.Since(started).Round(time.Millisecond))
	return nil, nil
}

// importText is s as a MediaFile spec field bounded at maxBytes holds it:
// the runes a server-side apply cannot carry replaced, then cut on a rune
// boundary (loop spec §2.11.2). A Download's release fields are unbounded,
// and the apiserver refuses an over-long one whole, which would fail the
// import.
func importText(s string, maxBytes int) string {
	return k8s.ClampText(k8s.SanitizeText(s), maxBytes)
}

// relPath renders srcPath relative to root: the path the file had inside
// the download. It falls back to the absolute path when the two
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
