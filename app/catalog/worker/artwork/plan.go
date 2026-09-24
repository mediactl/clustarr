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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	catalogstatus "github.com/mediactl/clustarr/app/catalog/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/overlay"
	"github.com/mediactl/clustarr/pkg/version"
)

// ErrNoOverlay marks an object or kind that carries no status.overlay:
// anything but Movie and Series (spec §B.6).
var ErrNoOverlay = errors.New("artwork: kind has no overlay")

// Reasons a RenderOverlayTask carries (schema.RenderOverlayTask.Reason).
// The gateway publishes "original"; the OverlayProfile controller publishes
// ReasonProfile. The renderer reads neither: every task is judged against
// the item's current inputs, whatever it says (§C.6).
const ReasonProfile = "profile"

// Overlaid reports whether kind carries an overlay: Movie and Series
// (catalogstatus.HasOverlay, the one statement of the rule).
func Overlaid(kind commonv1.MediaKind) bool {
	return catalogstatus.HasOverlay(kind)
}

// Item is a Movie or Series as the renderer and the OverlayProfile
// controller both read it.
type Item struct {
	Kind   commonv1.MediaKind
	Object client.Object

	// Ratings is status.metadata.ratings, nil before the first fetch.
	Ratings []catalogv1alpha1.Rating

	// PosterDigest is status.artwork's poster entry's digest, "" when the
	// gateway has recorded no poster. The renderer judges by the stored
	// object's digest instead; the controller, which never reads the
	// store, by this.
	PosterDigest string

	// Overlay is status.overlay, the renderer's record.
	Overlay *catalogv1alpha1.OverlayEntry
}

// ItemOf reads obj's overlay inputs. Only Movie and Series have any.
func ItemOf(obj client.Object) (Item, error) {
	switch o := obj.(type) {
	case *catalogv1alpha1.Movie:
		it := Item{Kind: commonv1.MediaKindMovie, Object: o, Overlay: o.Status.Overlay, PosterDigest: posterDigest(o.Status.Artwork)}
		if o.Status.Metadata != nil {
			it.Ratings = o.Status.Metadata.Ratings
		}
		return it, nil
	case *catalogv1alpha1.Series:
		it := Item{Kind: commonv1.MediaKindSeries, Object: o, Overlay: o.Status.Overlay, PosterDigest: posterDigest(o.Status.Artwork)}
		if o.Status.Metadata != nil {
			it.Ratings = o.Status.Metadata.Ratings
		}
		return it, nil
	default:
		return Item{}, fmt.Errorf("%w: %T", ErrNoOverlay, obj)
	}
}

// NewObject returns an empty object of kind, ready for a Get.
func NewObject(kind commonv1.MediaKind) (client.Object, error) {
	switch kind {
	case commonv1.MediaKindMovie:
		return &catalogv1alpha1.Movie{}, nil
	case commonv1.MediaKindSeries:
		return &catalogv1alpha1.Series{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrNoOverlay, kind)
	}
}

func posterDigest(entries []catalogv1alpha1.ArtworkEntry) string {
	for _, e := range entries {
		if e.Type == catalogv1alpha1.ImageTypePoster {
			return e.Digest
		}
	}
	return ""
}

// EffectiveKinds is spec.kinds, or both kinds when unset.
func EffectiveKinds(spec catalogv1alpha1.OverlayProfileSpec) []commonv1.MediaKind {
	if len(spec.Kinds) == 0 {
		return []commonv1.MediaKind{commonv1.MediaKindMovie, commonv1.MediaKindSeries}
	}
	return spec.Kinds
}

// EffectiveBadges is spec.badges, or the one metacritic badge the field's
// doc comment promises when unset.
func EffectiveBadges(spec catalogv1alpha1.OverlayProfileSpec) []catalogv1alpha1.OverlayBadge {
	if len(spec.Badges) == 0 {
		return []catalogv1alpha1.OverlayBadge{{Source: catalogv1alpha1.RatingSourceMetacritic}}
	}
	return spec.Badges
}

