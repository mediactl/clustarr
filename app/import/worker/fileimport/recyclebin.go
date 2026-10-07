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
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// RecycleSweeper empties the RootFolders' recycle bins of what has outlived
// spec.recycleBin.cleanupDays: it is that field's consumer, which nothing
// was. Sonarr's RecycleBinProvider.Cleanup is the model -- a housekeeping
// task that removes what is older than the cleanup days, and does nothing
// when they are 0.
//
// It is the importarr-recycle handler on the import agent (spec 2026-10-06
// §3.5.3): the manager queues one task every 6 h, and whichever import pod
// takes it sweeps. Two pods sweeping one RWX bin at once remove the same
// folders, and whichever is second finds them gone.
//
// Each distinct bin path is swept once per pass, whichever RootFolders
// share it -- they all default to /data/.recycle -- and with the LONGEST
// retention any of them asks for: a file recycled from one root folder
// cannot be told from another's in a shared bin, and removing it sooner
// than its own root folder asked is data loss where keeping it longer is
// not. For the same reason a bin any sharing RootFolder sets cleanupDays 0
// for is not swept at all. And a bin path that is, or contains, a
// RootFolder's own path is refused: a sweep removes every date-named
// folder directly beneath the bin, and a library's folders are not its to
// judge.
type RecycleSweeper struct {
	// Client lists RootFolders, across every namespace the cache covers.
	Client client.Reader

	// Clock is the time source, injected so tests are deterministic.
	Clock func() time.Time

	// Namespace, when set, limits a sweep to that namespace's RootFolders.
	// Empty -- production -- is every RootFolder the cache holds. A bin
	// shared with a RootFolder outside the namespace is then judged
	// without it, so set it only where the namespace owns its bins (a
	// test, or a single-namespace install).
	Namespace string
}

// NewRecycleSweeper builds a RecycleSweeper with the production clock.
func NewRecycleSweeper(c client.Reader) *RecycleSweeper {
	return &RecycleSweeper{Client: c, Clock: time.Now}
}

// Handle runs one importarr-recycle task: one SweepOnce over every bin,
// with in-progress acks while it runs, since removing a large day folder on
// NFS can outlast the 5 min first-delivery deadline (BackOff[0]). A failed
// sweep is retried on the consumer's backoff; a payload that is not a
// RecycleSweepTask is dead-lettered at once.
func (s *RecycleSweeper) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	var task schema.RecycleSweepTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable recycle sweep task", err)
	}
	stop := keepAlive(ctx, m)
	defer stop()
	if err := s.SweepOnce(ctx); err != nil {
		return fmt.Errorf("fileimport: recycle sweep for the slot at %s: %w", task.Period.UTC().Format(time.RFC3339), err)
	}
	return nil
}

// keepAlive sends an in-progress ack every heartbeatInterval until stop.
func keepAlive(ctx context.Context, m events.Message) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := heartbeat(ctx, m); err != nil && ctx.Err() == nil {
					logging.FromContext(ctx).Warn("fileimport: recycle sweep heartbeat failed", "error", err)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

// binPlan is one bin path and what the RootFolders sharing it ask of it.
type binPlan struct {
	path      string
	retention int32 // the longest cleanupDays any sharer sets
	disabled  bool  // some sharer sets cleanupDays 0
	folders   []string
}

// SweepOnce is one pass over every recycle bin: see [RecycleSweeper] for the
// rules. It returns the bins' errors joined; a bin that fails does not stop
// the others.
func (s *RecycleSweeper) SweepOnce(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "fileimport.RecycleSweeper.SweepOnce")
	defer span.End()
	log := logging.FromContext(ctx)

	var folders catalogv1alpha1.RootFolderList
	var opts []client.ListOption
	if s.Namespace != "" {
		opts = append(opts, client.InNamespace(s.Namespace))
	}
	if err := s.Client.List(ctx, &folders, opts...); err != nil {
		return fmt.Errorf("fileimport: list root folders for the recycle sweep: %w", err)
	}
	bins, libraries := plan(folders.Items)
	now := time.Now()
	if s.Clock != nil {
		now = s.Clock()
	}

	var errs []error
	for _, b := range bins {
		switch {
		case b.disabled:
			log.Debug("fileimport: recycle bin cleanup is off (cleanupDays 0)", "bin", b.path, "rootFolders", b.folders)
			continue
		case !filepath.IsAbs(b.path):
			log.Warn("fileimport: not sweeping a recycle bin whose path is not absolute", "bin", b.path, "rootFolders", b.folders)
			continue
		}
		if lib := holdsLibrary(b.path, libraries); lib != "" {
			log.Warn("fileimport: not sweeping a recycle bin that is, or contains, a root folder's library",
				"bin", b.path, "library", lib, "rootFolders", b.folders)
			continue
		}
		removed, err := fsops.SweepRecycleBin(ctx, b.path, time.Duration(b.retention)*24*time.Hour, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("sweep %s: %w", b.path, err))
			continue
		}
		if removed > 0 {
			log.Info("fileimport: emptied expired days from a recycle bin", "bin", b.path, "removed", removed,
				"cleanupDays", b.retention, "rootFolders", b.folders)
		}
	}
	return errors.Join(errs...)
}

// plan groups root folders by recycle-bin path (the CRD default applied,
// then cleaned) and returns the bins in path order, plus every root
// folder's library path.
func plan(items []catalogv1alpha1.RootFolder) (bins []binPlan, libraries []string) {
	byPath := map[string]*binPlan{}
	for i := range items {
		rf := &items[i]
		if !rf.DeletionTimestamp.IsZero() {
			continue
		}
		if rf.Spec.Path != "" {
			libraries = append(libraries, filepath.Clean(rf.Spec.Path))
		}
		path := filepath.Clean(fsops.RecycleBinPath(rf.Spec.RecycleBin.Path))
		b, ok := byPath[path]
		if !ok {
			b = &binPlan{path: path}
			byPath[path] = b
		}
		days := rf.Spec.RecycleBin.CleanupDaysOrDefault()
		if days <= 0 {
			b.disabled = true
		}
		b.retention = max(b.retention, days)
		b.folders = append(b.folders, rf.Namespace+"/"+rf.Name)
	}
	for _, b := range byPath {
		bins = append(bins, *b)
	}
	sort.Slice(bins, func(i, j int) bool { return bins[i].path < bins[j].path })
	return bins, libraries
}

// holdsLibrary returns the first library path that is bin itself or lies
// beneath it, or "".
func holdsLibrary(bin string, libraries []string) string {
	for _, lib := range libraries {
		rel, err := filepath.Rel(bin, lib)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return lib
		}
	}
	return ""
}
