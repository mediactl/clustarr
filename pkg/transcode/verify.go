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

package transcode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Report is Verifier.Verify's result.
type Report struct {
	OK             bool
	Problems       []string
	DurationMillis int64
	Streams        int32
	SizeBytes      int64
}

// Verifier probes src and dst with ffprobe and compares the result against
// an Expectation.
type Verifier struct{ FFprobePath string }

// NewVerifier returns a Verifier that shells out to ffprobePath.
func NewVerifier(ffprobePath string) Verifier { return Verifier{FFprobePath: ffprobePath} }

// probeSummary is the handful of ffprobe fields CompareProbes needs, pulled
// out of the full JSON response.
type probeSummary struct {
	durationMillis int64
	streams        int32
	videoCodec     string
	pixFmt         string
}

// ffprobeJSON is the subset of `ffprobe -show_format -show_streams
// -print_format json` this package reads.
type ffprobeJSON struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		PixFmt    string `json:"pix_fmt"`
	} `json:"streams"`
}

func (v Verifier) probe(ctx context.Context, path string) (probeSummary, error) {
	out, err := exec.CommandContext(ctx, v.FFprobePath, "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", path).Output()
	if err != nil {
		return probeSummary{}, fmt.Errorf("transcode: verify: probe %s: %w", path, err)
	}

	var data ffprobeJSON
	if err := json.Unmarshal(out, &data); err != nil {
		return probeSummary{}, fmt.Errorf("transcode: verify: decode probe of %s: %w", path, err)
	}

	// ffprobe's format.duration is a decimal-seconds string; parsed to
	// milliseconds immediately below. The float64 is a local, immediately
	// converted intermediate -- never an exported value.
	durSec, _ := strconv.ParseFloat(data.Format.Duration, 64)

	sum := probeSummary{
		durationMillis: int64(durSec * 1000),
		streams:        int32(len(data.Streams)),
	}
	for _, s := range data.Streams {
		if s.CodecType == "video" && sum.videoCodec == "" {
			sum.videoCodec = s.CodecName
			sum.pixFmt = s.PixFmt
		}
	}
	return sum, nil
}

// Verify probes src and dst independently (rather than trusting a
// pre-computed duration, which can drift) and checks: |dst duration - src
// duration| <= exp.DurationTolMillis; len(dst streams) == exp.Streams; the
// dst video stream's codec_name == exp.VideoCodec and pix_fmt ==
// exp.PixelFormat; dst file size >= exp.MinOutputBytes. Packet-count
// equality (note §8 item 3) and VMAF (note §8 item 5) are reserved by
// VerifySpec but NOT implemented here -- out of this task's stated scope.
func (v Verifier) Verify(ctx context.Context, src, dst string, exp Expectation) (*Report, error) {
	ctx, span := tracing.Start(ctx, "transcode.verify")
	defer span.End()
	logger := logging.FromContext(ctx)

	srcProbe, err := v.probe(ctx, src)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	dstProbe, err := v.probe(ctx, dst)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	info, err := os.Stat(dst)
	if err != nil {
		err = fmt.Errorf("transcode: verify: stat %s: %w", dst, err)
		tracing.RecordError(span, err)
		return nil, err
	}

	report := CompareProbes(srcProbe.durationMillis, dstProbe.durationMillis, dstProbe.streams,
		dstProbe.videoCodec, dstProbe.pixFmt, info.Size(), exp)
	report.DurationMillis = dstProbe.durationMillis
	report.Streams = dstProbe.streams
	report.SizeBytes = info.Size()

	if report.OK {
		logger.Info("transcode: verify passed", "durationMillis", report.DurationMillis, "streams", report.Streams)
	} else {
		logger.Warn("transcode: verify failed", "problems", report.Problems)
	}
	return report, nil
}

// CompareProbes is Verify's pure comparator, exported so it is testable
// without an ffprobe binary. Every failing check appends its own problem;
// checks never short-circuit each other.
func CompareProbes(srcDurationMillis, dstDurationMillis int64, dstStreams int32, dstVideoCodec, dstPixFmt string, dstSizeBytes int64, exp Expectation) *Report {
	r := &Report{}

	delta := dstDurationMillis - srcDurationMillis
	if delta < 0 {
		delta = -delta
	}
	if delta > exp.DurationTolMillis {
		r.Problems = append(r.Problems, fmt.Sprintf("duration drifted %dms beyond tolerance %dms", delta, exp.DurationTolMillis))
	}

	if dstStreams != exp.Streams {
		r.Problems = append(r.Problems, fmt.Sprintf("stream count %d, want %d", dstStreams, exp.Streams))
	}

	if dstVideoCodec != exp.VideoCodec {
		r.Problems = append(r.Problems, fmt.Sprintf("video codec %q, want %q", dstVideoCodec, exp.VideoCodec))
	}

	if dstPixFmt != exp.PixelFormat {
		r.Problems = append(r.Problems, fmt.Sprintf("pix_fmt %q, want %q", dstPixFmt, exp.PixelFormat))
	}

	if dstSizeBytes < exp.MinOutputBytes {
		r.Problems = append(r.Problems, fmt.Sprintf("output size %d bytes below minimum %d", dstSizeBytes, exp.MinOutputBytes))
	}

	r.OK = len(r.Problems) == 0
	return r
}