// ProfileHash is OverlayProfile.status.hash: every render field of spec,
// hashed through the same conversion the renderer draws from --
// overlay.TemplateSpec for the corner and geometry, EffectiveBadges for
// what is drawn, in order. The selection fields (selector, kinds) decide
// which items, never what an overlay looks like, and stay out of it.
func ProfileHash(spec catalogv1alpha1.OverlayProfileSpec) string {
	var b strings.Builder
	b.WriteString("template=")
	b.WriteString(overlay.TemplateHash(overlay.TemplateSpec(spec)))
	b.WriteString(";badges=")
	for i, badge := range EffectiveBadges(spec) {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(string(badge.Source))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Selects reports whether p's selector and kinds match it. A nil or
// malformed selector selects nothing -- a typo fails safe into no overlays
// rather than badging a whole library -- and a profile being deleted, or in
// another namespace, selects nothing.
func Selects(p *catalogv1alpha1.OverlayProfile, it Item) bool {
	if p.Spec.Selector == nil || k8s.IsDeleting(p) || p.Namespace != it.Object.GetNamespace() {
		return false
	}
	kindOK := false
	for _, k := range EffectiveKinds(p.Spec) {
		if k == it.Kind {
			kindOK = true
			break
		}
	}
	if !kindOK {
		return false
	}
	sel, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
	if err != nil {
		return false
	}
	return sel.Matches(labels.Set(it.Object.GetLabels()))
}

// Winner is the profile that owns it's overlay: of the profiles that
// select it, the one with the lowest name. Every other selecting profile is
// overlapped for this item (spec §C.4). nil when none selects it.
func Winner(profiles []catalogv1alpha1.OverlayProfile, it Item) *catalogv1alpha1.OverlayProfile {
	var best *catalogv1alpha1.OverlayProfile
	for i := range profiles {
		p := &profiles[i]
		if !Selects(p, it) {
			continue
		}
		if best == nil || p.Name < best.Name {
			best = p
		}
	}
	return best
}

// Badges renders spec's badges for ratings, in the profile's order: a badge
// whose source has no rating -- or one FormatScore omits -- is left out
// (spec §C.6 step 4).
func Badges(spec catalogv1alpha1.OverlayProfileSpec, ratings []catalogv1alpha1.Rating) []overlay.Badge {
	bySource := make(map[catalogv1alpha1.RatingSource]int32, len(ratings))
	for _, r := range ratings {
		bySource[r.Source] = r.ValueCentis
	}
	var out []overlay.Badge
	for _, b := range EffectiveBadges(spec) {
		centis, ok := bySource[b.Source]
		if !ok {
			continue
		}
		score, ok := overlay.FormatScore(string(b.Source), centis)
		if !ok {
			continue
		}
		logo, _ := overlay.Logo(string(b.Source))
		out = append(out, overlay.Badge{Source: string(b.Source), Score: score, Logo: logo})
	}
	return out
}

// RenderVersion is the renderer's own input to every InputsDigest: a
// change to what the renderer draws from the same inputs -- a new layout,
// font, resampling or MaxRenderWidth -- bumps it, and every stored overlay's
// Clustarr-Rendered-From stops matching, so each is re-rendered on its next
// task rather than served stale forever. 1 was the unversioned renderer
// through M7's first cut; 2 downscales originals to MaxRenderWidth.
const RenderVersion = 2

// InputsDigest is spec §C.6 step 2: the hex SHA-256 over the original
// poster's digest, the profile hash and the item's ratings sorted by
// source -- and [RenderVersion], so a renderer upgrade re-renders. A rating
// contributes its source and value; its vote count is never drawn, so it
// stays out -- a count ticking over on every metadata refresh would
// otherwise re-render every poster for no visible change.
func InputsDigest(originalDigest, profileHash string, ratings []catalogv1alpha1.Rating) string {
	return inputsDigest(RenderVersion, originalDigest, profileHash, ratings)
}

func inputsDigest(version int, originalDigest, profileHash string, ratings []catalogv1alpha1.Rating) string {
	sorted := append([]catalogv1alpha1.Rating(nil), ratings...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Source < sorted[j].Source })
	var b strings.Builder
	fmt.Fprintf(&b, "render=%d\noriginal=%s\nprofile=%s\n", version, originalDigest, profileHash)
	for _, r := range sorted {
		fmt.Fprintf(&b, "rating=%s:%d\n", r.Source, r.ValueCentis)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Want is the overlay an item should carry. A nil Profile means none: no
// profile selects the item, no badge has a rating, or there is no original
// poster to draw on.
type Want struct {
	Profile      *catalogv1alpha1.OverlayProfile
	Badges       []overlay.Badge
	InputsDigest string

	// OriginalDigest is the original poster's digest the plan was made
	// from, the one the render must draw on.
	OriginalDigest string
}

// Plan decides it's overlay from its labels and ratings, the profiles in
// its namespace and the digest of its original poster ("" when there is
// none).
func Plan(it Item, profiles []catalogv1alpha1.OverlayProfile, originalDigest string) Want {
	p := Winner(profiles, it)
	if p == nil || originalDigest == "" {
		return Want{}
	}
	badges := Badges(p.Spec, it.Ratings)
	if len(badges) == 0 {
		return Want{}
	}
	return Want{
		Profile: p, Badges: badges, OriginalDigest: originalDigest,
		InputsDigest: InputsDigest(originalDigest, ProfileHash(p.Spec), it.Ratings),
	}
}

// ProfileName is the wanted profile's name, "" for none.
func (w Want) ProfileName() string {
	if w.Profile == nil {
		return ""
	}
	return w.Profile.Name
}

// Same reports whether w and o want the same overlay.
func (w Want) Same(o Want) bool {
	return w.ProfileName() == o.ProfileName() && w.InputsDigest == o.InputsDigest
}

// RecordedBy reports whether status.overlay already records w: none for no
// overlay, else the same profile rendered from the same inputs. The digest
// and the time are the render's own, not something w decides.
func (w Want) RecordedBy(o *catalogv1alpha1.OverlayEntry) bool {
	if w.Profile == nil {
		return o == nil
	}
	return o != nil && o.ProfileRef == w.Profile.Name && o.RenderedFrom == w.InputsDigest
}

// Publish publishes one RenderOverlayTask for it under
// schema.MsgIDForRenderOverlay(uid, token), on
// events.WorkArtworkRenderSubject -- the subject the render consumer reads.
func Publish(ctx context.Context, bus events.Publisher, it Item, token, reason string) error {
	schemaName, data, err := schema.Encode(schema.RenderOverlayTask{
		MediaRef: commonv1.MediaRef{Kind: it.Kind, Name: it.Object.GetName()},
		Reason:   reason,
	})
	if err != nil {
		return err
	}
	env := &events.Envelope{
		ID:     schema.MsgIDForRenderOverlay(it.Object.GetUID(), token),
		Type:   "catalog.RenderOverlayTask",
		Schema: schemaName,
		Source: "catalogarr@" + version.String(),
		Key:    it.Object.GetNamespace() + "/" + it.Object.GetName(),
		Time:   time.Now().UTC(),
		Data:   data,
	}
	mediaKey := events.MediaKey(string(it.Kind), it.Object.GetNamespace(), it.Object.GetName())
	if _, err := bus.Publish(ctx, events.WorkArtworkRenderSubject(mediaKey), env); err != nil {
		return fmt.Errorf("artwork: publish the render task for %s %s/%s: %w",
			it.Kind, it.Object.GetNamespace(), it.Object.GetName(), err)
	}
	return nil
}
