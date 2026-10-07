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

package metadatainputs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/version"
)

// Durable is the consumer a metadata task is admitted on (ruling R23: the
// ledger admits it and forgets it on the answer or after AckWait ×
// MaxDeliver; no status block records it).
const Durable = events.ConsumerCatalogMetadata

// keyMangaDex is pkg/metadata/clients/extid.KeyMangaDex's value, restated so
// this package links no provider client.
const keyMangaDex = "mangadex"

// For is what a refresh of obj asks for: its provider ids (the ids the
// gateway looks it up by), the document schema version this binary wants
// (pkg/metadata.SchemaVersion), and -- while the operator's
// clustarr.io/refresh-metadata annotation stands -- Force with its epoch.
// obj must be one of the eight kinds with a status.metadata of their own;
// any other reads as empty inputs.
func For(obj client.Object) schema.MetadataInputs {
	in := schema.MetadataInputs{ProviderIDs: providerIDs(obj), SchemaVersion: pkgmetadata.SchemaVersion}
	if v, ok := obj.GetAnnotations()[catalogv1alpha1.AnnotationRefreshMetadata]; ok {
		if epoch, err := strconv.ParseInt(v, 10, 64); err == nil && epoch > 0 {
			in.Force, in.RefreshEpoch = true, epoch
		}
	}
	return in
}

// Forced is in with Force set and epoch recorded: the refresher's ask
// (app/catalog/controller/metadatarefresh).
func Forced(in schema.MetadataInputs, epoch int64) schema.MetadataInputs {
	in.Force, in.RefreshEpoch = true, epoch
	return in
}

// providerIDs mirrors the gateway's externalIDs (app/catalog/metadata
// target.go): the spec id each kind is looked up by.
func providerIDs(obj client.Object) map[string]string {
	switch o := obj.(type) {
	case *catalogv1alpha1.Movie:
		return map[string]string{pkgmetadata.KeyTMDB: strconv.FormatInt(o.Spec.TmdbID, 10)}
	case *catalogv1alpha1.Series:
		return map[string]string{pkgmetadata.KeyTVDB: strconv.FormatInt(o.Spec.TvdbID, 10)}
	case *catalogv1alpha1.Artist:
		return map[string]string{pkgmetadata.KeyMBArtist: o.Spec.MusicBrainzID}
	case *catalogv1alpha1.Album:
		return map[string]string{pkgmetadata.KeyMBReleaseGroup: o.Spec.ReleaseGroupID}
	case *catalogv1alpha1.Author:
		return map[string]string{pkgmetadata.KeyOpenLibraryAuthor: o.Spec.OpenLibraryID}
	case *catalogv1alpha1.Book:
		return map[string]string{pkgmetadata.KeyOpenLibraryWork: o.Spec.WorkID}
	case *catalogv1alpha1.Audiobook:
		region := string(o.Spec.Region)
		if region == "" {
			region = "us"
		}
		return map[string]string{pkgmetadata.KeyASIN: o.Spec.ASIN, "region": region}
	case *catalogv1alpha1.Comic:
		if o.Spec.Source == catalogv1alpha1.ComicSourceMangaDex {
			return map[string]string{keyMangaDex: o.Spec.SourceID}
		}
		return map[string]string{pkgmetadata.KeyComicVine: o.Spec.SourceID}
	}
	return nil
}

