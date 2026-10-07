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

// Package graftstate is what squasharr's TranscodeProfile, TranscodeJob
// and AudioGraft controllers share about an audio graft's state, kept apart
// so none of the three imports another for it.
package graftstate

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// JoinedPrefix starts an AudioGraft's status.jobName when its graft rides
// along with a TranscodeJob: "transcodejob/<name>".
const JoinedPrefix = "transcodejob/"

// Reason the AudioGraft controller gives a reduce Job.
const ReasonReducing = "Reducing"

// Joined is the TranscodeJob an AudioGraft's graft rides along with, "" when
// none.
func Joined(g *transcodev1alpha1.AudioGraft) string {
	if name, ok := strings.CutPrefix(g.Status.JobName, JoinedPrefix); ok {
		return name
	}
	return ""
}

// Standalone reports whether g runs a graft Job of its own on its file: the
// one state in which a transcode must keep off the file (a reduce writes
// only the donor; a joined graft is the transcode).
func Standalone(g *transcodev1alpha1.AudioGraft) bool {
	switch g.Status.Phase {
	case transcodev1alpha1.AudioGraftPending, transcodev1alpha1.AudioGraftRunning:
	default:
		return false
	}
	return g.Status.JobName != "" && Joined(g) == "" && g.Status.Reason != ReasonReducing
}

// Grafting is the set of MediaFiles (namespace/name) a standalone graft is
// rewriting, which the TranscodeProfile controller leaves alone (spec §7.2:
// never a graft and a transcode on one file at once).
func Grafting(ctx context.Context, c client.Reader) (map[types.NamespacedName]bool, error) {
	var l transcodev1alpha1.AudioGraftList
	if err := c.List(ctx, &l); err != nil {
		return nil, fmt.Errorf("graftstate: list AudioGrafts: %w", err)
	}
	out := map[types.NamespacedName]bool{}
	for i := range l.Items {
		if g := &l.Items[i]; Standalone(g) && g.Status.MediaFileRef != "" {
			out[types.NamespacedName{Namespace: g.Namespace, Name: g.Status.MediaFileRef}] = true
		}
	}
	return out, nil
}
