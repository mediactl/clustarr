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

package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// EditRequest is what the item page's Edit dialog saves (Radarr's Edit
// Movie, Sonarr's Edit Series; since the 2026-11-10 design §9.2-§9.5 the
// non-video kinds' own fields): the item's quality profile, whether it is
// monitored, and the fields each kind has. A field left at its zero value
// is not sent, so it keeps its value; a field set on a kind that does not
// have it refuses the request.
type EditRequest struct {
	// QualityProfileRef is the QualityProfile to rank the item against.
	// Required for every editable kind but a Book and an Album, whose
	// profile is an override of their author's or artist's and is sent only
	// when given.
	QualityProfileRef string
	// InheritProfile, on an Album, clears spec.qualityProfileRef so the
	// artist's profile applies again (the dialog's "Artist's" choice,
	// 2026-11-10 design §9.2). Set on any other kind, or beside a
	// QualityProfileRef, the request is refused.
	InheritProfile bool
	// Monitored, when set, is written too.
	Monitored *bool
	// SeriesType is a series' spec.seriesType (standard, daily, anime);
	// empty leaves it. Set on any other kind, the request is refused.
	SeriesType string
	// MinimumAvailability is a movie's spec.minimumAvailability (tba,
	// announced, inCinemas, released); empty leaves it. Set on any other
	// kind, the request is refused.
	MinimumAvailability string
	// Seasons, when set, is a series' whole spec.seasons, sent under
	// ResourceVersion (the series as read): the Edit dialog switching an
	// unmonitored series to monitored monitors every season but the
	// specials ([AllSeasonsMonitored]). Set on any other kind, the request
	// is refused.
	Seasons         []catalogv1alpha1.SeasonSpec
	ResourceVersion string
	// MonitorNewItems is an artist's spec.monitorNewItems (all, none, new)
	// or an author's (all, none); empty leaves it.
	MonitorNewItems string
	// MusicProfile, when set, is an artist's spec.metadataProfile: which
	// MusicBrainz release groups become Albums.
	MusicProfile *MusicProfile
	// BookProfile, when set, is an author's spec.metadataProfile: which
	// Open Library works become Books.
	BookProfile *BookProfile
	// ComicKind and SpecialVersion are a comic's spec.kind (comic, manga)
	// and spec.specialVersion (normal, tpb, oneShot, hardCover, omnibus,
	// volumeAsIssue); MonitorNewIssues, when set, its spec.monitorNewIssues.
	ComicKind        string
	SpecialVersion   string
	MonitorNewIssues *bool
	// Region is an audiobook's spec.region, an Audible marketplace.
	// AuthorRef and BookRef, when set, are its links to an Author and a
	// Book; an empty string clears one.
	Region    string
	AuthorRef *string
	BookRef   *string
	// RootFolderRef is a standalone book's spec.rootFolderRef, sent with
	// its QualityProfileRef (design §9.3: a standalone Book is editable --
	// profile, root, monitored); empty leaves it.
	RootFolderRef string
	// AnyReleaseOk and ReleaseID are an album's release choice (design
	// §9.2: the album bar's Edit): spec.anyReleaseOk, whether a release of
	// the group other than the pinned one is taken when it is the best
	// match, and spec.releaseID, the MusicBrainz release pinned -- an empty
	// string clears the pin, so the manager chooses. Either set on any
	// other kind, the request is refused.
	AnyReleaseOk *bool
	ReleaseID    *string
}

// MusicProfile is an artist's spec.metadataProfile as the Edit dialog
// saves it: each list whole, since a merge patch replaces a list.
type MusicProfile struct {
	PrimaryTypes, SecondaryTypes, ReleaseStatuses []string
}

// BookProfile is an author's spec.metadataProfile as the Edit dialog
// saves it. MinPopularity, a quantity the dialog has no control for, is
// left as it is.
type BookProfile struct {
	AllowedLanguages                                                        []string
	SkipMissingDate, SkipMissingISBN, SkipPartsAndSets, SkipSeriesSecondary bool
	MinPages                                                                int32
}

