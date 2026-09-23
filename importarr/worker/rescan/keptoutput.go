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

package rescan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// CodeUnconfirmedTranscodeOutput means a file is named as squasharr names a
// replaceSource=false output, beside the recorded file it would have been
// made from, and its own CLUSTARR_PROFILE could not be read to settle
// whether squasharr wrote it. It is left out rather than recorded as a
// second file for the item.
const CodeUnconfirmedTranscodeOutput = "unconfirmed_transcode_output"

// keptOutputSeparator is the " - <label>" multiple-version separator
// squasharr/worker.OutputPath puts between a kept source's stem and the
// profile name (docs/research/naming.md §A3).
const keptOutputSeparator = " - "

// keptOutputContainers are the extensions squasharr writes an output with:
// TranscodeProfile's container enum, lower-cased, as OutputPath renders it.
var keptOutputContainers = map[string]bool{".mkv": true, ".mp4": true}

// ProbeTranscodeProfile reads a file's CLUSTARR_PROFILE container tag,
// "" when it carries none.
type ProbeTranscodeProfile func(ctx context.Context, path string) (string, error)

// probeTranscodeProfile is the production [ProbeTranscodeProfile]: the tag
// pkg/mediainfo's probe records as MediaInfo.TranscodeProfile, the same
// reading catalogarr's probe gives the file.
func probeTranscodeProfile(ctx context.Context, path string) (string, error) {
	mi, _, err := mediainfo.Probe(ctx, path)
	if err != nil {
		return "", err
	}
	return mi.TranscodeProfile, nil
}

// keptOutputName splits base as squasharr/worker.OutputPath names a
// replaceSource=false output, "<stem> - <profile>.<container>", into the
// kept source's stem and the profile; ok is false for any other name. A
// profile is a TranscodeProfile name, a DNS-1123 subdomain with no space in
// it, so the LAST separator splits, and a source whose own name holds one
// ("Heat (1995) - Bluray-1080p") keeps it in the stem. The same test
// rejects a person's own " - Director's Cut" version outright.
func keptOutputName(base string) (stem, profile string, ok bool) {
	ext := filepath.Ext(base)
	if !keptOutputContainers[ext] {
		return "", "", false
	}
	name := strings.TrimSuffix(base, ext)
	i := strings.LastIndex(name, keptOutputSeparator)
	if i <= 0 {
		return "", "", false
	}
	stem, profile = name[:i], name[i+len(keptOutputSeparator):]
	if len(validation.IsDNS1123Subdomain(profile)) > 0 {
		return "", "", false
	}
	return stem, profile, true
}

// keptOutput decides a media file that no Succeeded TranscodeJob names but
// that may still be squasharr's replaceSource=false output: its job can be
// gone (deleted by hand, or garbage-collected with its owner) while the
// derived copy stays beside the kept source for good. Adopting it would
// record a second file for the item, which catalogarr deliberately never
// does (its swapTarget: the copy is a version a media server shows, not the
// catalog's file).
//
// The decision, recorded in the gap-fix Z-wave: a file is such an output
// when all three hold --
//
//   - it is named as OutputPath names one (keptOutputName), in a movie or
//     series root folder (only video is transcoded);
//   - a MediaFile records a file beside it with the same stem, the kept
//     source (recordedSourceBeside) -- without one there is no source to be
//     a version of, and the file is the item's own, adopted as usual;
//   - squasharr's record says it wrote that profile's output: the kept
//     source's status.transcode.profileTag names the profile (catalogarr
//     sets it when it incorporates a kept job), or, when that record has
//     moved on (a later transcode of the source by another profile), the
//     file's own CLUSTARR_PROFILE tag names it. The probe runs only then,
//     so an ordinary rescan of a library of kept copies probes nothing.
//
// A recognised output is skipped and counted in Progress.TranscodeOutputs,
// as one a TranscodeJob names is. A file that passes the first two and
// cannot be probed is reported unmatched ([CodeUnconfirmedTranscodeOutput])
// rather than guessed at either way. An output given an explicit
// spec.outputPath follows no naming convention, so it is recognised only
// while its TranscodeJob exists.
func (w *Worker) keptOutput(ctx context.Context, st *scanState, path string) (skip bool, err error) {
	if k := st.root.Spec.Kind; k != catalogv1alpha1.RootFolderKindMovie && k != catalogv1alpha1.RootFolderKindSeries {
		return false, nil
	}
	stem, profile, ok := keptOutputName(filepath.Base(path))
	if !ok {
		return false, nil
	}
	source, err := w.recordedSourceBeside(ctx, st.scan.Namespace, path, stem)
	if err != nil || source == nil {
		return false, err
	}
	rel := relPath(st.root.Spec.Path, path)
	confirmed := source.Status.Transcode != nil && strings.HasPrefix(source.Status.Transcode.ProfileTag, profile+"@")
	if !confirmed && w.ProbeTranscodeProfile != nil {
		tag, perr := w.ProbeTranscodeProfile(ctx, path)
		if perr != nil {
			st.progress.FilesSeen++
			st.unmatched(rel, CodeUnconfirmedTranscodeOutput, fmt.Sprintf(
				"named as squasharr names profile %s's output beside the kept source %s, but its CLUSTARR_PROFILE "+
					"tag could not be read to confirm it (%v); left out rather than recorded as a second file for "+
					"that source's item", profile, relPath(st.root.Spec.Path, source.Spec.Path), perr), nil, w.now())
			return true, nil
		}
		confirmed = strings.HasPrefix(tag, profile+"@")
	}
	if !confirmed {
		return false, nil
	}
	st.progress.FilesSeen++
	st.progress.TranscodeOutputs++
	st.progress.FilesSkipped++
	logging.FromContext(ctx).Debug("left a kept source's transcode output alone",
		"path", rel, "source", source.Name, "profile", profile)
	return true, nil
}

// recordedSourceBeside is the MediaFile recording a file in path's folder
// whose name is stem plus any extension -- the source a kept output was
// made from -- or nil. The source's extension is not the output's (a
// container change keeps the source too, under replaceSource=false), so the
// folder is listed rather than one name guessed.
func (w *Worker) recordedSourceBeside(ctx context.Context, namespace, path, stem string) (*catalogv1alpha1.MediaFile, error) {
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("rescan: list %s for a kept transcode's source: %w", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == filepath.Base(path) || strings.TrimSuffix(name, filepath.Ext(name)) != stem {
			continue
		}
		mf, err := w.existingMediaFile(ctx, namespace, filepath.Join(dir, name))
		if err != nil || mf != nil {
			return mf, err
		}
	}
	return nil, nil
}
