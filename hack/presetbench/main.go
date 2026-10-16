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

// Command presetbench encodes each sample the way a transcode pool worker
// would -- the standard's plan for the sample on this pod's measured
// device, through the in-process engine -- once per preset, video only,
// and prints one JSON line per encode: its size, its wall time and where
// it wrote it. It answers "how much bigger is a faster preset" on the real
// device (2026-10-16: QSV veryslow against medium on the owner's NUC).
//
//	presetbench -class intel -presets veryslow,slow,medium -out /scratch/out /scratch/s1.mkv ...
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/app/transcode/worker/inprocess"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

type result struct {
	Sample   string  `json:"sample"`
	Preset   string  `json:"preset"`
	Encoder  string  `json:"encoder"`
	Decode   string  `json:"decode"`
	Action   string  `json:"action"`
	Bytes    int64   `json:"bytes,omitempty"`
	Seconds  float64 `json:"seconds,omitempty"`
	Output   string  `json:"output,omitempty"`
	Error    string  `json:"error,omitempty"`
	Duration float64 `json:"sampleSeconds"`
}

func main() {
	class := flag.String("class", "intel", "the pool hardware class whose device to measure and encode on")
	presets := flag.String("presets", "veryslow,medium", "comma-separated presets, in the encoder's own names")
	out := flag.String("out", "/scratch/out", "directory for the encodes")
	cpuDecode := flag.Bool("cpu-decode", false, "decode on the CPU even where the device measured a GPU decode")
	audio := flag.Bool("audio", false, "keep the plan's audio, as a job does")
	flag.Parse()
	if err := run(context.Background(), transcode.Hardware(*class), strings.Split(*presets, ","), *out, *cpuDecode, *audio, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "presetbench:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, class transcode.Hardware, presets []string, out string, cpuDecode, audio bool, samples []string) error {
	eng, err := inprocess.New(class)
	if err != nil {
		return err
	}
	m, err := eng.Measure(ctx, class)
	if err != nil {
		return fmt.Errorf("measure %s: %w", class, err)
	}
	if cpuDecode {
		m.Limits.VAAPI, m.Limits.NVDEC = nil, &transcode.Decoders{Formats: map[string]bool{}}
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	// The live profile's settings are its defaults (hevc-mkv sets none of
	// quality, codecs or modifiers).
	profile := jobspec.StandardProfile("bench", "bench", transcodev1alpha1.TranscodeProfileSpec{Hardware: transcodev1alpha1.Hardware(class)})
	enc := json.NewEncoder(os.Stdout)
	for _, s := range samples {
		mi, raw, err := eng.Probe(ctx, s)
		if err != nil {
			return fmt.Errorf("probe %s: %w", s, err)
		}
		info, err := transcode.FromProbe(mi, raw)
		if err != nil {
			return fmt.Errorf("read %s: %w", s, err)
		}
		plan := standard.Plan(info, profile, standard.Hardware{Tier: m.Tier, Limits: m.Limits})
		// Video alone, unless asked: the presets differ only there.
		if !audio {
			plan.Audio = nil
		}
		plan.Subtitles, plan.Sidecars = nil, nil
		base := result{Sample: filepath.Base(s), Encoder: plan.Video.Encoder, Decode: plan.Video.Decode, Action: plan.Video.Action, Duration: info.Format.Duration.Seconds()}
		if plan.Video.Action != "encode" {
			_ = enc.Encode(base)
			continue
		}
		for _, preset := range presets {
			p := plan
			p.Video.Options = maps.Clone(plan.Video.Options)
			p.Video.Options["preset"] = preset
			r := base
			r.Preset = preset
			r.Output = filepath.Join(out, strings.TrimSuffix(filepath.Base(s), filepath.Ext(s))+"."+preset+".mp4")
			start := time.Now()
			if tail, err := eng.Encode(ctx, p, m.Tier, s, r.Output, nil); err != nil {
				r.Error = fmt.Sprintf("%v: %s", err, tail)
			} else if st, err := os.Stat(r.Output); err == nil {
				r.Bytes = st.Size()
			}
			r.Seconds = time.Since(start).Seconds()
			_ = enc.Encode(r)
		}
	}
	return nil
}