// editables are the kinds with a quality profile in their spec: the ones
// the Edit dialog is for. An Episode or Issue ranks under its parent's
// profile and has no dialog; a Book carries an optional override of its
// author's -- a standalone book's own (2026-11-10 design §9.3) -- and an
// Album an optional override of its artist's beside its release choice
// (design §9.2, the album bar's Edit).
var editables = []commonv1.MediaKind{
	commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindArtist,
	commonv1.MediaKindAuthor, commonv1.MediaKindComic, commonv1.MediaKindAudiobook, commonv1.MediaKindBook,
	commonv1.MediaKindAlbum,
}

// EditableKinds returns, sorted, every kind [EditItem] accepts.
func EditableKinds() []commonv1.MediaKind {
	kinds := slices.Clone(editables)
	slices.Sort(kinds)
	return kinds
}

// The enumerations the dialog's selects and checkboxes offer, as the CRDs
// spell them (api/catalog/v1alpha1); a value outside one is refused here
// rather than by the apiserver, so the message names the field.
var (
	seriesTypes           = []string{"standard", "daily", "anime"}
	minimumAvailabilities = []string{"tba", "announced", "inCinemas", "released"}
	artistMonitorNew      = []string{"all", "none", "new"}
	authorMonitorNew      = []string{"all", "none"}
	comicKinds            = []string{"comic", "manga"}
	comicSpecialVersions  = []string{"normal", "tpb", "oneShot", "hardCover", "omnibus", "volumeAsIssue"}
	audiobookRegions      = []string{"us", "uk", "ca", "au", "de", "fr", "es", "in", "it", "jp"}
	// MusicBrainz's release-group types and statuses as
	// MusicMetadataProfile spells them (artist_types.go).
	musicPrimaryTypes   = []string{"album", "ep", "single", "broadcast", "other"}
	musicSecondaryTypes = []string{
		"studio", "compilation", "soundtrack", "spokenword", "interview", "audiobook", "live",
		"remix", "djMix", "mixtape", "demo", "audioDrama", "fieldRecording",
	}
	musicReleaseStatuses = []string{"official", "promotion", "bootleg", "pseudoRelease"}
)

// EditChoices are the enumerations the Edit dialog offers, exported so the
// dialog's selects and the action refuse the same values (ui/views reads
// them; TestEditDialogChoicesAreTheActions holds them equal).
type EditChoices struct {
	ArtistMonitorNew, AuthorMonitorNew, ComicKinds, ComicSpecialVersions, AudiobookRegions []string
	MusicPrimaryTypes, MusicSecondaryTypes, MusicReleaseStatuses                           []string
}

// Choices returns the Edit dialog's enumerations.
func Choices() EditChoices {
	return EditChoices{
		ArtistMonitorNew: slices.Clone(artistMonitorNew), AuthorMonitorNew: slices.Clone(authorMonitorNew),
		ComicKinds: slices.Clone(comicKinds), ComicSpecialVersions: slices.Clone(comicSpecialVersions),
		AudiobookRegions:  slices.Clone(audiobookRegions),
		MusicPrimaryTypes: slices.Clone(musicPrimaryTypes), MusicSecondaryTypes: slices.Clone(musicSecondaryTypes),
		MusicReleaseStatuses: slices.Clone(musicReleaseStatuses),
	}
}

