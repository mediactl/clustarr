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

package subtitles

import (
	"sync"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/app/caption/subtitleplan"
)

// listedSet is subtitles.listed (§6.4.3 gate 4): what this process last
// listed each file for. It is per process and starts empty, so a new
// leader relists every video file once.
type listedSet struct {
	mu sync.Mutex
	m  map[types.UID]subtitleplan.Listed
}

func newListedSet() *listedSet { return &listedSet{m: map[types.UID]subtitleplan.Listed{}} }

func (s *listedSet) get(uid types.UID) *subtitleplan.Listed {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.m[uid]
	if !ok {
		return nil
	}
	return &l
}

func (s *listedSet) set(uid types.UID, l subtitleplan.Listed) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[uid] = l
}
