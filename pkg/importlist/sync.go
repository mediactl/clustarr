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

package importlist

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// SyncLevel mirrors catalogv1alpha1.SyncLevel (this package may not import
// the CRD group). It says what happens to catalog items that came from an
// ImportList and have since fallen off the remote list.
type SyncLevel string

// Sync levels.
const (
	SyncLevelDisabled         SyncLevel = "disabled"
	SyncLevelLogOnly          SyncLevel = "logOnly"
	SyncLevelKeepAndUnmonitor SyncLevel = "keepAndUnmonitor"
	SyncLevelRemoveAndKeep    SyncLevel = "removeAndKeep"
	SyncLevelRemoveAndDelete  SyncLevel = "removeAndDelete"
)

// SyncAction is what ApplySyncLevel decided to do about one catalog item
// that fell off a list. It carries no I/O of its own -- the ImportList
// controller is what turns a SyncDecision into an actual catalog write.
type SyncAction string

// Sync actions.
const (
	SyncActionLog             SyncAction = "log"
	SyncActionUnmonitor       SyncAction = "unmonitor"
	SyncActionRemove          SyncAction = "remove"          // remove the catalog item, keep files
	SyncActionRemoveAndDelete SyncAction = "removeAndDelete" // remove the catalog item and its files
)

// SyncDecision is one catalog item ApplySyncLevel decided needs an action.
type SyncDecision struct {
	Item   Item
	Action SyncAction
}

// ErrUnknownSyncLevel is returned by ApplySyncLevel when level is not one
// of the five SyncLevel constants.
var ErrUnknownSyncLevel = errors.New("importlist: unknown sync level")

// syncLevelActions maps every SyncLevel except SyncLevelDisabled (handled
// separately by ApplySyncLevel, since it produces no decisions at all) to
// the SyncAction it applies to an item that has fallen off the list.
var syncLevelActions = map[SyncLevel]SyncAction{
	SyncLevelLogOnly:          SyncActionLog,
	SyncLevelKeepAndUnmonitor: SyncActionUnmonitor,
	SyncLevelRemoveAndKeep:    SyncActionRemove,
	SyncLevelRemoveAndDelete:  SyncActionRemoveAndDelete,
}

// Key returns the identity used for de-duplication and sync matching: the
// first non-empty of "tmdb:<id>", "imdb:<id>", "tvdb:<id>", then the other
// kind-specific IDs (musicbrainz, isbn, asin, comicvine, anilist, in that
// order) if Item carries one, else "title:<normalized title>:<year>"
// (lower-case, a trailing "(YYYY)" annotation stripped, non-alphanumerics
// collapsed to single spaces, trimmed).
func (i Item) Key() string {
	switch {
	case i.ExternalIDs.TMDB != "":
		return "tmdb:" + i.ExternalIDs.TMDB
	case i.ExternalIDs.IMDb != "":
		return "imdb:" + i.ExternalIDs.IMDb
	case i.ExternalIDs.TVDB != "":
		return "tvdb:" + i.ExternalIDs.TVDB
	case i.ExternalIDs.MusicBrainz != "":
		return "musicbrainz:" + i.ExternalIDs.MusicBrainz
	case i.ExternalIDs.ISBN != "":
		return "isbn:" + i.ExternalIDs.ISBN
	case i.ExternalIDs.ASIN != "":
		return "asin:" + i.ExternalIDs.ASIN
	case i.ExternalIDs.ComicVine != "":
		return "comicvine:" + i.ExternalIDs.ComicVine
	case i.ExternalIDs.AniList != "":
		return "anilist:" + i.ExternalIDs.AniList
	default:
		return fmt.Sprintf("title:%s:%d", normalizeTitle(i.Title), i.Year)
	}
}

// normalizeTitle lower-cases title, strips a trailing "(YYYY)" year
// annotation some providers embed directly in the title (so "The Matrix
// (1999)" and "The Matrix" normalize identically when paired with the same
// separate Year field), and collapses every run of non-alphanumeric runes
// to a single space.
func normalizeTitle(title string) string {
	t := strings.TrimSpace(title)
	if stripped, ok := stripTrailingYear(t); ok {
		t = stripped
	}

	var b strings.Builder
	lastWasSpace := true // suppress a leading space in the result
	for _, r := range t {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
			lastWasSpace = false
			continue
		}
		if !lastWasSpace {
			b.WriteByte(' ')
			lastWasSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// stripTrailingYear removes a trailing "(YYYY)" annotation from t, if
// present, and reports whether it did.
func stripTrailingYear(t string) (string, bool) {
	if !strings.HasSuffix(t, ")") {
		return t, false
	}
	open := strings.LastIndex(t, "(")
	if open < 0 {
		return t, false
	}
	inner := t[open+1 : len(t)-1]
	if len(inner) != 4 {
		return t, false
	}
	for _, r := range inner {
		if r < '0' || r > '9' {
			return t, false
		}
	}
	return strings.TrimSpace(t[:open]), true
}

// Dedupe drops later items whose Key repeats an earlier one; order is
// preserved, first wins.
func Dedupe(items []Item) []Item {
	seen := make(map[string]struct{}, len(items))
	out := make([]Item, 0, len(items))
	for _, it := range items {
		k := it.Key()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, it)
	}
	return out
}

// ApplySyncLevel decides what happens to each existing catalog item
// (previously added from this list) that is no longer present in current,
// matched by Key: SyncLevelDisabled produces no decision at all (an empty
// slice); every other level produces one SyncDecision per item that fell
// off, with the action syncLevelActions maps the level to. An item still
// present in current produces no decision. An unknown level returns
// ErrUnknownSyncLevel. ApplySyncLevel is pure: no I/O, and it never
// fetches or writes anything itself.
func ApplySyncLevel(level SyncLevel, current, existing []Item) ([]SyncDecision, error) {
	if level == SyncLevelDisabled {
		return []SyncDecision{}, nil
	}
	action, ok := syncLevelActions[level]
	if !ok {
		return nil, fmt.Errorf("importlist: unknown sync level %q: %w", string(level), ErrUnknownSyncLevel)
	}

	currentKeys := make(map[string]struct{}, len(current))
	for _, it := range current {
		currentKeys[it.Key()] = struct{}{}
	}

	decisions := make([]SyncDecision, 0, len(existing))
	for _, it := range existing {
		if _, present := currentKeys[it.Key()]; present {
			continue
		}
		decisions = append(decisions, SyncDecision{Item: it, Action: action})
	}
	return decisions, nil
}
