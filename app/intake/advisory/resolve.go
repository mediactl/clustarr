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

package advisory

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	cataloghistory "github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/events"
)

// resolved is one task's dispatch: its CR, seq and sub.
type resolved struct {
	target cataloghistory.Target
	seq    int64
	sub    string
}

func (r resolved) key() dispatch.Key {
	return dispatch.Key{Kind: r.target.Kind, Namespace: r.target.Namespace, Name: r.target.Name, Sub: r.sub}
}

// indexed is a resolved nak remembered by its Msg-Id, so a later term of
// the same message resolves without the message.
type indexed struct {
	resolved
	at time.Time
}

// indexTTL is how long a resolved Msg-Id is remembered: the task-events
// stream's own MaxAge, past which no advisory of it can still arrive.
const indexTTL = events.TaskEventsMaxAge

// indexCap bounds the Msg-Id index; past it a nak is resolved but not
// remembered, and its term falls back to ParseDispatchID.
const indexCap = 16384

// UIDResolver maps the UID a terminated task's Msg-Id names to its CR
// (ruling A2-2): the message is gone from its WorkQueue stream, so only the
// Msg-Id is left.
type UIDResolver interface {
	ResolveUID(ctx context.Context, id cataloghistory.DispatchID) (cataloghistory.Target, bool)
}

// CacheResolver resolves through the manager's cache: a MediaFile by the
// remediation loop's UID index, a TranscodeJob by listing. An item's, a
// Search's or an entry's UID is not indexed yet (A3 and A4 add those), so
// such a term resolves only from a nak the intake indexed first.
type CacheResolver struct {
	Reader client.Reader
}

// ResolveUID implements UIDResolver.
func (r CacheResolver) ResolveUID(ctx context.Context, id cataloghistory.DispatchID) (cataloghistory.Target, bool) {
	if r.Reader == nil || id.UID == "" {
		return cataloghistory.Target{}, false
	}
	switch id.Kind {
	case "MediaFile":
		var list catalogv1alpha1.MediaFileList
		if err := r.Reader.List(ctx, &list, client.MatchingFields{mfindex.UID: id.UID}); err != nil || len(list.Items) != 1 {
			return cataloghistory.Target{}, false
		}
		mf := list.Items[0]
		return cataloghistory.Target{
			Namespace: mf.Namespace, Name: mf.Name,
			Kind: "MediaFile", APIVersion: catalogv1alpha1.GroupVersion.String(),
		}, true
	case "TranscodeJob":
		var list transcodev1alpha1.TranscodeJobList
		if err := r.Reader.List(ctx, &list); err != nil {
			return cataloghistory.Target{}, false
		}
		for i := range list.Items {
			if list.Items[i].UID == types.UID(id.UID) {
				return cataloghistory.Target{
					Namespace: list.Items[i].Namespace, Name: list.Items[i].Name,
					Kind: "TranscodeJob", APIVersion: transcodev1alpha1.GroupVersion.String(),
				}, true
			}
		}
	}
	return cataloghistory.Target{}, false
}

// remember indexes r under msgID.
func (i *Intake) remember(msgID string, r resolved, now time.Time) {
	if msgID == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.index[msgID]; !ok && len(i.index) >= indexCap {
		return
	}
	i.index[msgID] = indexed{resolved: r, at: now}
}

// lookup is msgID's indexed resolution.
func (i *Intake) lookup(msgID string) (resolved, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	e, ok := i.index[msgID]
	return e.resolved, ok
}

// resolveTerm resolves a terminated task's Clustarr-Id: the index first,
// then the Msg-Id shape through the UIDResolver.
func (i *Intake) resolveTerm(ctx context.Context, id string) (resolved, bool) {
	if r, ok := i.lookup(id); ok {
		return r, true
	}
	did, ok := cataloghistory.ParseDispatchID(id)
	if !ok || i.ByUID == nil {
		return resolved{}, false
	}
	t, ok := i.ByUID.ResolveUID(ctx, did)
	if !ok || !t.KindKnown() {
		return resolved{}, false
	}
	return resolved{target: t, seq: did.Seq, sub: did.Sub}, true
}
