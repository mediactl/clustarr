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

// Package itemread is the manager's cache reads of a grab or search target
// (ADR-0019 §6.6, §7.3, §7.4): what the grab stage, the search stage and
// the Search controller each need to know about an item beside its own
// status -- its grab configuration (quality profile, delay profile, tags,
// its own or its container's), its current file as pkg/decision reads it,
// its grab owner and the entries covering it, whether it and its container
// are monitored, whether it is out, and the namespace's DownloadClients and
// Indexers. The three read one item through this one code, so the grab
// planner, the search planner and an interactive Search's results agree on
// what the item has (the rule app/catalog/worker/search's snapshot and the
// RSS matcher's resolve kept before the decisions moved).
//
// It reads through the reader it is given -- the manager's cache -- and
// writes nothing. The agents never link it.
package itemread

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/delay"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// Kind is an item object's Kind ("Movie", "Episode", …), "" for any other
// object.
func Kind(o client.Object) string {
	switch o.(type) {
	case *catalogv1alpha1.Movie:
		return "Movie"
	case *catalogv1alpha1.Episode:
		return "Episode"
	case *catalogv1alpha1.Series:
		return "Series"
	case *catalogv1alpha1.Album:
		return "Album"
	case *catalogv1alpha1.Book:
		return "Book"
	case *catalogv1alpha1.Audiobook:
		return "Audiobook"
	case *catalogv1alpha1.Issue:
		return "Issue"
	case *catalogv1alpha1.Comic:
		return "Comic"
	}
	return ""
}

// MediaKind is an item object's commonv1.MediaKind, "" for any other object.
func MediaKind(o client.Object) commonv1.MediaKind {
	return commonv1.MediaKind(strings.ToLower(Kind(o)))
}

// Ref is o's schema.ItemRef.
func Ref(o client.Object) schema.ItemRef {
	return schema.ItemRef{Kind: Kind(o), Ref: schema.Ref{Namespace: o.GetNamespace(), Name: o.GetName(), UID: string(o.GetUID())}}
}

// New is an empty object of an item Kind ("Movie", "movie"), false for any
// other kind.
func New(kind string) (client.Object, bool) {
	switch strings.ToLower(kind) {
	case "movie":
		return &catalogv1alpha1.Movie{}, true
	case "episode":
		return &catalogv1alpha1.Episode{}, true
	case "series":
		return &catalogv1alpha1.Series{}, true
	case "album":
		return &catalogv1alpha1.Album{}, true
	case "book":
		return &catalogv1alpha1.Book{}, true
	case "audiobook":
		return &catalogv1alpha1.Audiobook{}, true
	case "issue":
		return &catalogv1alpha1.Issue{}, true
	case "comic":
		return &catalogv1alpha1.Comic{}, true
	}
	return nil, false
}

// Config is the grab configuration governing an item: the QualityProfile it
// ranks under, and the DelayProfile ref and tags that select its delay.
type Config struct {
	QualityProfileRef string
	DelayProfileRef   *string
	Tags              []string
}

