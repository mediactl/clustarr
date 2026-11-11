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

// Package nfo is the import domain's import-nfo consumer
// (docs/superpowers/specs/2026-11-10-non-video-libraries-design.md §8.2):
// it writes the artist.nfo, album.nfo and image files the manager rendered
// for an item into the item's folder on /data and answers in clustarr-nfo.
// The manager renders every byte (pkg/nfo); this worker decides nothing
// but where a file may go and whether it may replace what is there.
package nfo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/version"
)

// HeartbeatInterval is how often a write in progress resets its delivery's
// deadline while it copies an image from the artwork store.
const HeartbeatInterval = 10 * time.Second

// DefaultMode is the mode a written file carries when the task names none:
// RootFolderSpec.Perms.FileMode's default, readable by a media server
// running as another uid in the media group.
const DefaultMode os.FileMode = 0o664

// maxImageBytes bounds one image copied from the artwork store.
const maxImageBytes = 64 << 20

// Worker is the import-nfo handler.
type Worker struct {
	dataDir string
	store   events.ObjectStore
	answers *records.Answerer[*schema.NFORecord]
	reader  *records.Requester[*schema.NFORecord]
	now     func() time.Time
}

// New builds the worker over bus: dataDir is the library volume's mount
// (every write stays under it), writer the pod's name.
func New(bus events.Bus, dataDir, writer string) *Worker {
	spec := agentrecords.NFO()
	kv := bus.KV(events.BucketNFO)
	return &Worker{
		dataDir: filepath.Clean(dataDir),
		store:   bus.ObjectStore(events.BucketArtwork),
		answers: records.NewAnswerer(kv, spec, writer, version.String()),
		reader:  records.NewRequester(kv, spec),
		now:     time.Now,
	}
}

// Handle writes one NFOWriteTask. A task whose record was superseded (a
// newer request, a withdrawal, an answer at its seq) is acknowledged and
// ignored; a folder outside the data directory is dead-lettered; an I/O
// failure answers the record failed (transient) and is retried.
func (w *Worker) Handle(ctx context.Context, m events.Message) error {
	ctx, span := tracing.Start(ctx, "import.nfo.Handle")
	defer span.End()
	env := m.Envelope()
	var task schema.NFOWriteTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable NFO write task", err)
	}
	log := logging.FromContext(ctx).With("item", task.Item.Kind+"/"+task.Item.Namespace+"/"+task.Item.Name, "folder", task.Folder, "seq", task.Seq)
	key := agentrecords.NFOKey(task.Item.UID)
	if superseded, err := w.answers.Superseded(ctx, key, task.Seq); err != nil {
		return fmt.Errorf("nfo: read the record: %w", err)
	} else if superseded {
		log.Debug("nfo: write superseded; dropped")
		return nil
	}
	folder, err := w.folderUnderData(task.Folder)
	if err != nil {
		return events.Discard("NFO folder refused", err)
	}

	// The names this worker wrote before (status.nfo.files, carried as
	// task.Owned; the record's last answer too, while it still has one) are
	// the only ones it rewrites; anything else already in the folder is
	// somebody's and is kept.
	mine := slices.Clone(task.Owned)
	if cur, _, ok, err := w.reader.Get(ctx, key); err == nil && ok && cur.Answer != nil {
		mine = append(mine, cur.Answer.Files...)
	}

	stop := w.keepAlive(ctx, m)
	defer stop()
	ans, werr := w.write(ctx, folder, task, mine)
	rec := &schema.NFORecord{
		Request: schema.NFOWriteRequest{Folder: task.Folder, Hash: task.Hash, Files: fileNames(task), Images: imageNames(task)},
		Answer:  &ans,
	}
	h := rec.Header()
	h.Schema, h.Item, h.Seq, h.State = schema.NFORecord{}.Schema(), &task.Item, task.Seq, records.StateAnswered
	if werr != nil {
		h.State, h.Failure, h.Transient = records.StateFailed, clamp(werr.Error(), schema.MaxRecordFailure), true
		if len(ans.Files) == 0 {
			rec.Answer = nil
		}
	}
	if _, err := w.answers.Answer(ctx, key, rec); err != nil && !errors.Is(err, records.ErrRaced) {
		return fmt.Errorf("nfo: answer the record: %w", err)
	}
	if werr != nil {
		return fmt.Errorf("nfo: write %s: %w", task.Folder, werr)
	}
	log.Info("nfo: wrote", "files", ans.Files, "kept", ans.Kept)
	return nil
}

