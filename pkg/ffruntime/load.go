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

package ffruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/obinnaokechukwu/ffgo"
)

const (
	// AVCodecMajor is FFmpeg 9's libavcodec major.
	AVCodecMajor = 63
	// MinShimAPI is FFSHIM_API_VERSION of ffgo v0.0.0-clustarr.13: an older
	// shim reads FFmpeg 9's structs at FFmpeg 4-7's offsets (spec §7.1 fact 8).
	MinShimAPI = 1
	// Grace is how long Do waits for a call after its context ends: the old
	// subprocess WaitDelay.
	Grace = 5 * time.Second
	// MaxAbandoned outstanding abandoned calls wedge the process.
	MaxAbandoned = 4
)

// Report is what Load found; the library paths are the files the dynamic
// loader mapped (/proc/self/maps), not the names asked for.
type Report struct {
	FFmpegMajor int    `json:"ffmpegMajor"`
	ShimAPI     int    `json:"shimAPI"`
	AVUtil      string `json:"avutil,omitempty"`
	AVCodec     string `json:"avcodec,omitempty"`
	AVFormat    string `json:"avformat,omitempty"`
	ShimPath    string `json:"shimPath,omitempty"`
}

var (
	// ErrUnavailable wraps every reason FFmpeg cannot be used here.
	ErrUnavailable = errors.New("ffruntime: FFmpeg is unavailable")
	// ErrWedged refuses calls once MaxAbandoned are outstanding.
	ErrWedged = errors.New("ffruntime: FFmpeg calls are stuck; restart the process")
	// ErrAbandoned is a call that outlived its context plus Grace.
	ErrAbandoned = errors.New("ffruntime: an FFmpeg call outlived its context and was abandoned")
)

var ffmpegReleases = map[int]int{58: 4, 59: 5, 60: 6, 61: 7, 62: 8, 63: 9}

var (
	loadOnce   sync.Once
	loadReport Report
	loadErr    error
)

// Load loads FFmpeg and the shim once per process and keeps the first
// result: ffgo's own loader remembers a failure and cannot be retried.
func Load() (Report, error) {
	loadOnce.Do(func() { loadReport, loadErr = load(func() ([]byte, error) { return os.ReadFile("/proc/self/maps") }) })
	return loadReport, loadErr
}

func load(maps func() ([]byte, error)) (Report, error) {
	var r Report
	if err := ffgo.Init(); err != nil {
		return r, fmt.Errorf("%w: load the FFmpeg libraries (LD_LIBRARY_PATH): %w", ErrUnavailable, err)
	}
	_, avc, _ := ffgo.Version()
	major := int(avc >> 16)
	r.FFmpegMajor = ffmpegReleases[major]
	if major != AVCodecMajor {
		return r, fmt.Errorf("%w: libavcodec %d is not FFmpeg 9's (%d)", ErrUnavailable, major, AVCodecMajor)
	}
	d := ffgo.Diagnose()
	r.ShimPath = d.ShimPath
	if !d.ShimLoaded {
		return r, fmt.Errorf("%w: no ffgo shim built for FFmpeg 9 was loaded (FFGO_SHIM_DIR): %s", ErrUnavailable, d.ShimError)
	}
	r.ShimAPI = ffgo.ShimAPIVersion()
	if r.ShimAPI < MinShimAPI {
		return r, fmt.Errorf("%w: the ffgo shim %s has API %d; this build needs %d (rebuild libffshim.so from ffgo v0.0.0-clustarr.13)",
			ErrUnavailable, d.ShimPath, r.ShimAPI, MinShimAPI)
	}
	if b, err := maps(); err == nil {
		r.AVUtil, r.AVCodec, r.AVFormat = mapped(b, "libavutil.so"), mapped(b, "libavcodec.so"), mapped(b, "libavformat.so")
		if p := mapped(b, "libffshim.so"); p != "" {
			r.ShimPath = p
		}
	}
	// F13 makes the level effective under a callback: lines above it never
	// reach the trampoline, so a debug line costs no purego callback.
	if err := ffgo.SetLogLevel(ffgo.LogWarning); err != nil {
		return r, fmt.Errorf("%w: set FFmpeg's log level: %w", ErrUnavailable, err)
	}
	return r, nil
}

// mapped is the path of the first mapping whose file name begins with lib,
// so "libavcodec.so" matches libavcodec.so.63.1.100.
func mapped(maps []byte, lib string) string {
	for _, line := range strings.Split(string(maps), "\n") {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		if p := strings.Join(f[5:], " "); strings.HasPrefix(filepath.Base(p), lib) {
			return p
		}
	}
	return ""
}
