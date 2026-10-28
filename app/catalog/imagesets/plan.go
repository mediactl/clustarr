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

package imagesets

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// Timings.
const (
	// DefaultRefresh is how often an item's choice is reconsidered when the
	// provider names no refresh.
	DefaultRefresh = 7 * 24 * time.Hour
	// ErrorRetry is how long a failed check stands before the item is asked
	// again.
	ErrorRetry = time.Hour
	// RequestTimeout is how long a request stands unanswered before the
	// loop asks again.
	RequestTimeout = time.Hour
	// MaxMessage is ImageSetStatus.Message's MaxLength.
	MaxMessage = 512
	// MaxSeasonArtwork is ImageSetStatus.SeasonArtwork's MaxItems.
	MaxSeasonArtwork = 1000
	// MaxSets is ImageSetStatus.Sets' MaxItems.
	MaxSets = 32
)

// Input is what Plan decides from.
type Input struct {
	Item   schema.ItemRef
	Inputs schema.ImageSetInputs
	Mode   catalogv1alpha1.ImageSetMode
	// ProviderReady is whether a mediux MetadataProvider is enabled and
	// Ready; Refresh is its refresh, DefaultRefresh when zero.
	ProviderReady bool
	Refresh       time.Duration
	Prev          *catalogv1alpha1.ImageSetStatus
	Record        *schema.ImageSetRecord
	RecordRev     uint64
	RecordOK      bool
	Now           time.Time
}

// Decision is what Plan decided.
type Decision struct {
	// ImageSet is the block to apply; Prev when nothing changed. Clear
	// releases it (spec.imageSet: none).
	ImageSet *catalogv1alpha1.ImageSetStatus
	Clear    bool
	// Request is written at Input.RecordRev, then published; Republish is
	// an unclaimed request published again under the same Msg-Id. Resume
	// is a deferred record whose time has come: its task published again
	// under ResumeMessage's Msg-Id (the worker defers and acks; the loop
	// owns the republish, ADR-0019 §5.6).
	Request   *schema.ImageSetRecord
	Republish *schema.ImageSetRecord
	Resume    *schema.ImageSetRecord
	// Answered reports an answer incorporated this pass: the dispatch is
	// done.
	Answered bool
	Due      time.Time
}

// Askable reports whether in names the item to a provider: a movie by its
// TMDB id, a series by its TMDB or TheTVDB id.
func Askable(in schema.ImageSetInputs) bool {
	if in.SetID != "" {
		return in.Kind == string(commonv1.MediaKindMovie) || in.Kind == string(commonv1.MediaKindSeries)
	}
	switch commonv1.MediaKind(in.Kind) {
	case commonv1.MediaKindMovie:
		return in.TMDBID != ""
	case commonv1.MediaKindSeries:
		return in.TMDBID != "" || in.TVDBID != ""
	}
	return false
}

// Plan decides one pass: an opted-out item's status is cleared; with no
// provider nothing is asked and a stored choice stands; an answer for the
// item's ids newer than the stored check is incorporated; a check older
// than the refresh (or ErrorRetry after a failure) asks again, waiting out
// a request in flight.
func Plan(in Input) Decision {
	d := Decision{ImageSet: in.Prev}
	if in.Mode == catalogv1alpha1.ImageSetNone {
		if in.Prev != nil {
			d.ImageSet, d.Clear = nil, true
		}
		return d
	}
	if !in.ProviderReady || !Askable(in.Inputs) {
		return d
	}
	refresh := in.Refresh
	if refresh <= 0 {
		refresh = DefaultRefresh
	}
	var rec schema.ImageSetRecord
	if in.RecordOK && in.Record != nil {
		rec = *in.Record
	}
	if in.RecordOK && answeredFor(rec, in.Inputs, in.Prev) {
		d.ImageSet, d.Answered = incorporate(rec, in.Prev), true
		d.Due = nextCheck(d.ImageSet, refresh)
		return d
	}
	// A stored choice stands until its refresh -- unless the record shows
	// it answered other inputs (a pin set or changed, an id learned), which
	// is asked again at once.
	if in.Prev != nil && (!in.RecordOK || rec.Inputs.SameItem(in.Inputs)) {
		if at := nextCheck(in.Prev, refresh); in.Now.Before(at) {
			d.Due = at
			return d
		}
	}
	if in.RecordOK && rec.Inputs.SameItem(in.Inputs) && rec.State == records.StateRequested &&
		in.Now.Before(rec.RequestedAt.Add(RequestTimeout)) {
		d.Due = rec.RequestedAt.Add(RequestTimeout)
		if rec.ClaimedAt == nil && in.Now.Sub(rec.RequestedAt) < records.RepublishWindow {
			r := rec
			d.Republish = &r
		}
		return d
	}
	if in.RecordOK && rec.Inputs.SameItem(in.Inputs) && rec.State == records.StateDeferred && rec.DeferredUntil != nil &&
		in.Now.Before(rec.DeferredUntil.Add(RequestTimeout)) {
		// A MediUX limit's reset: wait for it, then republish the task for
		// records.RepublishWindow; the request timeout asks afresh.
		d.Due = rec.DeferredUntil.Add(RequestTimeout)
		switch until := *rec.DeferredUntil; {
		case in.Now.Before(until):
			d.Due = until
		case in.Now.Sub(until) < records.RepublishWindow:
			r := rec
			d.Resume = &r
		}
		return d
	}
	req := schema.ImageSetRecord{Inputs: in.Inputs}
	item := in.Item
	req.RecordHeader.Schema, req.Item, req.State, req.RequestedAt = RecordSchema, &item, records.StateRequested, in.Now.UTC()
	req.Seq = records.NextSeq(rec.Seq, 0, in.Now)
	d.Request, d.Due = &req, in.Now.Add(RequestTimeout)
	return d
}

