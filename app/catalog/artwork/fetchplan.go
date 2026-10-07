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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/version"
)

const (
	// RefetchAge is how old a stored original may grow before the item key
	// fetches its source again (ADR-0019 A5.2; ruling A5-R5): a provider
	// may serve new bytes under an unchanged URL, and an object written by
	// a fetcher that failed half-way is replaced.
	RefetchAge = 90 * 24 * time.Hour

	// FailureRetry is how long a source whose fetch failed rests before the
	// item key asks for it again: a source that keeps failing must not hold
	// a dispatch slot on every pass.
	FailureRetry = 6 * time.Hour

	// ReasonFetchFailed is the Event a source's failed fetch records on its
	// item, once per failure (§7.7), under RecorderGateway.
	ReasonFetchFailed = "ArtworkFetchFailed"

	// RecorderGateway is the recorder of ReasonFetchFailed: the metadata
	// gateway's name, kept (§7.7), though the manager emits it.
	RecorderGateway = "metadata-gateway"
)

// Failure is the latest failed fetch of one type, from the item's metadata
// record (schema.ItemMetadataRecord.ArtworkFailures).
type Failure struct {
	URL string
	At  time.Time
}

// FailuresOf indexes a record's artwork failures by type.
func FailuresOf(rec *schema.ItemMetadataRecord) map[catalogv1alpha1.ImageType]Failure {
	out := map[catalogv1alpha1.ImageType]Failure{}
	if rec == nil {
		return out
	}
	for _, f := range rec.ArtworkFailures {
		out[catalogv1alpha1.ImageType(f.Type)] = Failure{URL: f.URL, At: f.At}
	}
	return out
}

// FetchPlan is which originals an item's fetch task names (ADR-0019 §7.1):
// the images to fetch, the types whose removed override's original goes,
// and when a rested failure is next due.
type FetchPlan struct {
	Fetch []schema.ArtworkFetch
	Drop  []string
	// Due is when the earliest resting failure may be asked again; zero
	// for none.
	Due time.Time
}

// Empty reports a plan that names nothing.
func (p FetchPlan) Empty() bool { return len(p.Fetch) == 0 && len(p.Drop) == 0 }

// Hash is the hex SHA-256 over what the plan names: the fetch task's Msg-Id
// suffix (schema.MsgIDForArtworkFetch), so one plan is published once per
// duplicate window and a changed one at once.
func (p FetchPlan) Hash() string {
	h := sha256.New()
	for _, f := range p.Fetch {
		// NUL-separated: neither an enum token nor a URL admits one.
		_, _ = fmt.Fprintf(h, "fetch\x00%s\x00%s\x00%s\x00%s\x00", f.Type, f.Kind, f.URL, f.Language)
	}
	for _, t := range p.Drop {
		_, _ = fmt.Fprintf(h, "drop\x00%s\x00", t)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PlanFetch is the item key's artwork plan (ADR-0019 §7.1, A5.2; pure). For
// each type, with sources the item's ResolveSources and objects what the
// index has of its originals:
//
//   - a source whose type has no original, or one whose metadata predates
//     events.ArtworkMetaVersion (the fetcher backfills it), or one fetched
//     from another URL or kind, or one older than RefetchAge, is fetched --
//     unless that very URL failed less than FailureRetry ago, which waits;
//   - an original whose type has no source any more is dropped when its
//     metadata says it was a custom override (the override was removed); a
//     provider's is kept, since a provider that did not list a type this
//     time is no reason to drop art already stored;
//   - a failure keeps the object it would have replaced: nothing here
//     deletes an original for a failed fetch.
func PlanFetch(sources map[catalogv1alpha1.ImageType]Source, objects map[catalogv1alpha1.ImageType]Object,
	failures map[catalogv1alpha1.ImageType]Failure, now time.Time,
) FetchPlan {
	var p FetchPlan
	for _, t := range ImageTypes {
		src, hasSrc := sources[t]
		obj := objects[t]
		if !hasSrc {
			if obj.Present && obj.Current && obj.Source == catalogv1alpha1.ArtworkSourceCustom {
				p.Drop = append(p.Drop, string(t))
			}
			continue
		}
		need := !obj.Present || !obj.Current || obj.SourceURL != src.URL || obj.Source != src.Kind ||
			now.Sub(obj.UpdatedAt.Time) > RefetchAge
		if !need {
			continue
		}
		if f, failed := failures[t]; failed && f.URL == src.URL && now.Before(f.At.Add(FailureRetry)) {
			if next := f.At.Add(FailureRetry); p.Due.IsZero() || next.Before(p.Due) {
				p.Due = next
			}
			continue
		}
		p.Fetch = append(p.Fetch, schema.ArtworkFetch{
			Type: string(t), URL: src.URL, Kind: string(src.Kind), Language: src.Language,
		})
	}
	return p
}

// RedactURL drops a URL's userinfo, query and fragment before it reaches an
// Event or a log line: a custom URL may carry a token, and CLAUDE.md records
// two provider keys that leaked into error strings.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "an unparseable URL"
	}
	redacted := u.RawQuery != "" || u.User != nil
	u.User, u.RawQuery, u.Fragment, u.RawFragment = nil, "", "", ""
	if redacted {
		return u.String() + "?<redacted>"
	}
	return u.String()
}