// ConfigOf reads item's Config: a Movie's, Series', Audiobook's or Comic's
// own; an Episode's Series'; an Issue's Comic's; an Album's own profile
// override, else its Artist's, with the Artist's delay ref and tags; a
// Book's likewise from its Author (a standalone Book has its own profile
// and no delay ref). A container that is gone leaves what it would have
// given empty.
func ConfigOf(ctx context.Context, r client.Reader, item client.Object) (Config, error) {
	ns := item.GetNamespace()
	switch t := item.(type) {
	case *catalogv1alpha1.Movie:
		return Config{QualityProfileRef: t.Spec.QualityProfileRef, DelayProfileRef: t.Spec.DelayProfileRef, Tags: t.Spec.Tags}, nil
	case *catalogv1alpha1.Series:
		return Config{QualityProfileRef: t.Spec.QualityProfileRef, DelayProfileRef: t.Spec.DelayProfileRef, Tags: t.Spec.Tags}, nil
	case *catalogv1alpha1.Episode:
		var s catalogv1alpha1.Series
		if err := get(ctx, r, ns, t.Spec.SeriesRef, &s); err != nil {
			return Config{}, err
		}
		return Config{QualityProfileRef: s.Spec.QualityProfileRef, DelayProfileRef: s.Spec.DelayProfileRef, Tags: s.Spec.Tags}, nil
	case *catalogv1alpha1.Audiobook:
		return Config{QualityProfileRef: t.Spec.QualityProfileRef, DelayProfileRef: t.Spec.DelayProfileRef, Tags: t.Spec.Tags}, nil
	case *catalogv1alpha1.Comic:
		return Config{QualityProfileRef: t.Spec.QualityProfileRef, DelayProfileRef: t.Spec.DelayProfileRef, Tags: t.Spec.Tags}, nil
	case *catalogv1alpha1.Issue:
		var co catalogv1alpha1.Comic
		if err := get(ctx, r, ns, t.Spec.ComicRef, &co); err != nil {
			return Config{}, err
		}
		return Config{QualityProfileRef: co.Spec.QualityProfileRef, DelayProfileRef: co.Spec.DelayProfileRef, Tags: co.Spec.Tags}, nil
	case *catalogv1alpha1.Album:
		var artist catalogv1alpha1.Artist
		if err := get(ctx, r, ns, t.Spec.ArtistRef, &artist); err != nil {
			return Config{}, err
		}
		return Config{
			QualityProfileRef: ptr.Deref(t.Spec.QualityProfileRef, artist.Spec.QualityProfileRef),
			DelayProfileRef:   artist.Spec.DelayProfileRef, Tags: artist.Spec.Tags,
		}, nil
	case *catalogv1alpha1.Book:
		cfg := Config{QualityProfileRef: ptr.Deref(t.Spec.QualityProfileRef, "")}
		name := ptr.Deref(t.Spec.AuthorRef, "")
		if name == "" {
			return cfg, nil
		}
		var author catalogv1alpha1.Author
		if err := get(ctx, r, ns, name, &author); err != nil {
			return Config{}, err
		}
		if cfg.QualityProfileRef == "" {
			cfg.QualityProfileRef = author.Spec.QualityProfileRef
		}
		cfg.DelayProfileRef, cfg.Tags = author.Spec.DelayProfileRef, author.Spec.Tags
		return cfg, nil
	}
	return Config{}, fmt.Errorf("itemread: %T is not an item", item)
}

// get reads ns/name into o; a missing object (or an empty name) leaves o
// empty and is no error, since what it would have given is simply unset.
func get(ctx context.Context, r client.Reader, ns, name string, o client.Object) error {
	if name == "" {
		return nil
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, o); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("itemread: get %T %s/%s: %w", o, ns, name, err)
	}
	return nil
}

// ErrNoProfile is Profile's answer for an item with no profile ref, or one
// naming a QualityProfile that does not exist or does not resolve.
var ErrNoProfile = errors.New("itemread: no usable quality profile")

// Profile resolves the cluster-scoped QualityProfile name against the loaded
// TRaSH catalogue.
func Profile(ctx context.Context, r client.Reader, name string) (*quality.Profile, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: the item names none", ErrNoProfile)
	}
	var qp catalogv1alpha1.QualityProfile
	if err := r.Get(ctx, client.ObjectKey{Name: name}, &qp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: QualityProfile %q not found", ErrNoProfile, name)
		}
		return nil, fmt.Errorf("itemread: get QualityProfile %q: %w", name, err)
	}
	p, errs := quality.FromCRD(&qp, catalogue.LoadedCatalogue())
	if len(errs) > 0 {
		return nil, fmt.Errorf("%w: QualityProfile %q: %w", ErrNoProfile, name, errs[0])
	}
	return &p, nil
}

