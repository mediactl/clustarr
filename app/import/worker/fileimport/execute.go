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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/transcode/donoraudio"
)

// handleExecute carries out one ImportExecuteTask (ADR-0019 §6.9, step 3):
// it checks the plan's basis -- every MediaFile it replaces, re-read
// through the APIReader, must still be the UID and resourceVersion the plan
// was made against -- and that every file it replaces lies under the root
// folder, then the free space, places each file (containment,
// a recycle link for an existing destination, never an overwrite), recycles
// each replaced file, places an audio donor and reduces it to its audio
// (loop spec §4.12), and writes the record at RecordSubKey(entry,
// "execute"). No Kubernetes write: the manager materialises what the record
// says was placed, and the item key records the reduced donor.
//
// It seeds no probe record: the MediaFile, and the UID a probe record is
// keyed by, do not exist until the manager materialises it, so catalog
// probes a placed file once (ruling, A3.8).
func (w *Worker) handleExecute(ctx context.Context, m events.Message, env *events.Envelope) error {
	var task schema.ImportExecuteTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("fileimport: malformed ImportExecuteTask", err)
	}
	if task.Entry.UID == "" || task.Owner.Namespace == "" {
		return events.Discard("fileimport: an execute task names no entry or owner", nil)
	}
	key := agentrecords.ImportKey(task.Entry.UID, schema.ImportSubExecute)
	if cur, _, ok, err := w.reader().Get(ctx, key); err != nil {
		return fmt.Errorf("fileimport: read the execute record: %w", err)
	} else if ok && cur.Seq >= task.Seq {
		return nil
	}
	log := logging.FromContext(ctx).With("namespace", task.Owner.Namespace, "owner", task.Owner.Name, "entry", task.Entry.ID)
	ctx = logging.NewContext(ctx, log)

	x, failure, err := w.execute(ctx, m, &task)
	if err != nil {
		return err
	}
	now := w.now().UTC()
	rec := &schema.ImportRecord{
		RecordHeader: schema.RecordHeader{
			Item: &task.Owner, Sub: schema.ImportSubExecute, Seq: task.Seq, State: records.StateAnswered,
			AnsweredAt: &now, Failure: failure,
		},
		Entry: task.Entry, Execute: x,
	}
	if x == nil {
		rec.State = records.StateFailed
		rec.Transient = true
	}
	if _, err := w.writer().Write(ctx, key, rec); err != nil {
		return fmt.Errorf("fileimport: write the execute record: %w", err)
	}
	if x != nil {
		log.Info("fileimport: executed an import", "placed", len(x.Placed), "recycled", len(x.Recycled), "refused", x.Refused)
	}
	return nil
}

// execute carries out task's plan. A refusal is an execution with Refused
// set and failure saying why; a nil execution with failure is a failure the
// manager retries; err is only one the delivery must retry.
func (w *Worker) execute(ctx context.Context, m events.Message, task *schema.ImportExecuteTask) (*schema.ImportExecution, string, error) {
	plan := task.Plan
	x := &schema.ImportExecution{}
	if why, err := w.stale(ctx, task.Owner.Namespace, plan.Replaces); err != nil {
		return nil, "", err
	} else if why != "" {
		x.Refused = schema.ImportRefusedStale
		return x, why, nil
	}
	if why := outsideRoot(plan); why != "" {
		x.Refused = schema.ImportRefusedOutsideRoot
		return x, why, nil
	}

	var need int64
	for _, mv := range plan.Moves {
		info, err := os.Stat(mv.Source)
		if err != nil {
			return nil, fmt.Sprintf("%s: %v", filepath.Base(mv.Source), err), nil
		}
		need += info.Size()
	}
	if d := plan.Donor; d != nil {
		if info, err := os.Stat(d.Source); err == nil {
			need += info.Size()
		}
	}
	if err := fsops.EnsureFreeSpace(plan.RootFolder, need+plan.MinFreeBytes); err != nil {
		x.Refused = schema.ImportRefusedDiskFull
		return x, err.Error(), nil
	}
	mode := fsops.ImportHardlink
	switch plan.Mode {
	case schema.ImportModeMove:
		mode = fsops.ImportMove
	case schema.ImportModeCopy:
		mode = fsops.ImportCopy
	}

	var last time.Time
	dests := map[string]bool{}
	for _, mv := range plan.Moves {
		if err := w.beat(ctx, m, &last); err != nil {
			return nil, "", err
		}
		if refused, why, err := place(ctx, plan, mv.Source, mv.Dest, mode); err != nil {
			return nil, why, nil
		} else if refused != "" {
			x.Refused = refused
			return x, why, nil
		}
		info, err := os.Stat(mv.Dest)
		if err != nil {
			return nil, fmt.Sprintf("stat imported file %s: %v", mv.Dest, err), nil
		}
		dests[mv.Dest] = true
		x.Placed = append(x.Placed, schema.PlacedFile{
			Source: mv.Source, Dest: mv.Dest, SizeBytes: info.Size(), ModTime: info.ModTime().UTC(),
			Target: mv.Target, Keys: mv.Keys, MediaFileName: mv.MediaFileName, Frozen: mv.Frozen,
		})
		metrics.ImportFilesTotal.WithLabelValues(strings.ToLower(mv.Target.Kind), mode.String(), "imported").Inc()
	}

	// The files the plan replaces go to the recycle bin (P61); one already
	// gone is replaced all the same. A file that cannot be recycled is left
	// in place, and so is its MediaFile. Every one lies under the root
	// folder (outsideRoot, above).
	bin := fsops.RecycleBinPath(plan.RecycleBin)
	for _, b := range plan.Replaces {
		if dests[b.Path] {
			x.Replaced = append(x.Replaced, b)
			continue
		}
		if _, err := fsops.Recycle(bin, b.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logging.FromContext(ctx).Warn("fileimport: could not recycle the replaced file; leaving it in place",
				"path", b.Path, "error", err)
			continue
		}
		x.Recycled = append(x.Recycled, b.Path)
		x.Replaced = append(x.Replaced, b)
	}

	if d := plan.Donor; d != nil {
		if err := os.MkdirAll(d.Dir, 0o775); err != nil {
			return nil, fmt.Sprintf("create the donor folder: %v", err), nil
		}
		if refused, why, err := place(ctx, plan, d.Source, d.Dest, mode); err != nil {
			return nil, why, nil
		} else if refused != "" {
			x.Refused = refused
			return x, why, nil
		}
		// The donor is reduced at import, not at graft (loop spec §4.12): its
		// video leaves the library now, however long the graft waits.
		mka, err := w.reduceDonor(ctx, d.Dest, d.Anchor, d.Languages)
		switch {
		case errors.Is(err, donoraudio.ErrLacksLanguage):
			logging.FromContext(ctx).Info("fileimport: the donor lacks a language at reduction", "donor", d.Dest, "error", err)
			x.Refused = schema.ImportRefusedDonorLacksLanguage
			return x, err.Error(), nil
		case err != nil:
			return nil, fmt.Sprintf("reduce the donor %s to its audio: %v", d.Dest, err), nil
		}
		x.DonorPath = mka
		logging.FromContext(ctx).Info("fileimport: imported an audio donor", "donor", mka, "languages", d.Languages)
	}
	return x, "", nil
}