// write puts every document and image of task into folder, skipping a
// name that exists and is not one of mine. It returns what it wrote and
// kept, and the first error; the files written before the error stay, so a
// retry has less to do and the record says what landed.
func (w *Worker) write(ctx context.Context, folder string, task schema.NFOWriteTask, mine []string) (schema.NFOWriteAnswer, error) {
	ans := schema.NFOWriteAnswer{WrittenAt: w.now().UTC()}
	mode := DefaultMode
	if task.Mode != 0 {
		mode = os.FileMode(task.Mode)
	}
	if err := os.MkdirAll(folder, 0o775); err != nil {
		return ans, err
	}
	for _, f := range task.Files {
		path, err := w.fileIn(folder, f.Name)
		if err != nil {
			return ans, err
		}
		if keep(path, f.Name, mine) {
			ans.Kept = append(ans.Kept, f.Name)
			continue
		}
		if err := fsops.AtomicWrite(path, bytes.NewReader(f.Content), mode); err != nil {
			return ans, err
		}
		ans.Files = append(ans.Files, f.Name)
	}
	for _, img := range task.Images {
		path, err := w.fileIn(folder, img.Name)
		if err != nil {
			return ans, err
		}
		if keep(path, img.Name, mine) {
			ans.Kept = append(ans.Kept, img.Name)
			continue
		}
		if err := w.copyImage(ctx, path, img, mode); err != nil {
			return ans, err
		}
		ans.Files = append(ans.Files, img.Name)
	}
	return ans, nil
}

func (w *Worker) copyImage(ctx context.Context, path string, img schema.NFOImage, mode os.FileMode) error {
	store := w.store
	if img.Bucket != "" && img.Bucket != events.BucketArtwork {
		return fmt.Errorf("image %s names bucket %s; only %s is read", img.Name, img.Bucket, events.BucketArtwork)
	}
	_, rc, err := store.Get(ctx, img.Object)
	if err != nil {
		return fmt.Errorf("read artwork object %s: %w", img.Object, err)
	}
	defer rc.Close()
	return fsops.AtomicWrite(path, io.LimitReader(rc, maxImageBytes), mode)
}

// folderUnderData cleans folder and refuses one outside the data directory.
func (w *Worker) folderUnderData(folder string) (string, error) {
	clean := filepath.Clean(folder)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("folder %q is not absolute", folder)
	}
	rel, err := filepath.Rel(w.dataDir, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("folder %q is outside %s", folder, w.dataDir)
	}
	return clean, nil
}

// fileIn refuses a name with a path in it: every file goes into the folder.
func (w *Worker) fileIn(folder, name string) (string, error) {
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return "", fmt.Errorf("file name %q is not a bare name", name)
	}
	return filepath.Join(folder, name), nil
}

// keep reports whether path exists and is not one of the names this worker
// wrote before: an operator's own file, never replaced.
func keep(path, name string, mine []string) bool {
	if slices.Contains(mine, name) {
		return false
	}
	_, err := os.Lstat(path)
	return err == nil
}

func (w *Worker) keepAlive(ctx context.Context, m events.Message) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := m.InProgress(ctx); err != nil && ctx.Err() == nil {
					logging.FromContext(ctx).Warn("nfo: heartbeat failed", "err", err)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

func fileNames(t schema.NFOWriteTask) []string {
	out := make([]string, 0, len(t.Files))
	for _, f := range t.Files {
		out = append(out, f.Name)
	}
	return out
}

func imageNames(t schema.NFOWriteTask) []string {
	out := make([]string, 0, len(t.Images))
	for _, i := range t.Images {
		out = append(out, i.Name)
	}
	return out
}

func clamp(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
