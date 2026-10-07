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
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// The download intents (ADR-0019 §6.10) are annotations on the grab's
// owner -- a Movie, Series, Album, Book, Audiobook or Comic -- which the
// downloads stage reads; the ui patches only them, as a merge patch under
// FieldManager through the owner kinds' existing patch grant, so Grants()
// is unchanged.

// grabOwners is every kind whose status.downloads holds grab entries.
var grabOwners = map[commonv1.MediaKind]bool{
	commonv1.MediaKindMovie: true, commonv1.MediaKindSeries: true, commonv1.MediaKindAlbum: true,
	commonv1.MediaKindBook: true, commonv1.MediaKindAudiobook: true, commonv1.MediaKindComic: true,
}

// Standing is a standing list annotation as the caller read it off the
// owner -- its value and the owner's resourceVersion -- so a toggle of one
// entry keeps the others and fails on a conflict rather than overwriting a
// change it did not see.
type Standing struct {
	Value           string
	ResourceVersion string
}

// annotationPatch is a merge patch of annotations, carrying the read
// resourceVersion when there is one; a nil value removes the annotation.
type annotationPatch struct {
	Metadata struct {
		ResourceVersion string             `json:"resourceVersion,omitempty"`
		Annotations     map[string]*string `json:"annotations"`
	} `json:"metadata"`
}

// patchAnnotation merge-patches one annotation on the owner.
func patchAnnotation(
	ctx context.Context, p Patcher, ns string, kind commonv1.MediaKind, owner, key string, value *string, rv string,
) error {
	if err := validateItem(ns, kind, owner); err != nil {
		return err
	}
	if !grabOwners[kind] {
		return fmt.Errorf("%w: a %s holds no grabs; its Series or Comic does", ErrInvalid, kind)
	}
	var body annotationPatch
	body.Metadata.ResourceVersion = rv
	body.Metadata.Annotations = map[string]*string{key: value}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("actions: encode %s patch: %w", key, err)
	}
	obj := monitorables[kind].newObject()
	obj.SetNamespace(ns)
	obj.SetName(owner)
	if err := p.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		return fmt.Errorf("actions: set %s on %s %s/%s: %w", key, kind, ns, owner, err)
	}
	logging.FromContext(ctx).Info("ui action: download intent set", "namespace", ns, "kind", kind, "owner", owner, "annotation", key)
	return nil
}

// validEntry refuses an entry id that is empty or would break the
// annotation grammar.
func validEntry(id string) error {
	if id == "" || strings.ContainsAny(id, ", \t\n=") {
		return fmt.Errorf("%w: %q is not a grab entry id", ErrInvalid, id)
	}
	return nil
}

// nonce is a one-shot's nonce: the request's time in milliseconds.
func nonce(now time.Time) string { return strconv.FormatInt(now.UnixMilli(), 10) }

// listFields splits a standing list value on commas and whitespace.
func listFields(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
}