// reduceDonor is ReduceDonor, else donoraudio.Reduce with the plan's
// languages required and nothing optional.
func (w *Worker) reduceDonor(ctx context.Context, src, anchor string, languages []string) (string, error) {
	if w.ReduceDonor != nil {
		return w.ReduceDonor(ctx, src, anchor, languages)
	}
	return donoraudio.Reduce(ctx, src, anchor, languages, nil)
}

// place puts src at dest through placeFile: a destination outside the root
// folder and an overwrite are refusals, anything else a failure.
func place(ctx context.Context, plan schema.ImportPlan, src, dest string, mode fsops.ImportMode) (refused, why string, err error) {
	info, err := os.Stat(src)
	if err != nil {
		return "", fmt.Sprintf("%s: %v", filepath.Base(src), err), err
	}
	err = placeFile(ctx, plan.RootFolder, plan.RecycleBin, src, info, dest, mode)
	switch {
	case err == nil:
		return "", "", nil
	case errors.Is(err, errBlocked):
		return schema.ImportRefusedOutsideRoot, blockedMessage(err), nil
	case errors.Is(err, errWouldOverwrite):
		return schema.ImportRefusedOverwrite, err.Error(), nil
	}
	return "", err.Error(), err
}

// outsideRoot is why the plan replaces a file that does not lie strictly
// under its root folder, "" when every one does. Such a file is never
// recycled, and its MediaFile never deleted, whatever the plan says (the
// librarydelete rule; smoke T2): the whole plan is refused before anything
// is placed, so the item is not left with the new file beside the old one,
// and the manager holds the import for a person (outsideRoot is
// needsPerson).
func outsideRoot(plan schema.ImportPlan) string {
	for _, b := range plan.Replaces {
		if !fsops.StrictlyUnder(b.Path, plan.RootFolder) {
			return fmt.Sprintf("refused to recycle %s (media file %s), which is not inside root folder path %q: "+
				"a file outside the root folder is never touched", b.Path, b.Name, plan.RootFolder)
		}
	}
	return ""
}

// stale re-reads every MediaFile the plan replaces through the APIReader
// (Review Focus 5): one gone, recreated or changed since the plan makes the
// plan stale, and why says which.
func (w *Worker) stale(ctx context.Context, ns string, bases []schema.MediaFileBasis) (why string, err error) {
	for _, b := range bases {
		var mf catalogv1alpha1.MediaFile
		switch err := w.apiReader().Get(ctx, client.ObjectKey{Namespace: ns, Name: b.Name}, &mf); {
		case apierrors.IsNotFound(err):
			return fmt.Sprintf("MediaFile %s is gone since the plan", b.Name), nil
		case err != nil:
			return "", fmt.Errorf("fileimport: read MediaFile %s: %w", b.Name, err)
		case string(mf.UID) != b.UID:
			return fmt.Sprintf("MediaFile %s was recreated since the plan", b.Name), nil
		case b.ResourceVersion != "" && mf.ResourceVersion != b.ResourceVersion:
			return fmt.Sprintf("MediaFile %s changed since the plan", b.Name), nil
		}
	}
	return "", nil
}
