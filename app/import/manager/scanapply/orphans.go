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

package scanapply

import (
	"context"
	"crypto/sha1" //nolint:gosec // a dedup key, not a security boundary
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/version"
)

// OrphanPartAge is how long a transcode attempt's part file must have gone
// unwritten before it is recycled as abandoned: the worker's default
// per-task deadline (48h) plus a day, the rescan's own floor
// (rescan.OrphanPartAge). A running encode writes its part continuously, so
// one this old belongs to an attempt that died without its cleanup.
const OrphanPartAge = 72 * time.Hour

// orphanPart decides a transcode attempt's part file the scan reported
// (P47): it is recycled through a recycle-files task on import-recycle when
// it is of exactly the per-dispatch form or that form's copy still in
// flight (fsops.ParseTranscodePartCopy), unwritten for OrphanPartAge, no
// MediaFile whose UID starts with its eight characters has a transcode in
// flight, and the MediaFile it was made from (TranscodeOutputOf) has none
// either (status.transcode Queued, Running or Swapping). The import
// domain moves it into the root folder's recycle bin.
func (a *Applier) orphanPart(ctx context.Context, obs schema.ScanObservation, _ *tally) error {
	p, ok := fsops.ParseTranscodePartCopy(obs.Path)
	if !ok || a.now().Sub(obs.ModTime) <= OrphanPartAge {
		return nil
	}
	ns := obs.Scan.Namespace
	live, err := a.liveTranscode(ctx, ns, p.FileUID8)
	if err != nil || live {
		return err
	}
	if src := obs.TranscodeOutputOf; src != "" {
		var mf catalogv1alpha1.MediaFile
		switch err := a.reader().Get(ctx, types.NamespacedName{Namespace: ns, Name: src}, &mf); {
		case err == nil:
			if transcodeInFlight(&mf) {
				return nil
			}
		case !apierrors.IsNotFound(err):
			return fmt.Errorf("scanapply: get media file %s: %w", src, err)
		}
	}
	task := schema.RecycleFilesTask{
		RootFolder: schema.Ref{Namespace: ns, Name: obs.RootFolder.Name, UID: obs.RootFolder.UID},
		Paths:      []string{obs.Path},
	}
	name, data, err := schema.Encode(task)
	if err != nil {
		return err
	}
	id := shortSHA1(obs.Path)
	env := &events.Envelope{
		ID:     "recycle/" + id + "/" + strconv.FormatInt(obs.ModTime.Unix(), 10),
		Type:   "import.RecycleFilesTask",
		Schema: name,
		Source: "manager@" + version.String(),
		Key:    ns + "/" + obs.RootFolder.Name,
		Time:   a.now(),
		Data:   data,
	}
	if _, err := a.Bus.Publish(ctx, events.SubjectImportRecycleFiles(id), env); err != nil {
		return fmt.Errorf("scanapply: ask for %s to be recycled: %w", obs.Path, err)
	}
	logging.FromContext(ctx).Info("scanapply: an abandoned transcode part file is to be recycled",
		"path", obs.Path, "modified", obs.ModTime, "minAge", OrphanPartAge)
	return nil
}

// liveTranscode reports whether a MediaFile in ns whose UID starts with
// uid8 has a transcode in flight: its dispatch's worker sweeps its own
// parts.
func (a *Applier) liveTranscode(ctx context.Context, ns, uid8 string) (bool, error) {
	var files catalogv1alpha1.MediaFileList
	if err := a.reader().List(ctx, &files, client.InNamespace(ns), client.UnsafeDisableDeepCopy); err != nil {
		return false, fmt.Errorf("scanapply: list media files in %s: %w", ns, err)
	}
	for i := range files.Items {
		if mf := &files.Items[i]; strings.HasPrefix(string(mf.UID), uid8) && transcodeInFlight(mf) {
			return true, nil
		}
	}
	return false, nil
}

// transcodeInFlight reports whether mf's transcode block has a dispatch in
// flight: a part beside it may be that attempt's.
func transcodeInFlight(mf *catalogv1alpha1.MediaFile) bool {
	tr := mf.Status.Transcode
	if tr == nil {
		return false
	}
	switch tr.Phase {
	case catalogv1alpha1.TranscodePhaseQueued, catalogv1alpha1.TranscodePhaseRunning, catalogv1alpha1.TranscodePhaseSwapping:
		return true
	}
	return false
}

// shortSHA1 is the first 16 hex digits of s's SHA-1.
func shortSHA1(s string) string {
	sum := sha1.Sum([]byte(s)) //nolint:gosec // a dedup key
	return hex.EncodeToString(sum[:])[:16]
}