// EditItem saves the Edit dialog: one JSON merge patch of the spec leaves
// req names on the catalog item kind/name in namespace, made as
// [FieldManager], returning the object as the apiserver returned it. A
// field req leaves empty is not sent, so it keeps its value; the patch is
// exactly {"spec":{...}} of what is sent. A missing item is the
// apiserver's NotFound, wrapped; nothing is ever created.
func EditItem(
	ctx context.Context, p Patcher, namespace string, kind commonv1.MediaKind, name string, req EditRequest,
) (client.Object, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.EditItem")
	defer span.End()

	if err := validateItem(namespace, kind, name); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	spec, err := editSpec(kind, req)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	body := map[string]any{"spec": spec}
	if req.Seasons != nil {
		if kind != commonv1.MediaKindSeries || req.ResourceVersion == "" {
			err := fmt.Errorf("%w: seasons need a series as read, not a %s", ErrInvalid, kind)
			tracing.RecordError(span, err)
			return nil, err
		}
		spec["seasons"] = req.Seasons
		body["metadata"] = map[string]any{"resourceVersion": req.ResourceVersion}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		err = fmt.Errorf("actions: encode edit patch: %w", err)
		tracing.RecordError(span, err)
		return nil, err
	}

	obj := monitorables[kind].newObject()
	obj.SetNamespace(namespace)
	obj.SetName(name)
	if err := p.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: edit %s %s/%s: %w", kind, namespace, name, err)
		tracing.RecordError(span, err)
		return nil, err
	}

	logging.FromContext(ctx).Info("ui action: item edited",
		"namespace", namespace, "kind", kind, "item", name, "qualityProfile", req.QualityProfileRef)
	return obj, nil
}

