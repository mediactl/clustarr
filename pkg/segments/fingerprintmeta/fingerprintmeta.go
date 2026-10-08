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

// Package fingerprintmeta is the metadata of the clustarr-fingerprints
// object store (ADR-0021): the one builder every Put and the maintenance
// audit's SetMeta send, the check a reader makes before it trusts a cached
// fingerprint, and the audit itself. It is a leaf both sides import -- the
// markers binary's segment cache, which writes and reads fingerprints, and
// the manager's maintenance loop, which backfills them -- so it links no
// Chromaprint: the manager never analyses audio (R3), and pkg/segments, which
// the manager and the agent both link, stays free of the analysers.
package fingerprintmeta

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/segments"
)

// SampleRate is the rate every fingerprint's audio is decoded at:
// Chromaprint's, pkg/segments/chromaprint.SampleRate, restated so this
// package links no analyser; TestSampleRateIsChromaprints pins the two.
const SampleRate = 11025

// Channels is the channel count every fingerprint's audio is decoded to:
// mono (pkg/segments/decode).
const Channels = 1

// windowSecondsTolerance is how far a stored window length may sit from the
// one a reader computes and still match: the stored value is rounded to the
// millisecond.
const windowSecondsTolerance = 0.001

// Build is the one builder of a fingerprint object's metadata (ADR-0021
// §2): every Put to events.ObjectStoreFingerprints sends it, and the
// maintenance audit's SetMeta sends Backfill's, so neither ever sends a
// partial map (both replace the whole meta). which is "start" or "end",
// windowSeconds the window's length as decoded, and writtenBy the writing
// binary and its version ("markers@<version>").
func Build(probeHash, which string, windowSeconds float64, writtenBy string) events.ObjectMeta {
	return buildAt(segments.FingerprintVersion, probeHash, which, windowSeconds, writtenBy)
}

// buildAt is Build for a fingerprint computed under version v. A
// windowSeconds of zero or less is unknown and left out: a backfill cannot
// know it from the name.
func buildAt(v int32, probeHash, which string, windowSeconds float64, writtenBy string) events.ObjectMeta {
	md := map[string]string{
		events.ObjectMetaKeyVersion:         events.FingerprintMetaVersion,
		events.ObjectMetaKeyKind:            events.FingerprintMetaKind,
		events.FingerprintMetaKeyProbeHash:  probeHash,
		events.FingerprintMetaKeyWindow:     which,
		events.FingerprintMetaKeyVersion:    strconv.Itoa(int(v)),
		events.FingerprintMetaKeySampleRate: strconv.Itoa(SampleRate),
		events.FingerprintMetaKeyChannels:   strconv.Itoa(Channels),
	}
	if windowSeconds > 0 {
		md[events.FingerprintMetaKeyWindowSeconds] = strconv.FormatFloat(windowSeconds, 'f', 3, 64)
	}
	if writtenBy != "" {
		md[events.ObjectMetaKeyWrittenBy] = writtenBy
	}
	return events.ObjectMeta{Metadata: md}
}

// Mismatch says why a cached fingerprint's metadata does not match what
// the reader would compute now, "" when it does (ADR-0021 §4):
// the fingerprint version, the sample rate and channels, and the window's
// length when both sides know it. An object with no meta-version was
// written before the metadata existed; its name carries its version, and
// it is accepted until the maintenance audit backfills it or it expires.
// This guards a segments.FingerprintVersion change made without the key
// suffix.
func Mismatch(info events.ObjectInfo, windowSeconds float64) string {
	md := info.Metadata
	if md[events.ObjectMetaKeyVersion] == "" {
		return ""
	}
	if got, want := md[events.FingerprintMetaKeyVersion], strconv.Itoa(int(segments.FingerprintVersion)); got != want {
		return fmt.Sprintf("fingerprint-version %q, want %q", got, want)
	}
	if got, want := md[events.FingerprintMetaKeySampleRate], strconv.Itoa(SampleRate); got != want {
		return fmt.Sprintf("sample-rate %q, want %q", got, want)
	}
	if got, want := md[events.FingerprintMetaKeyChannels], strconv.Itoa(Channels); got != want {
		return fmt.Sprintf("channels %q, want %q", got, want)
	}
	if raw := md[events.FingerprintMetaKeyWindowSeconds]; raw != "" && windowSeconds > 0 {
		got, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.Abs(got-windowSeconds) > windowSecondsTolerance {
			return fmt.Sprintf("window-seconds %q, want %.3f", raw, windowSeconds)
		}
	}
	return ""
}

// ParseKey reads a segments.FingerprintKey back: its probe hash, its
// window ("start" or "end") and the FingerprintVersion it names (1 for the
// legacy unversioned key). ok is false for a name of any other shape.
func ParseKey(name string) (probeHash, which string, version int32, ok bool) {
	parts := strings.Split(name, ".")
	switch len(parts) {
	case 2:
		version = 1
	case 3:
		v, err := strconv.Atoi(strings.TrimPrefix(parts[2], "v"))
		if !strings.HasPrefix(parts[2], "v") || err != nil || v < 2 {
			return "", "", 0, false
		}
		version = int32(v)
	default:
		return "", "", 0, false
	}
	probeHash, which = parts[0], parts[1]
	if probeHash == "" || (which != "start" && which != "end") {
		return "", "", 0, false
	}
	return probeHash, which, version, true
}

// Backfill is the complete metadata the maintenance audit sets on
// a fingerprint written before its metadata existed (ADR-0021 §3): what its
// name says -- probe hash, window, version -- and the analyzer's fixed
// parameters; the window's length is unknown and left out. ok is false for a
// name that is not a fingerprint key.
func Backfill(name, writtenBy string) (events.ObjectMeta, bool) {
	probeHash, which, v, ok := ParseKey(name)
	if !ok {
		var none events.ObjectMeta
		return none, false
	}
	return buildAt(v, probeHash, which, 0, writtenBy), true
}

// BackfillStore is the fingerprint store's maintenance audit
// (ADR-0021 §3, run by the manager's maintenance loop): every object with no
// meta-version gets Backfill's complete metadata through SetMeta,
// never a re-upload of its chunks. A name that is not a fingerprint key is
// left alone; an object deleted meanwhile is skipped. It returns how many
// objects it backfilled.
func BackfillStore(ctx context.Context, store events.ObjectStore, writtenBy string) (int, error) {
	objects, err := store.List(ctx, "")
	if err != nil {
		return 0, fmt.Errorf("fingerprintmeta: list fingerprints: %w", err)
	}
	var (
		n    int
		errs []error
	)
	for _, o := range objects {
		if o.Metadata[events.ObjectMetaKeyVersion] != "" {
			continue
		}
		meta, ok := Backfill(o.Name, writtenBy)
		if !ok {
			continue
		}
		if err := store.SetMeta(ctx, o.Name, meta); err != nil {
			if errors.Is(err, events.ErrObjectNotFound) {
				continue
			}
			errs = append(errs, fmt.Errorf("fingerprintmeta: backfill %s: %w", o.Name, err))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}