// Delay is the DelayProfile spec governing cfg in ns (spec §8.2's order,
// app/catalog/delay.Resolve): the zero spec -- no delay -- when no profile
// matches, as an operator who removed the chart's catch-all meant.
func Delay(ctx context.Context, r client.Reader, ns string, cfg Config) (catalogv1alpha1.DelayProfileSpec, error) {
	var list catalogv1alpha1.DelayProfileList
	if err := r.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return catalogv1alpha1.DelayProfileSpec{}, fmt.Errorf("itemread: list DelayProfiles: %w", err)
	}
	dp, err := delay.Resolve(cfg.DelayProfileRef, cfg.Tags, list.Items)
	if err != nil {
		return catalogv1alpha1.DelayProfileSpec{}, nil
	}
	return dp.Spec, nil
}

// CurrentOf is mf as pkg/decision's Current: quality, revision, format score
// and matched formats from the spec frozen at import (spec §8.4), the
// transcoded verdict (rollup.Transcoded, the one place the rule lives), the
// probed audio languages, video codec and HDR format, and the source title
// and info hash frozen at import (ADR-0019 §6.2).
func CurrentOf(mf *catalogv1alpha1.MediaFile) *decision.Current {
	if mf == nil {
		return nil
	}
	cur := &decision.Current{
		Quality:        mf.Spec.Quality,
		Revision:       mf.Spec.Revision,
		FormatScore:    int(mf.Spec.FormatScore),
		Formats:        mf.Spec.MatchedFormats,
		Transcoded:     rollup.Transcoded(mf),
		AudioLanguages: rollup.ProbedAudioLanguages(mf),
	}
	if mi := mf.Status.MediaInfo; mi != nil {
		cur.VideoCodec, cur.HDR = mi.VideoCodec, mi.Hdr
	}
	if from := mf.Spec.ImportedFrom; from != nil {
		cur.SourceTitle = from.ReleaseTitle
		cur.SourceHash = from.InfoHash
	}
	return cur
}

// Current reads the file item already has as pkg/decision's Current, nil for
// none: a Movie's, Episode's, Book's or Issue's one MediaFile (fileRef); an
// Album's or Audiobook's status.quality rollup, since each is several
// MediaFiles and the rollup is what an upgrade has to beat. A fileRef whose
// MediaFile is gone reads as no file (the rollup has not caught up with a
// deletion; deciding as if there were no file can only approve an upgrade).
func Current(ctx context.Context, r client.Reader, item client.Object) (*decision.Current, error) {
	var (
		hasFile bool
		ref     *string
	)
	switch t := item.(type) {
	case *catalogv1alpha1.Movie:
		hasFile, ref = t.Status.HasFile, t.Status.FileRef
	case *catalogv1alpha1.Episode:
		hasFile, ref = t.Status.HasFile, t.Status.FileRef
	case *catalogv1alpha1.Book:
		hasFile, ref = t.Status.HasFile, t.Status.FileRef
	case *catalogv1alpha1.Issue:
		hasFile, ref = t.Status.HasFile, t.Status.FileRef
	case *catalogv1alpha1.Album:
		if t.Status.Quality != nil {
			return &decision.Current{Quality: *t.Status.Quality, FormatScore: int(t.Status.FormatScore)}, nil
		}
		return nil, nil
	case *catalogv1alpha1.Audiobook:
		if t.Status.HasFile && t.Status.Quality != nil {
			return &decision.Current{Quality: *t.Status.Quality}, nil
		}
		return nil, nil
	default:
		return nil, nil
	}
	if !hasFile || ref == nil || *ref == "" {
		return nil, nil
	}
	var mf catalogv1alpha1.MediaFile
	if err := r.Get(ctx, client.ObjectKey{Namespace: item.GetNamespace(), Name: *ref}, &mf); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("itemread: get MediaFile %s: %w", *ref, err)
	}
	return CurrentOf(&mf), nil
}

