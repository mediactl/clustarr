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

package naming

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

// namingRetry is how soon a pass whose render kept its previous proposal
// over a failed lookup runs again (§3.11).
const namingRetry = 30 * time.Second

// Options configures the naming planner.
type Options struct{}

// Adapter is the naming planner.
type Adapter struct{}

// New is the naming planner.
func New(Options) *Adapter { return &Adapter{} }

// Input is what Gather read.
type Input struct {
	Lookup           mediafile.NamingLookup
	TranscodePending bool
	Read             bool // Gather ran
}

// Name implements remediation.Planner.
func (*Adapter) Name() remediation.PlannerName { return remediation.PlannerNaming }

// Applies: catalog names every file whose owner kind has a file preset
// (mediafile's namingOwner): a movie, an episode, an album's track, a book,
// an audiobook's part and an issue (2026-11-10 design D7, §5.5). A file
// attributed to a container kind, or to nothing, is not named.
func (*Adapter) Applies(mf *catalogv1alpha1.MediaFile) bool {
	switch mf.Spec.MediaRef.Kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindEpisode,
		commonv1.MediaKindAlbum, commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindIssue:
		return true
	}
	return false
}

// Gather reads the item and its RootFolder from the cache. The directory
// listing (status.sidecars) is the subtitles planner's since F5.1 (loop
// spec §6.4.3); whether a transcode holds the file is read off the draft in
// Plan (F6.2), since Gather runs before the transcode planner's Plan.
//
// An audiobook part is numbered first (numberedPart): {PartNumber} is the
// file's place among its Audiobook's parts, which only the loop, with the
// Audiobook's files, can count. A failed listing is Gather's error, so the
// pass keeps the proposal the file had rather than render a number from a
// partial list.
func (*Adapter) Gather(ctx context.Context, env *remediation.Env, v *remediation.View) (Input, error) {
	file := v.File
	if file.Spec.MediaRef.Kind == commonv1.MediaKindAudiobook {
		numbered, err := numberedPart(ctx, env.Reader, file)
		if err != nil {
			return Input{}, err
		}
		file = numbered
	}
	return Input{Read: true, Lookup: mediafile.LoadNaming(ctx, env.Reader, file)}, nil
}

// numberedPart is file as mediafile.LoadNaming reads an audiobook part: a
// copy whose spec.partIndex is the part's number among its Audiobook's
// MediaFiles in play order (catalogctx.PartNumbers over the files the item
// index lists: 1..n, 0 for a lone file), which is what {PartNumber} renders
// (2026-11-10 design D7, D11) and what the import numbered the file by
// when it placed it (fileimport's placeParts), so the loop proposes the
// path the import gave. The file's own spec.partIndex is the ordering key
// (disc*1000+track from the tags, else the name's part number), never the
// number: LoadNaming hands catalogctx.Audiobook the field it is given as the
// number, and nothing else on the naming path reads it, so the copy is
// numbered here. A file the cache has yet to list (its own create) counts
// itself.
func numberedPart(ctx context.Context, r client.Reader, file *catalogv1alpha1.MediaFile) (*catalogv1alpha1.MediaFile, error) {
	files, err := mfindex.FilesOf(ctx, r, file.Namespace, commonv1.MediaKindAudiobook, file.Spec.MediaRef.Name, client.UnsafeDisableDeepCopy)
	if err != nil {
		return nil, err
	}
	parts := make([]catalogctx.Part, 0, len(files)+1)
	self := -1
	for i := range files {
		if files[i].Name == file.Name {
			self = len(parts)
		}
		parts = append(parts, catalogctx.Part{Index: files[i].Spec.PartIndex, Path: files[i].Spec.Path})
	}
	if self < 0 {
		self = len(parts)
		parts = append(parts, catalogctx.Part{Index: file.Spec.PartIndex, Path: file.Spec.Path})
	}
	numbered := *file
	numbered.Spec.PartIndex = catalogctx.PartNumbers(parts)[self]
	return &numbered, nil
}

// Plan renders status.naming and NamingCurrent against spec.path as of this
// pass's main apply.
func (*Adapter) Plan(v *remediation.View, in Input, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
	var res remediation.Result
	if !in.Read {
		return res, nil
	}
	// A file the probe planner found missing keeps the proposal it had:
	// nothing is rendered for a file that is not there (the MediaFile
	// controller's FileMissing return), and out already carries it.
	if fileMissing(v.Draft) {
		return res, nil
	}
	// A transcode in flight holds the rename: its worker re-probes the
	// source and swaps its output over it (spec D6).
	if t := v.Draft.Transcode; t != nil {
		switch t.Phase {
		case catalogv1alpha1.TranscodePhaseQueued, catalogv1alpha1.TranscodePhaseRunning, catalogv1alpha1.TranscodePhaseSwapping:
			in.TranscodePending = true
		}
	}
	n, retry := mediafile.RenderNaming(v.File, in.Lookup, v.Draft.MediaInfo, v.Prev.Naming, v.SpecPath(), probeFailed(v.Draft), in.TranscodePending)
	out.Naming = n
	if n == nil {
		k8s.RemoveCondition(&out.Conditions, catalogv1alpha1.ConditionNamingCurrent)
	} else {
		mediafile.MarkNamingCurrent(v.File, &out.Conditions, n)
	}
	if retry {
		res.Due = v.Now.Add(namingRetry)
	}
	return res, nil
}

// probeFailed is W4.13's probeStale for a failed probe of changed bytes: the
// draft's Probed reads ProbeFailed (D-F3-5).
func probeFailed(s *catalogv1alpha1.MediaFileStatus) bool {
	c := k8s.FindCondition(s.Conditions, catalogv1alpha1.MediaFileConditionProbed)
	return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "ProbeFailed"
}

// fileMissing reports the probe planner's FileMissing on the draft: Ready
// False, reason FileMissing.
func fileMissing(s *catalogv1alpha1.MediaFileStatus) bool {
	c := k8s.FindCondition(s.Conditions, catalogv1alpha1.MediaFileConditionReady)
	return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "FileMissing"
}

// Copy is naming's row of §3.5's table.
func (*Adapter) Copy(from, into *catalogv1alpha1.MediaFileStatus) {
	into.Naming = from.Naming.DeepCopy()
	remediation.CopyConditions(from, into, catalogv1alpha1.ConditionNamingCurrent)
}
