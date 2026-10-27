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

// Package pgsocr implements subtitles.Provider over the image subtitles
// beside a media file: the .sup sidecars the MP4 standard writes for a PGS
// track no text track covers (standard.Version 8), which no player reads.
// Search lists the sidecars the planner found; Download reads one and hands
// it to the process's ReadFunc, which returns it as SubRip. The package runs
// no model itself: the manager links it for the planner's and the
// provider set's sake, and only the caption agent sets a reader
// (pkg/subtitles/ocr, PaddleOCR on ONNX Runtime through purego), so
// cmd/manager never links a dynamic loader.
package pgsocr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// Name is the provider's Name(), and spec.type's value for it.
const Name = "ocr"

// MaxSupBytes caps a .sup read: a two-hour film's PGS track with every cue
// runs tens of MiB (The Sopranos' pilot, 18 MB for 889 bitmaps).
const MaxSupBytes = 256 << 20

// ReadFunc reads a PGS stream -- a .sup file's bytes -- as SubRip text.
type ReadFunc func(ctx context.Context, sup []byte) ([]byte, error)

var (
	// ErrNoReader is Download's error in a process that set no Config.Read.
	ErrNoReader = errors.New("subtitles: ocr: no reader in this process")
	// ErrTooLarge is a .sup over MaxSupBytes.
	ErrTooLarge = errors.New("subtitles: ocr: the .sup is over 256 MiB")
	// ErrNotASidecar is a Download of a candidate that names no image
	// sidecar beside the file.
	ErrNotASidecar = errors.New("subtitles: ocr: not an image sidecar beside the file")
)

// Sidecar is one image subtitle beside the media file: its file name and
// the language key its name carries (a bare <stem>.sup's is the item's
// original language, the planner's attribution).
type Sidecar struct {
	Name    string
	LangKey subtitles.LangKey
}

// Config configures a Provider.
type Config struct {
	// Path is the media file's path in this process's filesystem; the
	// sidecars are read from its directory.
	Path string
	// Sidecars is the image sidecars the planner found beside the file.
	Sidecars []Sidecar
	// Read turns a .sup's bytes into SubRip. Nil: Download returns
	// ErrNoReader.
	Read ReadFunc
}

// Provider implements subtitles.Provider over a file's own image sidecars.
type Provider struct{ cfg Config }

// New builds a Provider from cfg.
func New(cfg Config) *Provider { return &Provider{cfg: cfg} }

func (p *Provider) Name() string       { return Name }
func (p *Provider) HIVerifiable() bool { return true } // the sidecar's name says, from the track's disposition

// Capabilities reports HashVerifiable false for the reason the embedded
// provider does: the candidate IS a subtitle of the file being searched
// for, so the "hash" match it sets has nothing to corroborate, and claiming
// otherwise would score every sidecar at 0 or 1.
func (p *Provider) Capabilities() subtitles.Capabilities {
	return subtitles.Capabilities{Movies: true, Episodes: true, ForcedSearch: true, HashVerifiable: false}
}

// Search returns one Candidate per image sidecar whose language key
// parses, with the key's language, forced and hearing-impaired flags; the
// caller's language filter chooses among them.
func (p *Provider) Search(context.Context, subtitles.Query) ([]subtitles.Candidate, error) {
	var out []subtitles.Candidate
	for _, s := range p.cfg.Sidecars {
		lang, forced, hi, err := subtitles.ParseLangKey(s.LangKey)
		if err != nil || !subtitles.ImageSidecar(s.Name) {
			continue
		}
		out = append(out, subtitles.Candidate{
			Provider: Name, FetchID: s.Name,
			Language: lang, HI: hi, Forced: forced,
			Matches: map[string]bool{subtitles.MatchHash: true, subtitles.MatchHearingImpaired: true},
		})
	}
	return out, nil
}

// Download reads the sidecar c.FetchID names beside the file and returns it
// as SubRip through Config.Read, named <stem>.srt. A FetchID that is not
// one of the configured sidecars' names is refused: the candidate came
// from Search, and nothing else is read from the directory.
func (p *Provider) Download(ctx context.Context, c subtitles.Candidate) ([]byte, string, error) {
	ctx, span := tracing.Start(ctx, "subtitles.ocr.download")
	defer span.End()

	name := c.FetchID
	known := false
	for _, s := range p.cfg.Sidecars {
		known = known || s.Name == name
	}
	if !known || name != filepath.Base(name) || !subtitles.ImageSidecar(name) {
		return nil, "", fmt.Errorf("%w: %q", ErrNotASidecar, name)
	}
	if p.cfg.Read == nil {
		tracing.RecordError(span, ErrNoReader)
		return nil, "", ErrNoReader
	}
	sup, err := readCapped(filepath.Join(filepath.Dir(p.cfg.Path), name))
	if err != nil {
		wrapped := fmt.Errorf("subtitles: ocr: read %s: %w", name, err)
		tracing.RecordError(span, wrapped)
		return nil, "", wrapped
	}
	srt, err := p.cfg.Read(ctx, sup)
	if err != nil {
		wrapped := fmt.Errorf("subtitles: ocr: read %s: %w", name, err)
		tracing.RecordError(span, wrapped)
		return nil, "", wrapped
	}
	return srt, strings.TrimSuffix(name, filepath.Ext(name)) + ".srt", nil
}

// readCapped reads path up to MaxSupBytes, ErrTooLarge past it.
func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, MaxSupBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxSupBytes {
		return nil, ErrTooLarge
	}
	return b, nil
}