// nextCheck is when prev is reconsidered: ErrorRetry after a failed check,
// else the refresh.
func nextCheck(prev *catalogv1alpha1.ImageSetStatus, refresh time.Duration) time.Time {
	if prev.Message != "" {
		return prev.CheckedAt.Add(ErrorRetry)
	}
	return prev.CheckedAt.Add(refresh)
}

// answeredFor reports a worker's terminal answer for the item in's ids
// name, newer than prev's check.
func answeredFor(rec schema.ImageSetRecord, in schema.ImageSetInputs, prev *catalogv1alpha1.ImageSetStatus) bool {
	if !records.TerminalAnswered(rec.State) || !rec.Inputs.SameItem(in) || rec.AnsweredAt == nil {
		return false
	}
	at := rec.AnsweredAt.UTC().Truncate(time.Second)
	return prev == nil || at.After(prev.CheckedAt.Time)
}

// incorporate is status.imageSet from rec: a found set's images, a
// NotFound, or a failure keeping prev's images with the failure as the
// message.
func incorporate(rec schema.ImageSetRecord, prev *catalogv1alpha1.ImageSetStatus) *catalogv1alpha1.ImageSetStatus {
	at := metav1.NewTime(rec.AnsweredAt.UTC().Truncate(time.Second))
	if rec.State == records.StateFailed || rec.Answer == nil {
		out := &catalogv1alpha1.ImageSetStatus{CheckedAt: at, Message: clamp(rec.Failure, MaxMessage)}
		if out.Message == "" {
			out.Message = "the worker answered nothing"
		}
		if prev != nil {
			kept := prev.DeepCopy()
			kept.CheckedAt, kept.Message = at, out.Message
			return kept
		}
		return out
	}
	ans := rec.Answer
	out := &catalogv1alpha1.ImageSetStatus{Provider: ans.Provider, CheckedAt: at, NotFound: ans.NotFound}
	if ans.NotFound {
		return out
	}
	out.SetID, out.Title, out.Creator, out.URL = ans.SetID, clamp(ans.Title, 256), clamp(ans.Creator, 128), ans.URL
	if ans.UpdatedAt != nil {
		t := metav1.NewTime(ans.UpdatedAt.UTC().Truncate(time.Second))
		out.UpdatedAt = &t
	}
	out.Artwork, out.SeasonArtwork = overrides(ans.Images)
	if len(out.SeasonArtwork) > MaxSeasonArtwork {
		out.SeasonArtwork = out.SeasonArtwork[:MaxSeasonArtwork]
	}
	for _, src := range ans.Sets {
		if src.SetID == "" || len(src.SetID) > 64 || len(out.Sets) == MaxSets {
			continue
		}
		u := src.URL
		if len(u) > 2048 {
			u = ""
		}
		out.Sets = append(out.Sets, catalogv1alpha1.ImageSetSource{
			SetID: src.SetID, Title: clamp(src.Title, 256), Creator: clamp(src.Creator, 128),
			Kind: setKind(src.Kind), URL: u, Images: int32(max(0, src.Images)),
		})
	}
	return out
}

// setKind is a source's kind as the API spells it, "" for one it does not.
func setKind(k string) catalogv1alpha1.ImageSetKind {
	switch kind := catalogv1alpha1.ImageSetKind(k); kind {
	case catalogv1alpha1.ImageSetKindShow, catalogv1alpha1.ImageSetKindMovie, catalogv1alpha1.ImageSetKindCollection:
		return kind
	}
	return ""
}

// overrides maps an answer's images to the catalog's shapes by scope: the
// item's own to spec.artwork's (one per type), a season's and an
// episode's to spec.seasonArtwork's. A specials poster is scope season,
// season 0, never a second item poster, which the status' list map would
// refuse. The mediuxsets package is the agent's copy of this mapping (the
// manager links no pkg/metadata).
func overrides(images []schema.ImageSetImage) (artwork []catalogv1alpha1.ArtworkOverride, seasons []catalogv1alpha1.SeasonArtworkOverride) {
	seen := map[catalogv1alpha1.ImageType]bool{}
	for _, img := range images {
		t := catalogv1alpha1.ImageType(img.Type)
		switch {
		case img.URL == "":
			continue
		case img.Scope == schema.ImageScopeItem:
			if seen[t] {
				continue
			}
			seen[t] = true
			artwork = append(artwork, catalogv1alpha1.ArtworkOverride{Type: t, URL: img.URL})
		case img.Scope == schema.ImageScopeSeason:
			seasons = append(seasons, catalogv1alpha1.SeasonArtworkOverride{Season: img.Season, Type: t, URL: img.URL})
		case img.Scope == schema.ImageScopeEpisode && img.Episode >= 1:
			n := img.Episode
			seasons = append(seasons, catalogv1alpha1.SeasonArtworkOverride{Season: img.Season, Episode: &n, Type: t, URL: img.URL})
		}
	}
	return artwork, seasons
}
