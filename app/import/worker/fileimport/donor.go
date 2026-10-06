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
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=audiografts,verbs=get;list;watch;create;patch;update

// DonorDir is the folder under a RootFolder that holds audio donors, one
// folder per item UID (anime dual-audio spec §6.2). Plex's scanner skips a
// dot folder, and so does every walk here (fsops.Classifier.Walk).
const DonorDir = fsops.ClustarrDir + "/donors"

// donorExts are the files a donor download can hold its audio in.
var donorExts = map[string]bool{".mkv": true, ".mka": true, ".mp4": true, ".m4v": true, ".avi": true, ".ts": true}

// donorItem is what a donor import needs of its Episode or Movie.
type donorItem struct {
	obj        client.Object
	audio      *catalogv1alpha1.AudioState
	original   string
	rootFolder string
	profileRef string
}

// importDonor imports an audio donor (Download spec.purpose audioDonor):
// no MediaFile, no library name. The download's file is checked to carry
// the item's missing languages and its original, placed under the item's
// donor folder, and named in the item's AudioGraft, which squasharr grafts
// from. A file that lacks them is the release's fault, reported as every
// file rejected, so grabarr blocklists it and the donor is searched for
// again.
func (w *Worker) importDonor(ctx context.Context, m events.Message, dl *downloadv1alpha1.Download, ref commonv1.MediaRef) error {
	log := logging.FromContext(ctx).With("donorFor", ref.Name)
	item, err := w.donorItem(ctx, dl.Namespace, ref)
	if err != nil {
		return err
	}
	if item == nil {
		return w.finishBlocked(ctx, dl, nil, nil, fmt.Sprintf("a donor is for an episode or a movie; %s %q is gone or neither", ref.Kind, ref.Name))
	}
	if item.audio == nil || len(item.audio.Missing) == 0 {
		return w.finishBlocked(ctx, dl, nil, nil, fmt.Sprintf("%s %q no longer lacks a language a donor could graft", ref.Kind, ref.Name))
	}
	anchor, ok := lang.Normalize(item.original)
	if !ok {
		return w.finishBlocked(ctx, dl, nil, nil, fmt.Sprintf("%s %q has no known original language to align a donor on", ref.Kind, ref.Name))
	}
	root, err := w.getRoot(ctx, dl.Namespace, item.rootFolder)
	if err != nil {
		return err
	}
	if dl.Status.ContentRoot == "" {
		return fmt.Errorf("fileimport: download %s/%s has no status.contentRoot yet", dl.Namespace, dl.Name)
	}
	src, info, err := donorFile(dl.Status.ContentRoot)
	if err != nil {
		if w.finalAttempt(m) {
			return w.finishBlocked(ctx, dl, nil, nil, fmt.Sprintf("content root %q: %v", dl.Status.ContentRoot, err))
		}
		return fmt.Errorf("fileimport: find the donor's file under %s: %w", dl.Status.ContentRoot, err)
	}
	if src == "" {
		return w.finishBlocked(ctx, dl, nil, []string{"no video or audio file in the download"}, downloadv1alpha1.ImportMessageEveryFileRejected)
	}
	rel, _ := filepath.Rel(dl.Status.ContentRoot, src)

	mi, err := probeVideo(ctx, m, src, rel)
	if err != nil {
		return err
	}
	if mi == nil {
		return w.finishBlocked(ctx, dl, nil, []string{rel + ": could not be probed"}, downloadv1alpha1.ImportMessageEveryFileRejected)
	}
	have := map[string]bool{}
	for _, a := range mi.Audio {
		if t, ok := lang.Normalize(a.Language); ok {
			have[baseTag(string(t))] = true
		}
	}
	var lacks []string
	for _, l := range append([]string{string(anchor)}, item.audio.Missing...) {
		if !have[baseTag(l)] {
			lacks = append(lacks, l)
		}
	}
	if len(lacks) > 0 {
		log.Info("fileimport: the donor lacks a language its release promised", "file", rel, "lacks", lacks)
		return w.finishBlocked(ctx, dl, nil, []string{fmt.Sprintf("%s: no tagged %s audio track", rel, strings.Join(lacks, ", "))},
			downloadv1alpha1.ImportMessageEveryFileRejected)
	}

	dest := filepath.Join(root.Spec.Path, DonorDir, string(item.obj.GetUID()), ref.Name+strings.ToLower(filepath.Ext(src)))
	if err := os.MkdirAll(filepath.Dir(dest), 0o775); err != nil {
		return fmt.Errorf("fileimport: create the donor folder: %w", err)
	}
	mode := fsops.ImportHardlink
	if dl.Status.CanMoveFiles {
		mode = fsops.ImportMove
	}
	if err := placeFile(ctx, root.Spec.Path, root.Spec.RecycleBin.Path, src, info, dest, mode); err != nil {
		if errors.Is(err, errBlocked) {
			return w.finishBlocked(ctx, dl, nil, nil, blockedMessage(err))
		}
		return err
	}

	profile, err := w.resolveProfile(ctx, dl.Spec.QualityProfileRef, item.profileRef)
	if err != nil {
		return err
	}
	if err := w.applyAudioGraft(ctx, dl, item, dest, string(anchor), profile.AudioDefault); err != nil {
		return err
	}
	log.Info("fileimport: imported an audio donor", "file", rel, "donor", dest, "languages", item.audio.Missing)
	imported := []*downloadac.ImportedFileApplyConfiguration{downloadac.ImportedFile().WithSourcePath(rel).WithDestPath(dest)}
	return w.finishImported(ctx, dl, imported, nil)
}