// Owner is item's grab owner (ADR-0019 §6.1): an Episode's Series, an
// Issue's Comic, every other item itself. nil when the container is gone.
func Owner(ctx context.Context, r client.Reader, item client.Object) (client.Object, error) {
	ns := item.GetNamespace()
	var (
		name string
		o    client.Object
	)
	switch t := item.(type) {
	case *catalogv1alpha1.Episode:
		name, o = t.Spec.SeriesRef, &catalogv1alpha1.Series{}
	case *catalogv1alpha1.Issue:
		name, o = t.Spec.ComicRef, &catalogv1alpha1.Comic{}
	default:
		return item, nil
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, o); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("itemread: get the owner of %s: %w", item.GetName(), err)
	}
	return o, nil
}

// Covers reports whether entry e on item's owner covers item: an Episode's
// season and number, an Issue's number, every other item always (its owner
// is itself).
func Covers(e *catalogv1alpha1.DownloadEntry, item client.Object) bool {
	switch t := item.(type) {
	case *catalogv1alpha1.Episode:
		return rollup.CoversEpisode(e, t.Name, t.Spec.SeasonNumber, t.Spec.EpisodeNumber)
	case *catalogv1alpha1.Issue:
		return rollup.CoversIssue(e, t.Name, t.Spec.Number)
	}
	return true
}

// OwnMonitored is item's own spec.monitored (default true), its container's
// left out.
func OwnMonitored(item client.Object) bool {
	switch t := item.(type) {
	case *catalogv1alpha1.Movie:
		return ptr.Deref(t.Spec.Monitored, true)
	case *catalogv1alpha1.Series:
		return ptr.Deref(t.Spec.Monitored, true)
	case *catalogv1alpha1.Comic:
		return ptr.Deref(t.Spec.Monitored, true)
	case *catalogv1alpha1.Album:
		return ptr.Deref(t.Spec.Monitored, true)
	case *catalogv1alpha1.Book:
		return ptr.Deref(t.Spec.Monitored, true)
	case *catalogv1alpha1.Audiobook:
		return ptr.Deref(t.Spec.Monitored, true)
	case *catalogv1alpha1.Episode:
		return ptr.Deref(t.Spec.Monitored, true)
	case *catalogv1alpha1.Issue:
		return ptr.Deref(t.Spec.Monitored, true)
	}
	return false
}

// Monitored reports item monitored, and an Episode's Series or an Issue's
// Comic monitored too (Sonarr's MonitoredEpisodeSpecification, P2).
func Monitored(ctx context.Context, r client.Reader, item client.Object) (bool, error) {
	ns := item.GetNamespace()
	switch t := item.(type) {
	case *catalogv1alpha1.Episode:
		if !ptr.Deref(t.Spec.Monitored, true) {
			return false, nil
		}
		var s catalogv1alpha1.Series
		if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: t.Spec.SeriesRef}, &s); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return ptr.Deref(s.Spec.Monitored, true), nil
	case *catalogv1alpha1.Issue:
		if !ptr.Deref(t.Spec.Monitored, true) {
			return false, nil
		}
		var co catalogv1alpha1.Comic
		if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: t.Spec.ComicRef}, &co); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return ptr.Deref(co.Spec.Monitored, true), nil
	}
	return OwnMonitored(item), nil
}

// Available is §8.2's availability input: a Movie's status.available; an
// Episode's air date, else its phase (a blank phase with no air date is not
// available, so an unaired episode's fakes are never grabbed); an Album's,
// Book's or Audiobook's release date and an Issue's cover date, an unknown
// date counting as out (Lidarr, Readarr and Mylar decide undated ones).
func Available(item client.Object, now time.Time) bool {
	switch t := item.(type) {
	case *catalogv1alpha1.Movie:
		return t.Status.Available
	case *catalogv1alpha1.Episode:
		if t.Status.AirDate != nil {
			return !t.Status.AirDate.After(now)
		}
		return t.Status.Phase != "" && t.Status.Phase != catalogv1alpha1.EpisodePhaseUnaired
	case *catalogv1alpha1.Album:
		return t.Status.Metadata == nil || releasedBy(t.Status.Metadata.ReleaseDate, now)
	case *catalogv1alpha1.Book:
		return t.Status.Metadata == nil || releasedBy(t.Status.Metadata.ReleaseDate, now)
	case *catalogv1alpha1.Audiobook:
		return t.Status.Metadata == nil || releasedBy(t.Status.Metadata.ReleaseDate, now)
	case *catalogv1alpha1.Issue:
		return releasedBy(t.Status.Date, now)
	}
	return true
}

