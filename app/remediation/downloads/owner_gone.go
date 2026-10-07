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
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/remediation"
)

// errOwnerPresent is Gone's answer when the apiserver holds the owner the
// cache did not: nothing is removed, and the key comes back in 5 s.
var errOwnerPresent = errors.New("downloads: the owner exists; the cache has not seen it yet")

// Holds implements remediation.OwnerGone: the Unclaimed index holds a
// transfer naming k.
func (s *Stage) Holds(k remediation.Key) bool {
	return s.unclaimed.holds(ownerKey{Kind: string(k.Kind), Namespace: k.Namespace, Name: k.Name})
}

// Gone implements remediation.OwnerGone (A3.5 step 6, §6.7): one APIReader
// Get of the claim's owner. NotFound, or the same name now holding another
// provider id, removes each transfer with its claim's data rule and records
// TransferOwnerGone on its DownloadClient; the same provider id (a restored
// owner the cache has not seen) does nothing and comes back; any other
// error does nothing -- no evidence, no action.
func (s *Stage) Gone(ctx context.Context, _ *remediation.Env, k remediation.Key) ([]remediation.Effect, []remediation.ItemEvent, error) {
	obj, ok := newOwner(k.Kind)
	if !ok || s.o.APIReader == nil {
		return nil, nil, nil
	}
	transfers := s.unclaimed.forOwner(ownerKey{Kind: string(k.Kind), Namespace: k.Namespace, Name: k.Name})
	if len(transfers) == 0 {
		return nil, nil, nil
	}
	err := s.o.APIReader.Get(ctx, client.ObjectKey{Namespace: k.Namespace, Name: k.Name}, obj)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return nil, nil, err
	default:
		pid := providerIDOf(obj)
		for _, t := range transfers {
			if c := t.Record.Claim; c != nil && (c.ProviderID == "" || c.ProviderID == pid) {
				return nil, nil, errOwnerPresent
			}
		}
	}
	now := s.o.Clock().UTC()
	var (
		effs []remediation.Effect
		evs  []remediation.ItemEvent
	)
	for _, t := range transfers {
		eff, ok := unclaimedAbsent(t, now)
		if !ok {
			continue
		}
		effs = append(effs, eff)
		ev := s.event(owner{}, lifecycle.Event{
			Recorder: lifecycle.RecorderGrabEngine, Type: lifecycle.EventWarning, Reason: lifecycle.ReasonTransferOwnerGone,
			Message: "the owner of transfer " + t.Record.Claim.Entry.ID + " is gone; removing it",
			On:      downloadClientRef(k.Namespace, t.Record.Engine),
		})
		evs = append(evs, ev)
	}
	return effs, evs, nil
}

// newOwner is an empty object of an owner kind.
func newOwner(kind remediation.KeyKind) (client.Object, bool) {
	switch kind {
	case remediation.KindMovie:
		return &catalogv1alpha1.Movie{}, true
	case remediation.KindSeries:
		return &catalogv1alpha1.Series{}, true
	case remediation.KindAlbum:
		return &catalogv1alpha1.Album{}, true
	case remediation.KindBook:
		return &catalogv1alpha1.Book{}, true
	case remediation.KindAudiobook:
		return &catalogv1alpha1.Audiobook{}, true
	case remediation.KindComic:
		return &catalogv1alpha1.Comic{}, true
	}
	return nil, false
}
