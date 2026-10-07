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

package transcode

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/transcode/graftstate"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Gather takes the file's admission grant when it holds one (an in-use
// grant never expires, and the pass confirms or releases it), and reads the
// profiles, whether the file's item exists and whether a standalone graft
// holds the file, from the manager cache.
func (a *Adapter) Gather(ctx context.Context, env *remediation.Env, v *remediation.View) (In, error) {
	in := In{
		Read: true, Ledger: v.Ledger, Intents: transcodeplan.ParseIntents(v.File.Annotations),
		DeadLettered: v.File.Annotations[k8s.AnnotationDeadLettered],
	}
	if a.o.Ledger != nil && v.Ledger.Grant != transcodeplan.GrantNone {
		in.Ledger, in.Took = a.o.Ledger.TakeGrant(v.File.UID)
	}
	var profiles transcodev1alpha1.TranscodeProfileList
	if err := env.Reader.List(ctx, &profiles, client.UnsafeDisableDeepCopy); err != nil {
		return in, remediation.Transient(fmt.Errorf("transcode: list TranscodeProfiles: %w", err))
	}
	in.Profiles = profiles.Items
	exists, err := itemExists(ctx, env.Reader, v.File)
	if err != nil {
		return in, remediation.Transient(err)
	}
	in.ItemExists = exists
	if a.o.AudioGraftIndex != "" {
		if in.StandaloneGraft, err = graftstate.StandaloneFor(ctx, env.Reader, a.o.AudioGraftIndex, client.ObjectKeyFromObject(v.File)); err != nil {
			return in, remediation.Transient(err)
		}
	}
	if err := a.gatherDispatch(ctx, env, v, &in); err != nil {
		return in, remediation.Transient(err)
	}
	return in, nil
}

// gatherDispatch reads what a dispatch's rows need (§4.10, §5.9): the
// record while the block is in flight or a dispatch grant is held; the
// RootFolders when a task may be built; the lease of a Running dispatch,
// noting since when it is missing; and, once it has been missing for the
// lost-worker grace, whether the stream still holds the file's task.
func (a *Adapter) gatherDispatch(ctx context.Context, env *remediation.Env, v *remediation.View, in *In) error {
	b := v.Prev.Transcode
	uid := v.File.UID
	dispatching := in.Ledger.Grant == transcodeplan.GrantDispatch
	inFlight := b != nil && b.Dispatch.InFlight()
	if b != nil && b.Dispatch != nil {
		if err := a.statFiles(ctx, env, v, in); err != nil {
			return err
		}
	}
	readRecord := dispatching || inFlight || in.SpecPathMissing ||
		(b != nil && transcodeplan.InFactWindow(b, deadlineOf(in.Profiles, b), v.Now.Time))
	if a.o.Records != nil && readRecord {
		rec, rev, ok, err := a.o.Records.Get(ctx, events.RecordKey(string(uid)))
		if err != nil {
			return err
		}
		in.RecordRev = rev
		if ok {
			in.Record = rec
		}
	}
	if dispatching || (b != nil && b.Phase == catalogv1alpha1.TranscodePhaseQueued) {
		var roots catalogv1alpha1.RootFolderList
		if err := env.Reader.List(ctx, &roots, client.InNamespace(v.File.Namespace), client.UnsafeDisableDeepCopy); err != nil {
			return fmt.Errorf("transcode: list RootFolders: %w", err)
		}
		in.Folders = roots.Items
	}
	if b == nil || b.Phase != catalogv1alpha1.TranscodePhaseRunning || a.o.Leases == nil {
		if a.o.Ledger != nil {
			a.o.Ledger.NoteLeaseMissing(uid, false, v.Now.Time)
		}
		return nil
	}
	_, err := a.o.Leases.Get(ctx, events.TranscodeLeaseKey(string(uid)))
	switch {
	case errors.Is(err, events.ErrKeyNotFound):
		in.Lease.Missing = true
	case err != nil:
		return fmt.Errorf("transcode: read the lease: %w", err)
	}
	if a.o.Ledger != nil {
		in.Lease.MissingSince = a.o.Ledger.NoteLeaseMissing(uid, in.Lease.Missing, v.Now.Time)
	}
	if in.Lease.Missing && !in.Lease.MissingSince.IsZero() && v.Now.Sub(in.Lease.MissingSince) > transcodeplan.LeaseLostGrace &&
		a.o.Admin != nil {
		subjects, err := a.o.Admin.Subjects(ctx, events.StreamWorkTranscode, events.WorkTranscodeTaskSubjectAnyPool(string(uid)))
		if err != nil {
			return fmt.Errorf("transcode: read the stored tasks: %w", err)
		}
		in.TaskStored = new(len(subjects) > 0)
	}
	return nil
}

// itemExists Gets the file's Movie or Episode from the cache: NotFound is
// false, any other error is returned.
func itemExists(ctx context.Context, r client.Reader, mf *catalogv1alpha1.MediaFile) (bool, error) {
	key := types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}
	var obj client.Object
	switch mf.Spec.MediaRef.Kind {
	case commonv1.MediaKindMovie:
		obj = &catalogv1alpha1.Movie{}
	case commonv1.MediaKindEpisode:
		obj = &catalogv1alpha1.Episode{}
	default:
		return false, nil
	}
	switch err := r.Get(ctx, key, obj, client.UnsafeDisableDeepCopy); {
	case apierrors.IsNotFound(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("transcode: get %s %s: %w", mf.Spec.MediaRef.Kind, key, err)
	}
	return true, nil
}

// statFiles stats spec.path and, when it is gone, the last dispatch's
// output named in its record, through the I/O executor (§3.17): the
// FileMissing row (§5.9). A busy pool, a timeout or an open breaker is
// transient.
func (a *Adapter) statFiles(ctx context.Context, env *remediation.Env, v *remediation.View, in *In) error {
	if env.IO == nil || env.DataDir == "" {
		return nil
	}
	local, err := jobspec.LocalPath(env.DataDir, v.File.Spec.Path)
	if err != nil {
		return nil //nolint:nilerr // a path off /data is the probe planner's to report
	}
	switch _, err := env.IO.Stat(ctx, local); {
	case errors.Is(err, fs.ErrNotExist):
		in.SpecPathMissing = true
	case err != nil:
		return err
	default:
		return nil
	}
	if a.o.Records == nil {
		return nil
	}
	rec, _, ok, err := a.o.Records.Get(ctx, events.RecordKey(string(v.File.UID)))
	if err != nil || !ok || rec.Inputs.OutputPath == "" || rec.Inputs.OutputPath == v.File.Spec.Path {
		return err
	}
	out, err := jobspec.LocalPath(env.DataDir, rec.Inputs.OutputPath)
	if err != nil {
		return nil //nolint:nilerr // as above
	}
	switch fi, err := env.IO.Stat(ctx, out); {
	case err == nil:
		in.OutputExists, in.OutputSize = true, fi.Size()
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	return nil
}

// deadlineOf is the deadline b's dispatch ran under: its profile's, else
// the default.
func deadlineOf(profiles []transcodev1alpha1.TranscodeProfile, b *catalogv1alpha1.TranscodeState) time.Duration {
	for i := range profiles {
		if profiles[i].Name == b.Profile {
			return jobspec.ActiveDeadline(profiles[i].Spec)
		}
	}
	return jobspec.ActiveDeadline(transcodev1alpha1.TranscodeProfileSpec{})
}