func releasedBy(d *metav1.Time, now time.Time) bool { return d == nil || !d.After(now) }

// OriginalLanguage is item's original language tag, BCP-47 verbatim: a
// Movie's metadata, an Episode's or Series' Series metadata; "" otherwise.
func OriginalLanguage(ctx context.Context, r client.Reader, item client.Object) string {
	switch t := item.(type) {
	case *catalogv1alpha1.Movie:
		if t.Status.Metadata != nil {
			return t.Status.Metadata.OriginalLanguage
		}
	case *catalogv1alpha1.Series:
		if t.Status.Metadata != nil {
			return t.Status.Metadata.OriginalLanguage
		}
	case *catalogv1alpha1.Episode:
		var s catalogv1alpha1.Series
		if err := r.Get(ctx, client.ObjectKey{Namespace: t.Namespace, Name: t.Spec.SeriesRef}, &s); err == nil && s.Status.Metadata != nil {
			return s.Status.Metadata.OriginalLanguage
		}
	}
	return ""
}

// TranscodeExcluded reports whether item, or an Episode's Series, carries
// transcode.clustarr.io/exclude: "true" (catalogv1alpha1.AnnotationTranscodeExclude).
func TranscodeExcluded(ctx context.Context, r client.Reader, item client.Object) bool {
	if item.GetAnnotations()[catalogv1alpha1.AnnotationTranscodeExclude] == "true" {
		return true
	}
	if e, ok := item.(*catalogv1alpha1.Episode); ok {
		var s catalogv1alpha1.Series
		if err := r.Get(ctx, client.ObjectKey{Namespace: e.Namespace, Name: e.Spec.SeriesRef}, &s); err == nil {
			return s.Annotations[catalogv1alpha1.AnnotationTranscodeExclude] == "true"
		}
	}
	return false
}

// Protocols is the protocols of ns's enabled DownloadClients (P7).
func Protocols(ctx context.Context, r client.Reader, ns string) (map[commonv1.Protocol]bool, error) {
	var list downloadv1alpha1.DownloadClientList
	if err := r.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("itemread: list DownloadClients: %w", err)
	}
	out := map[commonv1.Protocol]bool{}
	for i := range list.Items {
		if c := &list.Items[i]; ptr.Deref(c.Spec.Enabled, true) {
			out[c.Spec.Protocol] = true
		}
	}
	return out, nil
}

// Indexers lists ns's Indexers.
func Indexers(ctx context.Context, r client.Reader, ns string) ([]indexv1alpha1.Indexer, error) {
	var list indexv1alpha1.IndexerList
	if err := r.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("itemread: list Indexers: %w", err)
	}
	return list.Items, nil
}

// Priorities maps each Indexer's name to its spec.priority (P9: the search
// agent ranks by it).
func Priorities(idxs []indexv1alpha1.Indexer) map[string]int32 {
	out := make(map[string]int32, len(idxs))
	for i := range idxs {
		out[idxs[i].Name] = idxs[i].Spec.Priority
	}
	return out
}

// Queue is pkg/decision's queue for item: the live entries on its owner
// covering it, of purpose (an audio donor is no video candidate, so an
// upgrade never waits on a dub, nor a dub on an upgrade).
func Queue(entries []catalogv1alpha1.DownloadEntry, item client.Object, purpose commonv1.DownloadPurpose) []decision.Queued {
	var out []decision.Queued
	for i := range entries {
		e := &entries[i]
		if !rollup.EntryNonTerminal(e) || e.Purpose != purpose || !Covers(e, item) {
			continue
		}
		out = append(out, quality.Candidate{Quality: e.Release.Quality, Revision: e.Release.Revision, FormatScore: int(e.Release.FormatScore)})
	}
	return out
}