// Hash is the hex SHA-256 of in's JSON (encoding/json sorts map keys): the
// Msg-Id suffix that makes one task per distinct ask.
func Hash(in schema.MetadataInputs) string {
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// MsgID is a metadata task's Msg-Id, "metadata/<item uid>/<inputsHash>"
// (ADR-0019 plan, Global Constraints): a hot loop that keeps finding the
// item stale publishes once per duplicate window, and a forced refresh,
// whose epoch is in the hash, is never absorbed as a repeat.
func MsgID(uid string, in schema.MetadataInputs) string {
	return "metadata/" + uid + "/" + Hash(in)
}

// Matches reports whether a record answered for answered answers want: the
// same provider ids, language and schema version. Force and RefreshEpoch are
// the ask's urgency, not the answer's identity -- the refresher consumes its
// annotation the moment it publishes, so the item's inputs never still name
// the epoch when the forced answer lands (ruling A5-R1).
func Matches(answered, want schema.MetadataInputs) bool {
	return maps.Equal(answered.ProviderIDs, want.ProviderIDs) &&
		answered.Language == want.Language && answered.SchemaVersion == want.SchemaVersion
}

// RefreshedAt is obj's status.metadata.refreshedAt; zero before the first
// fetch.
func RefreshedAt(obj client.Object) time.Time {
	var t *metav1.Time
	switch o := obj.(type) {
	case *catalogv1alpha1.Movie:
		if o.Status.Metadata != nil {
			t = &o.Status.Metadata.RefreshedAt
		}
	case *catalogv1alpha1.Series:
		if o.Status.Metadata != nil {
			t = &o.Status.Metadata.RefreshedAt
		}
	case *catalogv1alpha1.Artist:
		if o.Status.Metadata != nil {
			t = &o.Status.Metadata.RefreshedAt
		}
	case *catalogv1alpha1.Album:
		if o.Status.Metadata != nil {
			t = &o.Status.Metadata.RefreshedAt
		}
	case *catalogv1alpha1.Author:
		if o.Status.Metadata != nil {
			t = &o.Status.Metadata.RefreshedAt
		}
	case *catalogv1alpha1.Book:
		if o.Status.Metadata != nil {
			t = &o.Status.Metadata.RefreshedAt
		}
	case *catalogv1alpha1.Audiobook:
		if o.Status.Metadata != nil {
			t = &o.Status.Metadata.RefreshedAt
		}
	case *catalogv1alpha1.Comic:
		if o.Status.Metadata != nil {
			t = &o.Status.Metadata.RefreshedAt
		}
	}
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// Incorporable reports whether rec is a document obj takes (§7.1
// "Ordering"): a successful answer to obj's current ask whose refreshedAt is
// after obj's status.metadata.refreshedAt, compared at the second the
// apiserver keeps -- so an older refresh never rolls back a newer one, and
// a document once incorporated is not incorporated again.
func Incorporable(obj client.Object, rec *schema.ItemMetadataRecord) bool {
	if rec == nil || rec.Failure != "" || len(rec.Metadata) == 0 || rec.RefreshedAt.IsZero() {
		return false
	}
	if rec.Item != nil && rec.Item.UID != string(obj.GetUID()) {
		return false
	}
	if !Matches(rec.Inputs, For(obj)) {
		return false
	}
	return rec.RefreshedAt.UTC().Truncate(time.Second).After(RefreshedAt(obj).Truncate(time.Second))
}

// KindOf is obj's media kind and its ItemRef kind ("Movie"), for the eight
// kinds with metadata of their own.
func KindOf(obj client.Object) (commonv1.MediaKind, string, bool) {
	switch obj.(type) {
	case *catalogv1alpha1.Movie:
		return commonv1.MediaKindMovie, "Movie", true
	case *catalogv1alpha1.Series:
		return commonv1.MediaKindSeries, "Series", true
	case *catalogv1alpha1.Artist:
		return commonv1.MediaKindArtist, "Artist", true
	case *catalogv1alpha1.Album:
		return commonv1.MediaKindAlbum, "Album", true
	case *catalogv1alpha1.Author:
		return commonv1.MediaKindAuthor, "Author", true
	case *catalogv1alpha1.Book:
		return commonv1.MediaKindBook, "Book", true
	case *catalogv1alpha1.Audiobook:
		return commonv1.MediaKindAudiobook, "Audiobook", true
	case *catalogv1alpha1.Comic:
		return commonv1.MediaKindComic, "Comic", true
	}
	return "", "", false
}

// ItemRef is obj's ItemRef, UID included: the record's key and header.
func ItemRef(obj client.Object) schema.ItemRef {
	_, kind, _ := KindOf(obj)
	return schema.ItemRef{Kind: kind, Ref: schema.Ref{
		Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: string(obj.GetUID()),
	}}
}

// Seq is the admission sequence of a metadata dispatch made at now: a
// later ask supersedes an earlier one's slot (dispatch.Ledger.Admit), and
// the answer frees every slot at or below it.
func Seq(now time.Time) int64 { return now.Unix() }

// Task builds obj's MetadataTask v2 for in at priority p: its subject
// (WorkMetadataSubject) and envelope, under MsgID. The envelope key is the
// <namespace>/<name> routing key every worker parses; the media key is the
// subject token.
func Task(obj client.Object, in schema.MetadataInputs, p events.Priority, now time.Time) (string, *events.Envelope, error) {
	kind, _, ok := KindOf(obj)
	if !ok {
		return "", nil, errNoMetadata(obj)
	}
	name, data, err := schema.Encode(schema.MetadataTask{
		MediaRef:     commonv1.MediaRef{Kind: kind, Name: obj.GetName()},
		RefreshEpoch: in.RefreshEpoch,
		Item:         ItemRef(obj),
		Inputs:       in,
	})
	if err != nil {
		return "", nil, err
	}
	id := MsgID(string(obj.GetUID()), in)
	env := &events.Envelope{
		ID:     id,
		Type:   "catalog.MetadataTask",
		Schema: name,
		Source: "catalog@" + version.String(),
		Key:    obj.GetNamespace() + "/" + obj.GetName(),
		Time:   now.UTC(),
		Data:   data,
	}
	mediaKey := events.MediaKey(string(kind), obj.GetNamespace(), obj.GetName())
	return events.WorkMetadataSubject(p, mediaKey), env, nil
}

// errNoMetadata is a Task for an object with no status.metadata of its own.
func errNoMetadata(obj client.Object) error {
	return fmt.Errorf("metadatainputs: %T has no metadata of its own", obj)
}

// NewObject is an empty object of a media kind with metadata of its own,
// ready for a Get.
func NewObject(kind commonv1.MediaKind) (client.Object, bool) {
	switch kind {
	case commonv1.MediaKindMovie:
		return &catalogv1alpha1.Movie{}, true
	case commonv1.MediaKindSeries:
		return &catalogv1alpha1.Series{}, true
	case commonv1.MediaKindArtist:
		return &catalogv1alpha1.Artist{}, true
	case commonv1.MediaKindAlbum:
		return &catalogv1alpha1.Album{}, true
	case commonv1.MediaKindAuthor:
		return &catalogv1alpha1.Author{}, true
	case commonv1.MediaKindBook:
		return &catalogv1alpha1.Book{}, true
	case commonv1.MediaKindAudiobook:
		return &catalogv1alpha1.Audiobook{}, true
	case commonv1.MediaKindComic:
		return &catalogv1alpha1.Comic{}, true
	}
	return nil, false
}