// editSpec is the spec half of the merge patch: what req names, each
// field checked against the kind that has it and the values its CRD takes.
func editSpec(kind commonv1.MediaKind, req EditRequest) (map[string]any, error) {
	if !slices.Contains(editables, kind) {
		return nil, fmt.Errorf("%w: a %s has no quality profile of its own", ErrInvalid, kind)
	}
	spec := map[string]any{}
	switch {
	case req.InheritProfile && (kind != commonv1.MediaKindAlbum || req.QualityProfileRef != ""):
		return nil, fmt.Errorf("%w: only an album's profile is inherited, and not beside a profile of its own", ErrInvalid)
	case req.InheritProfile:
		spec["qualityProfileRef"] = nil // a merge patch's null removes the override
	case req.QualityProfileRef != "":
		spec["qualityProfileRef"] = req.QualityProfileRef
	case kind != commonv1.MediaKindBook && kind != commonv1.MediaKindAlbum:
		return nil, fmt.Errorf("%w: a quality profile is required", ErrInvalid)
	}
	if req.Monitored != nil {
		spec["monitored"] = *req.Monitored
	}
	// one is "field, its kind, its allowed values": set on another kind or
	// outside the values, the request is refused.
	type enumField struct {
		name, value string
		kind        commonv1.MediaKind
		allowed     []string
	}
	for _, f := range []enumField{
		{"seriesType", req.SeriesType, commonv1.MediaKindSeries, seriesTypes},
		{"minimumAvailability", req.MinimumAvailability, commonv1.MediaKindMovie, minimumAvailabilities},
		{"kind", req.ComicKind, commonv1.MediaKindComic, comicKinds},
		{"specialVersion", req.SpecialVersion, commonv1.MediaKindComic, comicSpecialVersions},
		{"region", req.Region, commonv1.MediaKindAudiobook, audiobookRegions},
	} {
		if f.value == "" {
			continue
		}
		if kind != f.kind || !slices.Contains(f.allowed, f.value) {
			return nil, fmt.Errorf("%w: %s %q on a %s", ErrInvalid, f.name, f.value, kind)
		}
		spec[f.name] = f.value
	}
	if req.MonitorNewItems != "" {
		allowed := map[commonv1.MediaKind][]string{commonv1.MediaKindArtist: artistMonitorNew, commonv1.MediaKindAuthor: authorMonitorNew}[kind]
		if !slices.Contains(allowed, req.MonitorNewItems) {
			return nil, fmt.Errorf("%w: monitorNewItems %q on a %s", ErrInvalid, req.MonitorNewItems, kind)
		}
		spec["monitorNewItems"] = req.MonitorNewItems
	}
	if req.MusicProfile != nil {
		if kind != commonv1.MediaKindArtist {
			return nil, fmt.Errorf("%w: a music metadata profile on a %s", ErrInvalid, kind)
		}
		mp := map[string]any{}
		for _, l := range []struct {
			name    string
			values  []string
			allowed []string
		}{
			{"primaryTypes", req.MusicProfile.PrimaryTypes, musicPrimaryTypes},
			{"secondaryTypes", req.MusicProfile.SecondaryTypes, musicSecondaryTypes},
			{"releaseStatuses", req.MusicProfile.ReleaseStatuses, musicReleaseStatuses},
		} {
			values := cleanList(l.values)
			// An empty list would let no release group through, which no
			// one means by unticking everything.
			if len(values) == 0 {
				return nil, fmt.Errorf("%w: at least one of metadataProfile.%s", ErrInvalid, l.name)
			}
			for _, v := range values {
				if !slices.Contains(l.allowed, v) {
					return nil, fmt.Errorf("%w: metadataProfile.%s %q", ErrInvalid, l.name, v)
				}
			}
			mp[l.name] = values
		}
		spec["metadataProfile"] = mp
	}
	if req.BookProfile != nil {
		if kind != commonv1.MediaKindAuthor {
			return nil, fmt.Errorf("%w: a book metadata profile on a %s", ErrInvalid, kind)
		}
		if req.BookProfile.MinPages < 0 {
			return nil, fmt.Errorf("%w: metadataProfile.minPages %d", ErrInvalid, req.BookProfile.MinPages)
		}
		mp := map[string]any{
			"skipMissingDate": req.BookProfile.SkipMissingDate, "skipMissingISBN": req.BookProfile.SkipMissingISBN,
			"skipPartsAndSets": req.BookProfile.SkipPartsAndSets, "skipSeriesSecondary": req.BookProfile.SkipSeriesSecondary,
			"minPages": req.BookProfile.MinPages,
		}
		// No language is "every language", which a merge patch spells as
		// null: an empty list would be kept as a list.
		if langs := cleanList(req.BookProfile.AllowedLanguages); len(langs) > 0 {
			mp["allowedLanguages"] = langs
		} else {
			mp["allowedLanguages"] = nil
		}
		spec["metadataProfile"] = mp
	}
	if req.MonitorNewIssues != nil {
		if kind != commonv1.MediaKindComic {
			return nil, fmt.Errorf("%w: monitorNewIssues on a %s", ErrInvalid, kind)
		}
		spec["monitorNewIssues"] = *req.MonitorNewIssues
	}
	for _, ref := range []struct {
		name  string
		value *string
	}{{"authorRef", req.AuthorRef}, {"bookRef", req.BookRef}} {
		if ref.value == nil {
			continue
		}
		if kind != commonv1.MediaKindAudiobook {
			return nil, fmt.Errorf("%w: %s on a %s", ErrInvalid, ref.name, kind)
		}
		if *ref.value == "" {
			spec[ref.name] = nil // a merge patch's null removes the link
		} else {
			spec[ref.name] = *ref.value
		}
	}
	if req.RootFolderRef != "" {
		if kind != commonv1.MediaKindBook {
			return nil, fmt.Errorf("%w: a root folder on a %s; only a standalone book's is edited", ErrInvalid, kind)
		}
		spec["rootFolderRef"] = req.RootFolderRef
	}
	if req.AnyReleaseOk != nil {
		if kind != commonv1.MediaKindAlbum {
			return nil, fmt.Errorf("%w: anyReleaseOk on a %s", ErrInvalid, kind)
		}
		spec["anyReleaseOk"] = *req.AnyReleaseOk
	}
	if req.ReleaseID != nil {
		if kind != commonv1.MediaKindAlbum {
			return nil, fmt.Errorf("%w: a release pin on a %s", ErrInvalid, kind)
		}
		if *req.ReleaseID == "" {
			spec["releaseID"] = nil // a merge patch's null removes the pin
		} else {
			spec["releaseID"] = *req.ReleaseID
		}
	}
	if len(spec) == 0 {
		return nil, fmt.Errorf("%w: nothing to edit", ErrInvalid)
	}
	return spec, nil
}

// cleanList trims each value and drops the empty ones (a form's unticked
// group posts nothing, a hidden marker posts "") and duplicates, keeping
// the order given.
func cleanList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// EditItem is [EditItem] over the Actions' writer.
func (a *Actions) EditItem(
	ctx context.Context, namespace string, kind commonv1.MediaKind, name string, req EditRequest,
) (client.Object, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return EditItem(ctx, a.w, namespace, kind, name, req)
}
