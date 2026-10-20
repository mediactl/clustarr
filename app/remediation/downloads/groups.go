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

package downloads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/grouplang"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// observeGroup records what the inspected files of in's entry carried
// against the release's group (pkg/grouplang), once per entry: the
// inspection probed the files before any transcode dropped a language, and
// a refused import's files teach as much as an accepted one's -- a "DUAL"
// release that carried Portuguese is how its group is learned. A failure is
// logged and retried at the next pass.
func (s *Stage) observeGroup(ctx context.Context, in importplan.Input) {
	group := in.Entry.Release.ReleaseGroup
	r := in.Inspect
	if group == "" || s.o.Bus == nil || r == nil || r.Inspect == nil || len(r.Inspect.Files) == 0 {
		return
	}
	uid := types.UID(in.Entry.UID)
	if _, done := s.groupsObserved.Load(uid); done {
		return
	}
	kv := s.o.Bus.KV(events.BucketReleaseGroups)
	for _, f := range r.Inspect.Files {
		if f.Probe == nil {
			continue
		}
		sum := sha256.Sum256([]byte(string(uid) + "\x00" + f.Path))
		if err := grouplang.Observe(ctx, kv, group, hex.EncodeToString(sum[:8]), f.Probe.AudioLanguages, in.OriginalLanguage); err != nil {
			logging.FromContext(ctx).Warn("downloads: recording a release group's audio failed; retrying at the next pass",
				"group", group, "entry", in.Entry.ID, "error", err)
			return
		}
	}
	s.groupsObserved.Store(uid, struct{}{})
}