// FailureBook remembers which recorded failures already raised their
// Event, so each raises one (§7.7: "on the transition"). It is
// leader-local: a new leader raises each standing failure once more.
type FailureBook struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewFailureBook returns an empty book.
func NewFailureBook() *FailureBook { return &FailureBook{seen: map[string]time.Time{}} }

// failureMemory is how long the book remembers a failure: longer than the
// metadata record's own TTL would keep it.
const failureMemory = 8 * 24 * time.Hour

// First reports whether item uid's failure f is new to the book, and
// records it.
func (b *FailureBook) First(uid string, f schema.ArtworkFailure, now time.Time) bool {
	key := uid + "\x00" + f.Type + "\x00" + f.At.UTC().Format(time.RFC3339Nano)
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, at := range b.seen {
		if now.Sub(at) > failureMemory {
			delete(b.seen, k)
		}
	}
	if _, ok := b.seen[key]; ok {
		return false
	}
	b.seen[key] = now
	return true
}

// FailureMessage is a failure's Event note, its URL redacted.
func FailureMessage(f schema.ArtworkFailure) string {
	return fmt.Sprintf("%s image from %s was not stored: %s", f.Type, RedactURL(f.URL), f.Error)
}

// FetchTask builds obj's ArtworkFetchTask v2 for plan (ADR-0019 §7.1): its
// subject (WorkArtworkFetchSubject) and envelope under
// schema.MsgIDForArtworkFetch(uid, plan.Hash()). item is obj's ItemRef,
// UID included.
func FetchTask(obj client.Object, kind commonv1.MediaKind, item schema.ItemRef, plan FetchPlan, now time.Time) (string, *events.Envelope, error) {
	name, data, err := schema.Encode(schema.ArtworkFetchTask{
		MediaRef: commonv1.MediaRef{Kind: kind, Name: obj.GetName()},
		Item:     item,
		Fetch:    plan.Fetch,
		Drop:     plan.Drop,
	})
	if err != nil {
		return "", nil, err
	}
	env := &events.Envelope{
		ID:     schema.MsgIDForArtworkFetch(obj.GetUID(), plan.Hash()),
		Type:   "catalog.ArtworkFetchTask",
		Schema: name,
		Source: "catalog@" + version.String(),
		// The <namespace>/<name> routing key every catalog worker parses;
		// the subject carries the tokenised media key instead.
		Key:  obj.GetNamespace() + "/" + obj.GetName(),
		Time: now.UTC(),
		Data: data,
	}
	return events.WorkArtworkFetchSubject(events.MediaKey(string(kind), obj.GetNamespace(), obj.GetName())), env, nil
}