// Tombstoned reports whether a Blocklisted entry on the owner blocks rel
// for item: the same info hash, or the same indexer and GUID (ADR-0019
// §6.14, "no regrab in the gap").
func Tombstoned(entries []catalogv1alpha1.DownloadEntry, item client.Object, rel commonv1.ReleaseInfo) bool {
	for i := range entries {
		e := &entries[i]
		if e.Phase != commonv1.DownloadPhaseBlocklisted || !Covers(e, item) {
			continue
		}
		if rel.InfoHash != "" && strings.EqualFold(e.Release.InfoHash, rel.InfoHash) {
			return true
		}
		if rel.GUID != "" && e.Release.GUID == rel.GUID && e.Release.IndexerRef == rel.IndexerRef {
			return true
		}
	}
	return false
}

// SearchDispatch is a searchable item's status.searchDispatch (ruling R12:
// Movie, Episode, Album, Book, Audiobook, Issue), nil for any other.
func SearchDispatch(o client.Object) *catalogv1alpha1.Dispatch {
	switch t := o.(type) {
	case *catalogv1alpha1.Movie:
		return t.Status.SearchDispatch
	case *catalogv1alpha1.Episode:
		return t.Status.SearchDispatch
	case *catalogv1alpha1.Album:
		return t.Status.SearchDispatch
	case *catalogv1alpha1.Book:
		return t.Status.SearchDispatch
	case *catalogv1alpha1.Audiobook:
		return t.Status.SearchDispatch
	case *catalogv1alpha1.Issue:
		return t.Status.SearchDispatch
	}
	return nil
}

// Searchable reports a kind the search planner dispatches for (R12, P1): a
// Series or a Comic is never searched itself, its Episodes and Issues are.
func Searchable(kind string) bool {
	switch strings.ToLower(kind) {
	case "movie", "episode", "album", "book", "audiobook", "issue":
		return true
	}
	return false
}

// HasDonorAttempts reports a kind with status.donorSearchAttempts (Movie,
// Episode): the kinds an audio donor is searched for.
func HasDonorAttempts(kind string) bool {
	switch strings.ToLower(kind) {
	case "movie", "episode":
		return true
	}
	return false
}

// MovieIdentity is the decision.Identity of one Movie: every title it is
// known by, its year, and its tmdb and imdb ids.
//
// Search, the RSS matcher (search.MovieIdentity) and import's runtime rule
// all read it, so they agree on what the item IS. The tmdb id comes from
// spec, not metadata, so a movie whose metadata has not landed is still
// identifiable by id.
func MovieIdentity(m *catalogv1alpha1.Movie) decision.Identity {
	id := decision.Identity{IDs: map[string]string{}}
	if m.Spec.TmdbID != 0 {
		id.IDs[commonv1.IDKeyTMDB] = strconv.FormatInt(m.Spec.TmdbID, 10)
	}
	md := m.Status.Metadata
	if md == nil {
		return id
	}
	id.Titles = appendUniqueTitles(id.Titles, md.Title, md.OriginalTitle)
	id.Titles = appendUniqueTitles(id.Titles, md.AlternateTitles...)
	id.Year = int(md.Year)
	// Radarr's own rule is "Year or SecondaryYear" (ruling R-7): a release
	// named by the festival year of a film dated by its general release is
	// the same film, however far apart the two sit.
	id.SecondaryYear = int(md.SecondaryYear)
	if v := md.ExternalIDs[commonv1.IDKeyIMDB]; v != "" {
		id.IDs[commonv1.IDKeyIMDB] = v
	}
	return id
}

// appendUniqueTitles is dst plus each non-empty title it does not hold.
func appendUniqueTitles(dst []string, titles ...string) []string {
	for _, t := range titles {
		if t != "" && !slices.Contains(dst, t) {
			dst = append(dst, t)
		}
	}
	return dst
}
