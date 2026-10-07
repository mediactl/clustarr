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

package artwork

import (
	ctrl "sigs.k8s.io/controller-runtime"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
)

// Sources implements remediation.Watcher: S26, the manager's artwork index
// waking the item an object's metadata names (§8.1) -- an original
// fetched, backfilled or lost, an overlay drawn or removed. It wakes the
// whole item key, so the metadata stage re-renders status.artwork and the
// overlay stage status.overlay on the same pass.
func (s *Stage) Sources(ctrl.Manager) ([]remediation.Source, error) {
	if s.o.Index == nil {
		return nil, nil
	}
	return []remediation.Source{{
		Name: "S26/" + events.BucketArtwork,
		Raw:  &catalogartwork.IndexSource[remediation.Key]{Index: s.o.Index, ToKey: KeyOf},
	}}, nil
}

// keyKinds are the loop kinds an artwork object's media kind maps to.
var keyKinds = map[commonv1.MediaKind]remediation.KeyKind{
	commonv1.MediaKindMovie:     remediation.KindMovie,
	commonv1.MediaKindSeries:    remediation.KindSeries,
	commonv1.MediaKindAlbum:     remediation.KindAlbum,
	commonv1.MediaKindBook:      remediation.KindBook,
	commonv1.MediaKindAudiobook: remediation.KindAudiobook,
	commonv1.MediaKindComic:     remediation.KindComic,
}

// KeyOf is the loop key of an artwork object's item; false for a kind the
// loop has no key for (Artist and Author wake their reconcilers).
func KeyOf(kind commonv1.MediaKind, namespace, name string) (remediation.Key, bool) {
	k, ok := keyKinds[kind]
	if !ok || name == "" {
		return remediation.Key{}, false
	}
	return remediation.Key{Kind: k, Namespace: namespace, Name: name}, true
}
