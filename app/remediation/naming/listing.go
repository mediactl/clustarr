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
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// maxSidecars is status.sidecars' MaxItems (F2.1).
const maxSidecars = 32

// Listing is one ReadDir of a file's directory: its regular files' names.
type Listing struct {
	Dir   string
	Names []string
}

// listings is the temporary sidecar listing's memory (until F5.1 moves the
// listing to the subtitles planner, loop spec §6.4.3): what each file was
// last listed for and what that found, and the files a SubtitleRequest
// change marked for a relist. It is leader-local and starts empty, so each
// leader start lists every file once.
type listings struct {
	mu    sync.Mutex
	last  map[types.UID]memo
	stale map[types.NamespacedName]bool
}

type memo struct {
	sig     string // "<probeHash>|<spec.path>"
	listing Listing
}

func signature(mf *catalogv1alpha1.MediaFile) string { return mf.Status.ProbeHash + "|" + mf.Spec.Path }

// need reports whether mf's directory must be listed this pass, and the
// memo to use when it need not.
func (l *listings) need(mf *catalogv1alpha1.MediaFile) (bool, *Listing) {
	l.mu.Lock()
	defer l.mu.Unlock()
	nn := types.NamespacedName{Namespace: mf.Namespace, Name: mf.Name}
	m, ok := l.last[mf.UID]
	if !ok || m.sig != signature(mf) || l.stale[nn] {
		return true, nil
	}
	li := m.listing
	return false, &li
}

func (l *listings) done(mf *catalogv1alpha1.MediaFile, li Listing) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[types.UID]memo{}
	}
	l.last[mf.UID] = memo{sig: signature(mf), listing: li}
	delete(l.stale, types.NamespacedName{Namespace: mf.Namespace, Name: mf.Name})
}

// MarkStale makes nn's next pass list its directory again (the
// SubtitleRequest wake, until F5.3).
func (l *listings) MarkStale(nn types.NamespacedName) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stale == nil {
		l.stale = map[types.NamespacedName]bool{}
	}
	l.stale[nn] = true
}

// sidecars is §2.7's list from a listing: every name ParseSidecar attributes
// to specPath's stem, with its absolute path (release N), sorted by name,
// capped at 32.
func sidecars(specPath string, li Listing) []catalogv1alpha1.Sidecar {
	stem := strings.TrimSuffix(filepath.Base(specPath), filepath.Ext(specPath))
	var out []catalogv1alpha1.Sidecar
	for _, name := range li.Names {
		key, ok := subtitles.ParseSidecar(stem, name)
		if !ok {
			continue
		}
		lang, forced, hi, err := subtitles.ParseLangKey(key)
		if err != nil {
			continue
		}
		out = append(out, catalogv1alpha1.Sidecar{Path: filepath.Join(li.Dir, name), Name: name, Language: lang, Forced: forced, HI: hi})
	}
	slices.SortFunc(out, func(a, b catalogv1alpha1.Sidecar) int { return strings.Compare(a.Name, b.Name) })
	return out[:min(len(out), maxSidecars)]
}