// setOrClear is value, or nil (the annotation removed) when it is empty.
func setOrClear(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// PauseDownload pauses (or, paused false, unpauses) the owner's entry
// entryID: download.clustarr.io/paused, standing, keeps every other entry
// cur names.
func PauseDownload(
	ctx context.Context, p Patcher, ns string, kind commonv1.MediaKind, owner, entryID string, paused bool, cur Standing,
) error {
	ctx, span := tracing.Start(ctx, "ui.actions.PauseDownload")
	defer span.End()
	if err := validEntry(entryID); err != nil {
		return err
	}
	ids := slices.DeleteFunc(listFields(cur.Value), func(id string) bool { return id == entryID })
	if paused {
		ids = append(ids, entryID)
	}
	err := patchAnnotation(ctx, p, ns, kind, owner, catalogv1alpha1.AnnotationDownloadPaused,
		setOrClear(strings.Join(ids, ",")), cur.ResourceVersion)
	tracing.RecordError(span, err)
	return err
}

// SetDownloadPriority sets the owner's entry entryID's priority (high,
// normal, low): download.clustarr.io/priority, standing, keeps every other
// entry cur names.
func SetDownloadPriority(
	ctx context.Context, p Patcher, ns string, kind commonv1.MediaKind, owner, entryID, priority string, cur Standing,
) error {
	ctx, span := tracing.Start(ctx, "ui.actions.SetDownloadPriority")
	defer span.End()
	if err := validEntry(entryID); err != nil {
		return err
	}
	switch commonv1.DownloadPriority(priority) {
	case commonv1.DownloadPriorityHigh, commonv1.DownloadPriorityNormal, commonv1.DownloadPriorityLow:
	default:
		return fmt.Errorf("%w: priority wants high, normal or low, got %q", ErrInvalid, priority)
	}
	kv := slices.DeleteFunc(listFields(cur.Value), func(f string) bool {
		id, _, _ := strings.Cut(f, "=")
		return id == entryID
	})
	kv = append(kv, entryID+"="+priority)
	err := patchAnnotation(ctx, p, ns, kind, owner, catalogv1alpha1.AnnotationDownloadPriority,
		setOrClear(strings.Join(kv, ",")), cur.ResourceVersion)
	tracing.RecordError(span, err)
	return err
}

// RemoveDownload asks the owner to remove its entry entryID's transfer
// (download.clustarr.io/remove, one-shot), its data with data, and its
// release blocklisted for the item with blocklist.
func RemoveDownload(
	ctx context.Context, p Patcher, ns string, kind commonv1.MediaKind, owner, entryID string, data, blocklist bool, now time.Time,
) error {
	ctx, span := tracing.Start(ctx, "ui.actions.RemoveDownload")
	defer span.End()
	if err := validEntry(entryID); err != nil {
		return err
	}
	v := nonce(now) + " " + entryID
	if data {
		v += " data"
	}
	if blocklist {
		v += " blocklist"
	}
	err := patchAnnotation(ctx, p, ns, kind, owner, catalogv1alpha1.AnnotationDownloadRemove, &v, "")
	tracing.RecordError(span, err)
	return err
}

// ResumeDownload releases a health hold on the owner's entry entryID
// (download.clustarr.io/resume, one-shot).
func ResumeDownload(ctx context.Context, p Patcher, ns string, kind commonv1.MediaKind, owner, entryID string, now time.Time) error {
	ctx, span := tracing.Start(ctx, "ui.actions.ResumeDownload")
	defer span.End()
	if err := validEntry(entryID); err != nil {
		return err
	}
	v := nonce(now) + " " + entryID
	err := patchAnnotation(ctx, p, ns, kind, owner, catalogv1alpha1.AnnotationDownloadResume, &v, "")
	tracing.RecordError(span, err)
	return err
}

// RetryImport imports the owner's entry entryID again
// (download.clustarr.io/import, one-shot): to target when set (an Episode
// or Issue names one of a pack), as a person's import with override.
func RetryImport(
	ctx context.Context, p Patcher, ns string, kind commonv1.MediaKind, owner, entryID string,
	target *schema.ItemRef, override bool, now time.Time,
) error {
	ctx, span := tracing.Start(ctx, "ui.actions.RetryImport")
	defer span.End()
	if err := validEntry(entryID); err != nil {
		return err
	}
	v := nonce(now) + " " + entryID
	if target != nil {
		if target.Kind == "" || target.Name == "" || strings.ContainsAny(target.Kind+target.Name, " /\t\n") {
			return fmt.Errorf("%w: an import target wants a kind and a name", ErrInvalid)
		}
		v += " target=" + strings.ToLower(target.Kind) + "/" + target.Name
	}
	if override {
		v += " override"
	}
	err := patchAnnotation(ctx, p, ns, kind, owner, catalogv1alpha1.AnnotationDownloadImport, &v, "")
	tracing.RecordError(span, err)
	return err
}

// UnblockRelease lifts the release index's block of a release for the
// owner, or for every item with global (download.clustarr.io/unblock,
// one-shot): release is an info hash or "<indexer>/<guid>".
func UnblockRelease(
	ctx context.Context, p Patcher, ns string, kind commonv1.MediaKind, owner, release string, global bool, now time.Time,
) error {
	ctx, span := tracing.Start(ctx, "ui.actions.UnblockRelease")
	defer span.End()
	if release == "" || strings.ContainsAny(release, " \t\n") {
		return fmt.Errorf("%w: unblock wants an info hash or <indexer>/<guid>", ErrInvalid)
	}
	v := nonce(now) + " " + release
	if global {
		v += " global"
	}
	err := patchAnnotation(ctx, p, ns, kind, owner, catalogv1alpha1.AnnotationDownloadUnblock, &v, "")
	tracing.RecordError(span, err)
	return err
}

// PauseDownload is [PauseDownload] over the Actions' writer.
func (a *Actions) PauseDownload(
	ctx context.Context, ns string, kind commonv1.MediaKind, owner, entryID string, paused bool, cur Standing,
) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return PauseDownload(ctx, a.w, ns, kind, owner, entryID, paused, cur)
}

// SetDownloadPriority is [SetDownloadPriority] over the Actions' writer.
func (a *Actions) SetDownloadPriority(
	ctx context.Context, ns string, kind commonv1.MediaKind, owner, entryID, priority string, cur Standing,
) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return SetDownloadPriority(ctx, a.w, ns, kind, owner, entryID, priority, cur)
}

// RemoveDownload is [RemoveDownload] over the Actions' writer, now.
func (a *Actions) RemoveDownload(
	ctx context.Context, ns string, kind commonv1.MediaKind, owner, entryID string, data, blocklist bool,
) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return RemoveDownload(ctx, a.w, ns, kind, owner, entryID, data, blocklist, time.Now())
}

// ResumeDownload is [ResumeDownload] over the Actions' writer, now.
func (a *Actions) ResumeDownload(ctx context.Context, ns string, kind commonv1.MediaKind, owner, entryID string) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return ResumeDownload(ctx, a.w, ns, kind, owner, entryID, time.Now())
}

// RetryImport is [RetryImport] over the Actions' writer, now.
func (a *Actions) RetryImport(
	ctx context.Context, ns string, kind commonv1.MediaKind, owner, entryID string, target *schema.ItemRef, override bool,
) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return RetryImport(ctx, a.w, ns, kind, owner, entryID, target, override, time.Now())
}

// UnblockRelease is [UnblockRelease] over the Actions' writer, now.
func (a *Actions) UnblockRelease(
	ctx context.Context, ns string, kind commonv1.MediaKind, owner, release string, global bool,
) error {
	if a == nil || a.w == nil {
		return ErrNoWriter
	}
	return UnblockRelease(ctx, a.w, ns, kind, owner, release, global, time.Now())
}
