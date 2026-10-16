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
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// ErrOutsideRootFolder is a named path that does not lie strictly under the
// task's RootFolder: a path the handler cannot place inside the library is
// not one it may take out of it.
var ErrOutsideRootFolder = errors.New("fileimport: file is not under the root folder")

// ErrNoRecycleBin is a RootFolder with an empty spec.recycleBin.path (the
// CRD defaults it, so this is a hand-cleared value). Recycling never falls
// back to unlinking, except for an orphan transcode part (see RecycleFiles).
var ErrNoRecycleBin = errors.New("fileimport: root folder has no recycle bin path")

// RecycleFiles moves the files a schema.RecycleFilesTask names into their
// RootFolder's recycle bin (spec.recycleBin.path), never a bare unlink, and
// prunes the folders it leaves empty up to, never including, the root
// folder (ADR-0019 §7.6, W29, P81). It is the import-recycle handler for
// clustarr.work.import.recycle.files.<id>: the ImportList controller asks
// for an item's files when spec.syncLevel removeAndDelete removes it, and
// the manager's scan applier for an orphan transcode part.
//
// The manager decided which files go; this handler only moves them. Beside
// each named file that is not a transcode part it also moves the subtitle
// sidecars on disk that name it (subtitles.SidecarsOf): the transcode writes
// the source's own subtitles beside its output (MP4 standard §4.1), and no
// MediaFile, so no task, lists them.
//
// Every path is checked before any is moved: one outside the root folder
// refuses the whole task (ErrOutsideRootFolder), and so does a root folder
// with no recycle bin (ErrNoRecycleBin), except that a transcode part
// (fsops.ParseTranscodePart) under a root folder with no bin is removed
// outright, after an Lstat shows it is still a regular file of that shape:
// it is a dead attempt's output, never a library file, and the rescan
// removed such parts outright before the scan applier asked for this. Both
// refusals are permanent and dead-letter the task; a file already gone is
// done (a redelivery after a partial run).
type RecycleFiles struct {
	// Client reads the RootFolder.
	Client client.Reader
}

// NewRecycleFiles builds a RecycleFiles handler.
func NewRecycleFiles(c client.Reader) *RecycleFiles { return &RecycleFiles{Client: c} }

// RecycleHandler routes import-recycle's two task kinds: a
// schema.RecycleFilesTask to files, anything else to the sweep, which
// dead-letters a payload that is not a RecycleSweepTask.
func RecycleHandler(sweep *RecycleSweeper, files *RecycleFiles) events.Handler {
	return func(ctx context.Context, m events.Message) error {
		if env := m.Envelope(); env != nil && env.Schema == (schema.RecycleFilesTask{}).Schema() {
			return files.Handle(ctx, m)
		}
		return sweep.Handle(ctx, m)
	}
}

// Handle runs one recycle-files task.
func (h *RecycleFiles) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "fileimport.RecycleFiles.Handle")
	defer span.End()

	var task schema.RecycleFilesTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable recycle files task", err)
	}
	if task.RootFolder.Name == "" {
		return events.Discard("recycle files task names no root folder", fmt.Errorf("task %+v", task.RootFolder))
	}
	log := logging.FromContext(ctx).With("rootFolder", task.RootFolder.String())
	ctx = logging.NewContext(ctx, log)

	var rf catalogv1alpha1.RootFolder
	key := types.NamespacedName{Namespace: task.RootFolder.Namespace, Name: task.RootFolder.Name}
	if err := h.Client.Get(ctx, key, &rf); err != nil {
		if apierrors.IsNotFound(err) {
			return events.Discard("recycle files: the root folder is gone; nothing to place the files under", err)
		}
		return fmt.Errorf("fileimport: get root folder %s: %w", key, err)
	}
	if task.RootFolder.UID != "" && string(rf.UID) != task.RootFolder.UID {
		return events.Discard("recycle files: the root folder was replaced", fmt.Errorf(
			"root folder %s has uid %s, task was published for %s", key, rf.UID, task.RootFolder.UID))
	}

	paths, err := recyclePlan(rf.Spec.Path, rf.Spec.RecycleBin.Path, task.Paths)
	if err != nil {
		return events.Discard("recycle files: refused", fmt.Errorf("%w: root folder %s", err, key))
	}
	stop := keepAlive(ctx, m)
	defer stop()
	root, bin := rf.Spec.Path, rf.Spec.RecycleBin.Path
	for _, p := range paths {
		if bin == "" {
			if err := removePart(p); err != nil {
				return fmt.Errorf("fileimport: remove transcode part %s: %w", p, err)
			}
			log.Info("fileimport: removed an orphan transcode part (the root folder has no recycle bin)", "path", p)
		} else {
			dest, err := recycleIfPresent(bin, p)
			if err != nil {
				return fmt.Errorf("fileimport: recycle %s: %w", p, err)
			}
			if dest != "" {
				log.Info("fileimport: recycled a file", "path", p, "recycledTo", dest)
			}
		}
		fsops.PruneEmptyDirs(root, filepath.Dir(p))
	}
	return nil
}

// recyclePlan checks every named path against the root folder and the bin
// and returns what to move: the named paths and, beside each one that is
// not a transcode part, its subtitle sidecars on disk. It refuses before
// anything moves.
func recyclePlan(root, bin string, named []string) ([]string, error) {
	var out []string
	for _, p := range named {
		if !fsops.StrictlyUnder(p, root) {
			return nil, fmt.Errorf("%w: %q is not under %q", ErrOutsideRootFolder, p, root)
		}
		_, part := fsops.ParseTranscodePartCopy(p)
		if bin == "" && !part {
			return nil, fmt.Errorf("%w: %q", ErrNoRecycleBin, p)
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
		if part {
			continue
		}
		found, _ := subtitles.SidecarsOf(p)
		for _, sc := range found {
			if fsops.StrictlyUnder(sc, root) && !slices.Contains(out, sc) {
				out = append(out, sc)
			}
		}
	}
	return out, nil
}

// recycleIfPresent moves path into bin, or does nothing when path is
// already gone (a redelivery after a partial run). It returns the
// destination, or "" when there was nothing to move.
func recycleIfPresent(bin, path string) (string, error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return fsops.Recycle(bin, path)
}

// removePart removes an orphan transcode part outright: only while an Lstat
// shows a regular file whose name still parses as a transcode part or its
// copy in flight (fsops.ParseTranscodePartCopy). A part
// already gone is done.
func removePart(path string) error {
	if _, ok := fsops.ParseTranscodePartCopy(path); !ok {
		return fmt.Errorf("%w: %q is not a transcode part", ErrNoRecycleBin, path)
	}
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%q is not a regular file", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
