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

package rescan

import (
	"context"
	"fmt"
	"os"
	"strconv"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/version"
)

// Observations (ADR-0019 §7.6). The walk writes nothing to Kubernetes: every
// file whose facts differ from the MediaFile the walk read, or that has
// none, is reported to the manager's scan applier
// (app/import/manager/scanapply) as a schema.ScanObservation on
// CLUSTARR_INTAKE, and the applier decides -- creating the item an
// attribution names, recording the MediaFile, handing a post-transcode
// change to catalog, deleting the record of a file gone from disk, or
// refusing (an exclusion, a missing default profile, a path recorded
// against another item, a name collision, a stale basis). The applier's
// outcomes reach LibraryScan.status through its own tally
// (scanprogress.AppliedKey); the walk's tally counts only what it saw.
//
// A dry run publishes nothing: it counts what the walk would propose.

// observe publishes obs for the walk's scan, stamped with the scan and root
// folder and deduplicated per scan, path and fingerprint
// (events.MsgIDForScanObservation): a redelivered walk re-publishing a file
// the last delivery already reported is absorbed by the intake stream's
// duplicate window.
func (w *Worker) observe(ctx context.Context, st *scanState, obs schema.ScanObservation) error {
	if w.Bus == nil {
		return fmt.Errorf("rescan: no bus to report %s to", obs.Path)
	}
	obs.Scan = schema.Ref{Namespace: st.scan.Namespace, Name: st.scan.Name, UID: string(st.scan.UID)}
	obs.RootFolder = schema.Ref{Namespace: st.root.Namespace, Name: st.root.Name, UID: string(st.root.UID)}
	now := w.now()
	if obs.SeenAt.IsZero() {
		obs.SeenAt = now
	}
	name, data, err := schema.Encode(obs)
	if err != nil {
		return fmt.Errorf("rescan: encode the observation of %s: %w", obs.Path, err)
	}
	uid := string(st.scan.UID)
	env := &events.Envelope{
		ID:     events.MsgIDForScanObservation(uid, obs.Path, observationFingerprint(obs)),
		Type:   "import.ScanObservation",
		Schema: name,
		Source: "import@" + version.String(),
		Key:    st.scan.Namespace + "/" + st.scan.Name,
		Time:   now,
		Data:   data,
	}
	if _, err := w.Bus.Publish(ctx, events.IntakeScanSubject(st.scan.Namespace, uid), env); err != nil {
		return fmt.Errorf("rescan: report %s: %w", obs.Path, err)
	}
	return nil
}

// observationFingerprint is the last field of an observation's Msg-Id: its
// kind and the file's size and mtime, so the same file seen changed again in
// one scan is a new observation and the same facts twice are one.
func observationFingerprint(obs schema.ScanObservation) string {
	return obs.Kind + "-" + strconv.FormatInt(obs.SizeBytes, 10) + "-" + strconv.FormatInt(obs.ModTime.Unix(), 10)
}

// basisOf is the MediaFile the walk read, as an observation names it: the
// applier refuses to apply over one whose spec no longer hashes to SpecHash.
func basisOf(mf *catalogv1alpha1.MediaFile) *schema.MediaFileBasis {
	if mf == nil {
		return nil
	}
	return &schema.MediaFileBasis{
		Name: mf.Name, UID: string(mf.UID), ResourceVersion: mf.ResourceVersion,
		Path: mf.Spec.Path, SpecHash: mediafilespec.SpecHash(mf.Spec),
	}
}

// attributionOf is ref as an observation's proposed attribution.
func attributionOf(ref commonv1.MediaRef, evidence string) *schema.ScanAttribution {
	return &schema.ScanAttribution{
		Kind: string(ref.Kind), Name: ref.Name, Keys: ref.Keys, Track: ref.Track, Evidence: evidence,
	}
}

// proposeFile reports one attributed file: a present observation naming the
// item ref (created first when create is set), the frozen import fields a
// new MediaFile takes, and the MediaFile the walk read (existing, nil when
// none records the path). A dry run counts what the applier would be asked
// and publishes nothing: created says the proposal creates the item.
func (w *Worker) proposeFile(
	ctx context.Context, st *scanState, path string, info os.FileInfo, existing *catalogv1alpha1.MediaFile,
	ref commonv1.MediaRef, frozen *mediafilespec.Frozen, create *schema.ItemCreate, evidence string,
) error {
	if st.task.DryRun {
		st.predict(create != nil)
		return nil
	}
	attr := attributionOf(ref, evidence)
	attr.Create = create
	obs := schema.ScanObservation{
		Kind: schema.ScanObservationPresent, Path: path,
		SizeBytes: info.Size(), ModTime: info.ModTime().UTC(),
		Attribution: attr, Basis: basisOf(existing), Manual: st.manual != nil,
	}
	if existing == nil && frozen != nil {
		obs.Frozen = mediafilespec.FrozenToSchema(*frozen)
	}
	return w.observe(ctx, st, obs)
}

// predict is a dry run's count of a file the walk would propose: matched,
// and its item created (the first file of an item the walk would create) or
// updated.
func (st *scanState) predict(created bool) {
	if created {
		st.progress.ItemsCreated++
	} else {
		st.progress.ItemsUpdated++
	}
	st.progress.FilesMatched++
}
