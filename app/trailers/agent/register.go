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

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jonboulle/clockwork"
	ctrl "sigs.k8s.io/controller-runtime"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo/native"
	"github.com/mediactl/clustarr/pkg/trailers"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
	"github.com/mediactl/clustarr/pkg/ytdlp"
)

// DefaultYtDlpPath is where the trailers image holds yt-dlp.
const DefaultYtDlpPath = "/usr/local/bin/yt-dlp"

// rpcTimeout bounds one request to the metadata gateway; a miss there is a
// request to Plex or TMDB behind its limiter.
const rpcTimeout = 60 * time.Second

// Options is what the trailers domain's Register takes.
type Options struct {
	k8s.Options
	// DataDir is where the RWX media volume is mounted; trailers are
	// placed under the root folders on it, and trailers.data proves it
	// writable.
	DataDir string
	// YtDlpPath is the yt-dlp program.
	YtDlpPath string
	// Language is the language a trailer is preferred in; English when
	// empty.
	Language string
}

// Register adds the sweeper as a runnable on every replica -- the domain
// runs one -- and the domain's checks: trailers.data, and the process's
// ffgo liveness, since the join and the probe run FFmpeg in process. The
// domain does not start without the yt-dlp program, FFmpeg 9's MP4 muxer
// or the media volume.
func Register(_ context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
	if bus == nil {
		return catalogagent.Registration{}, errors.New("trailers domain: Register needs the bus")
	}
	if o.DataDir == "" {
		return catalogagent.Registration{}, errors.New("trailers domain: a data directory is required")
	}
	if o.YtDlpPath == "" {
		o.YtDlpPath = DefaultYtDlpPath
	}
	if fi, err := os.Stat(o.YtDlpPath); err != nil || fi.IsDir() {
		return catalogagent.Registration{}, fmt.Errorf("trailers domain: no yt-dlp program at %s: this domain runs in the trailers image", o.YtDlpPath)
	}
	if err := ffruntime.Require(ffruntime.Needs{Demuxers: []string{"mov"}, Muxers: []string{"mp4"}}); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("trailers domain: joining a trailer is unavailable: %w", err)
	}
	prober, err := native.New()
	if err != nil {
		return catalogagent.Registration{}, fmt.Errorf("trailers domain: %w", err)
	}
	var live, ready k8s.Checks
	if err := live.Add("ffgo", ffruntime.Healthz); err != nil {
		return catalogagent.Registration{}, err
	}
	if err := ready.Add("trailers.data", k8s.DataReadyChecker(o.DataDir)); err != nil {
		return catalogagent.Registration{}, err
	}

	fetcher := ytdlp.Fetcher{Path: o.YtDlpPath}
	language := o.Language
	if language == "" {
		language = "en"
	}
	s := &Sweeper{
		Reader: mgr.GetClient(), Namespace: o.Namespace, KV: bus.KV(events.BucketTrailers),
		PlexExtras: func(ctx context.Context, plexID string) ([]json.RawMessage, error) {
			ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			var resp schema.PlexExtrasResponse
			if err := bus.Request(ctx, events.RPCMetadataExtras, schema.PlexExtrasRequest{PlexID: plexID}, &resp); err != nil {
				return nil, err
			}
			if resp.Error != "" {
				return nil, errors.New(resp.Error)
			}
			return resp.Extras, nil
		},
		Videos: func(ctx context.Context, kind commonv1.MediaKind, ids map[string]string) ([]trailers.Video, error) {
			ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			defer cancel()
			var resp schema.TrailersResponse
			if err := bus.Request(ctx, events.RPCMetadataTrailers, schema.TrailersRequest{Kind: kind, IDs: ids}, &resp); err != nil {
				return nil, err
			}
			if resp.Error != "" {
				return nil, errors.New(resp.Error)
			}
			return resp.Videos, nil
		},
		Fetch: fetcher.Fetch,
		Mux:   engine.MuxPair,
		Probe: func(ctx context.Context, path string) (Facts, error) {
			mi, _, err := prober.Probe(ctx, path)
			if err != nil {
				return Facts{}, err
			}
			f := Facts{DurationMillis: mi.RuntimeMillis, Width: int(mi.Width), Height: int(mi.Height), VideoCodec: mi.VideoCodec}
			if len(mi.Audio) > 0 {
				f.AudioCodec = mi.Audio[0].Codec
			}
			if fi, err := os.Stat(path); err == nil {
				f.SizeBytes = fi.Size()
			}
			return f, nil
		},
		Place:      fsops.MoveAtomic,
		ScratchDir: os.TempDir(),
		Language:   language,
		Clock:      clockwork.NewRealClock(),
	}
	if err := mgr.Add(k8s.EveryReplica(s.Run)); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("trailers domain: %w", err)
	}
	return catalogagent.Registration{Ready: &ready, Live: &live}, nil
}