func baseTag(t string) string { b, _, _ := strings.Cut(t, "-"); return strings.ToLower(b) }

// donorItem reads the Episode (and its Series) or Movie a donor is for; nil
// when it is gone or of another kind.
func (w *Worker) donorItem(ctx context.Context, ns string, ref commonv1.MediaRef) (*donorItem, error) {
	key := types.NamespacedName{Namespace: ns, Name: ref.Name}
	switch ref.Kind {
	case commonv1.MediaKindEpisode:
		var e catalogv1alpha1.Episode
		if err := w.Client.Get(ctx, key, &e); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		var s catalogv1alpha1.Series
		if err := w.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: e.Spec.SeriesRef}, &s); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		it := &donorItem{obj: &e, audio: e.Status.Audio, rootFolder: s.Spec.RootFolderRef, profileRef: s.Spec.QualityProfileRef}
		if s.Status.Metadata != nil {
			it.original = s.Status.Metadata.OriginalLanguage
		}
		return it, nil
	case commonv1.MediaKindMovie:
		var mv catalogv1alpha1.Movie
		if err := w.Client.Get(ctx, key, &mv); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		it := &donorItem{obj: &mv, audio: mv.Status.Audio, rootFolder: mv.Spec.RootFolderRef, profileRef: mv.Spec.QualityProfileRef}
		if mv.Status.Metadata != nil {
			it.original = mv.Status.Metadata.OriginalLanguage
		}
		return it, nil
	}
	return nil, nil
}

// donorFile is the largest media or Matroska audio file under root that is
// not a sample: a donor release's one episode.
func donorFile(root string) (string, os.FileInfo, error) {
	var best string
	var bestInfo os.FileInfo
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !donorExts[strings.ToLower(filepath.Ext(p))] || strings.Contains(strings.ToLower(d.Name()), "sample") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if bestInfo == nil || info.Size() > bestInfo.Size() {
			best, bestInfo = p, info
		}
		return nil
	})
	return best, bestInfo, err
}

// applyAudioGraft names the donor in the item's AudioGraft, owned by the
// item so it goes with it. A newer donor replaces the spec, which squasharr
// reads as a new generation; the old donor's files are removed.
func (w *Worker) applyAudioGraft(ctx context.Context, dl *downloadv1alpha1.Download, item *donorItem, donorPath, anchor, def string) error {
	name := k8s.AudioGraftName(item.obj.GetName())
	var old transcodev1alpha1.AudioGraft
	switch err := w.Client.Get(ctx, types.NamespacedName{Namespace: dl.Namespace, Name: name}, &old); {
	case err == nil:
		if old.Spec.DonorPath != donorPath {
			removeDonor(ctx, old.Spec.DonorPath, donorPath)
		}
	case client.IgnoreNotFound(err) != nil:
		return fmt.Errorf("fileimport: get AudioGraft %s: %w", name, err)
	}
	owner, err := k8s.OwnerReferenceAC(item.obj, w.Client.Scheme())
	if err != nil {
		return err
	}
	spec := transcodeac.AudioGraftSpec().
		WithItemRef(commonv1.MediaRef{Kind: kindOf(item.obj), Name: item.obj.GetName()}).
		WithDonorPath(donorPath).WithLanguages(item.audio.Missing...).WithAnchor(anchor).
		WithRelease(dl.Spec.Release.Title)
	if def != "" && def != "original" {
		spec = spec.WithDefault(def)
	}
	_, err = k8s.Apply(ctx, w.Client, FieldManager, transcodeac.AudioGraft(name, dl.Namespace).WithOwnerReferences(owner).WithSpec(spec))
	if err != nil {
		return fmt.Errorf("fileimport: apply AudioGraft %s: %w", name, err)
	}
	return nil
}

func kindOf(o client.Object) commonv1.MediaKind {
	if _, ok := o.(*catalogv1alpha1.Episode); ok {
		return commonv1.MediaKindEpisode
	}
	return commonv1.MediaKindMovie
}

// removeDonor removes a donor file and the .mka a graft reduced it to,
// never keep -- the donor just placed, which can be that very .mka.
func removeDonor(ctx context.Context, p, keep string) {
	for _, f := range []string{p, strings.TrimSuffix(p, filepath.Ext(p)) + ".mka"} {
		if f == keep {
			continue
		}
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logging.FromContext(ctx).Warn("fileimport: could not remove a replaced donor", "path", f, "error", err)
		}
	}
}
