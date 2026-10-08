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

// Package goldens reads the media goldens W6.23 recorded once from the exec
// implementations before their deletion -- ffprobeexec's probe, execextract's
// `ffmpeg -c:s srt` and pkg/segments/decode's ffmpeg(1) decoder -- on the
// committed clips under test/data/media-goldens (upgrade guide U2.3). The
// parity tests compare the in-process implementations with them. Imported
// only by tests.
package goldens

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// Dir is the goldens' directory, module-relative.
const Dir = "test/data/media-goldens"

// The kinds of golden a clip can carry.
const (
	KindProbe   = "probe"
	KindExtract = "extract"
	KindDecode  = "decode"
)

// Entry is one MANIFEST.json entry: a clip, the kinds of golden recorded
// for it, its sha256, and the testmedia.Spec it was made from (Go syntax,
// for the record).
type Entry struct {
	Name   string   `json:"name"`
	Path   string   `json:"path"` // module-relative
	Kinds  []string `json:"kinds"`
	SHA256 string   `json:"sha256"`
	Spec   string   `json:"spec,omitempty"`
}

// ProbeGolden is probe/<name>.json: ffprobeexec.Prober's Probe and
// ProbeAudio on the clip.
type ProbeGolden struct {
	MediaInfo *commonv1.MediaInfo `json:"mediaInfo"`
	// Raw is the whole mediainfo.Raw but FrameErr, which is FrameError.
	Raw        *mediainfo.Raw        `json:"raw"`
	FrameError string                `json:"frameError,omitempty"`
	ProbeError string                `json:"probeError,omitempty"`
	Audio      *mediainfo.AudioProbe `json:"audio"`
	AudioError string                `json:"audioError,omitempty"`
}

// DecodeGolden is decode/<name>.json: pkg/segments/decode's exec decoder
// over W7.6's windows.
type DecodeGolden struct {
	Audio  []AudioWindow  `json:"audio"`
	Frames []FramesWindow `json:"frames"`
	Frame  []FrameAt      `json:"frame"`
}

// AudioWindow is Decoder.Audio(path, Stream, From, Dur): Samples mono
// int16 samples at decode.SampleRate whose little-endian bytes hash to
// SHA256, and their chromaprint.Fingerprint.
type AudioWindow struct {
	Stream      int      `json:"stream"`
	From        float64  `json:"from"`
	Dur         float64  `json:"dur"`
	Samples     int      `json:"samples"`
	SHA256      string   `json:"sha256"`
	Chromaprint []uint32 `json:"chromaprint"`
	Error       string   `json:"error,omitempty"`
}

// FramesWindow is Decoder.Frames(path, From): Count gray frames, each
// hashed.
type FramesWindow struct {
	From   float64  `json:"from"`
	Count  int      `json:"count"`
	SHA256 []string `json:"sha256"`
	Error  string   `json:"error,omitempty"`
}

// FrameAt is Decoder.Frame(path, At): one RGB frame, hashed.
type FrameAt struct {
	At     float64 `json:"at"`
	SHA256 string  `json:"sha256"`
	Error  string  `json:"error,omitempty"`
}

// Root is the module root, found from this file.
func Root() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("goldens: no caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// Manifest is MANIFEST.json.
func Manifest(t testing.TB) []Entry {
	t.Helper()
	var m []Entry
	readJSON(t, filepath.Join(Root(), Dir, "MANIFEST.json"), &m)
	return m
}

// ClipPath is the absolute path of the clip named name.
func ClipPath(name string) string {
	return filepath.Join(Root(), entryPath(name))
}

// entryPath is name's module-relative path: the MANIFEST's, else the
// clips directory's.
func entryPath(name string) string {
	if b, err := os.ReadFile(filepath.Join(Root(), Dir, "MANIFEST.json")); err == nil {
		var m []Entry
		if json.Unmarshal(b, &m) == nil {
			for _, e := range m {
				if e.Name == name {
					return e.Path
				}
			}
		}
	}
	return filepath.Join(Dir, "clips", name)
}

// GoldenPath is the absolute path of name's golden of kind: probe/<name>.json,
// decode/<name>.json, and for extract the glob extract/<name>.s*.srt (one
// file per text subtitle stream).
func GoldenPath(kind, name string) string {
	base := filepath.Join(Root(), Dir, kind)
	if kind == KindExtract {
		return filepath.Join(base, globEscape(name)+".s*.srt")
	}
	return filepath.Join(base, name+".json")
}

// Clips maps the name of every committed clip that has goldens of kind to
// its path, after checking its bytes against the MANIFEST.
func Clips(t testing.TB, kind string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, e := range Manifest(t) {
		has := false
		for _, k := range e.Kinds {
			has = has || k == kind
		}
		if !has {
			continue
		}
		path := filepath.Join(Root(), e.Path)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("goldens: %s: %v", e.Name, err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != e.SHA256 {
			t.Fatalf("goldens: %s changed after its goldens were recorded (sha256 %s, recorded %s)", e.Name, got, e.SHA256)
		}
		out[e.Name] = path
	}
	return out
}

// Probe is name's probe golden.
func Probe(t testing.TB, name string) ProbeGolden {
	t.Helper()
	var g ProbeGolden
	readJSON(t, GoldenPath(KindProbe, name), &g)
	return g
}

// Extract is the SRT execextract wrote for name's stream.
func Extract(t testing.TB, name string, stream int) []byte {
	t.Helper()
	p := filepath.Join(Root(), Dir, KindExtract, fmt.Sprintf("%s.s%d.srt", name, stream))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("goldens: extract golden for %s stream %d: %v", name, stream, err)
	}
	return b
}

// ExtractStreams are the stream indexes name has extraction goldens for.
func ExtractStreams(t testing.TB, name string) []int {
	t.Helper()
	m, err := filepath.Glob(GoldenPath(KindExtract, name))
	if err != nil {
		t.Fatalf("goldens: %v", err)
	}
	var out []int
	for _, p := range m {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), name+".s"), ".srt"), "%d", &n); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// Decode is name's decode golden.
func Decode(t testing.TB, name string) DecodeGolden {
	t.Helper()
	var g DecodeGolden
	readJSON(t, GoldenPath(KindDecode, name), &g)
	return g
}

func readJSON(t testing.TB, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("goldens: %v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("goldens: %s: %v", path, err)
	}
}

func globEscape(s string) string {
	r := strings.NewReplacer(`*`, `\*`, `?`, `\?`, `[`, `\[`, `\`, `\\`)
	return r.Replace(s)
}
