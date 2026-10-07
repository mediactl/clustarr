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

package fileimport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/version"
)

// HeartbeatInterval is how often a walk sends an in-progress ack, checked
// before each file: a multi-file inspect or a hardlink-or-copy execute can
// outlast the delivery's acknowledgement deadline, ConsumerImportFile's
// BackOff[0] (30s on a first delivery, not its 60s AckWait: a BackOff
// replaces AckWait as the deadline). Between two beats the walk may wait out
// this interval and probe a file (videoProbeTimeout), so the two together
// must fit inside it; TestTheImportFitsTheFileConsumersAckDeadline holds
// them to it. It is also at most a third of that deadline, so two
// heartbeats can be lost before a lapse (test/guards.
// TestHeartbeatsFitTheirDeadline).
const HeartbeatInterval = 10 * time.Second

// The import agent reads only (ADR-0019 W21, W22, W24): the items an
// import names, their root folders and quality profiles, and the MediaFiles
// an execute's plan replaces (the basis check). The manager materialises
// MediaFiles and AudioGrafts.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=series;episodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=artists;albums;authors;books;audiobooks;comics;issues,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch

// Worker handles the import tasks on importarr-fileimport: the inspect and
// the execute of ADR-0019 §6.9.
type Worker struct {
	// Client is the cache-backed client the items, root folders and quality
	// profiles are read through. It is never written.
	Client client.Client

	// APIReader reads straight from the apiserver: the execute re-reads
	// every MediaFile its plan replaces and refuses a stale plan. Nil falls
	// back to Client.
	APIReader client.Reader

	// Bus carries the clustarr-imports records.
	Bus events.Bus

	// Catalogue is the TRaSH custom-format corpus scoring is done against.
	Catalogue *catalogue.Catalogue

	// Clock is the time source.
	Clock func() time.Time

	// Prober is the import domain's one prober (spec 2026-10-06 §6.6): a
	// video file's probe corrects its quality and names it. Nil probes
	// nothing.
	Prober mediainfo.Prober

	// ProbeAudio reads a music file's codec and bitrate (FrozenFileQuality).
	ProbeAudio AudioProber

	// SampleMaxBytes is the video size floor (fsops.IsSuspectedSample);
	// zero disables it.
	SampleMaxBytes int64

	// Pod names the writer on every record; empty is the host name.
	Pod string

	once sync.Once
	recs *records.Writer[*schema.ImportRecord]
	read *records.Reader[*schema.ImportRecord]
}

// NewWorker builds a Worker with the production catalogue, clock and sample
// threshold, probing through prober. A nil prober probes nothing.
func NewWorker(c client.Client, bus events.Bus, prober mediainfo.Prober) *Worker {
	w := &Worker{
		Client: c, Bus: bus, Catalogue: catalogue.LoadedCatalogue(), Clock: time.Now,
		SampleMaxBytes: fsops.DefaultSampleMaxBytes,
	}
	w.Prober = prober
	if prober != nil {
		w.ProbeAudio = prober.ProbeAudio
	}
	return w
}

func (w *Worker) initRecords() {
	w.once.Do(func() {
		pod := w.Pod
		if pod == "" {
			pod, _ = os.Hostname()
		}
		kv := w.Bus.KV(events.BucketImports)
		w.recs = records.NewWriter(kv, agentrecords.Imports(), pod, version.String())
		w.read = records.NewReader(kv, agentrecords.Imports())
	})
}

func (w *Worker) writer() *records.Writer[*schema.ImportRecord] { w.initRecords(); return w.recs }

func (w *Worker) reader() *records.Reader[*schema.ImportRecord] { w.initRecords(); return w.read }

func (w *Worker) apiReader() client.Reader {
	if w.APIReader != nil {
		return w.APIReader
	}
	return w.Client
}

// audioProbe is the AudioProber an inspect hands FrozenFileQuality:
// ProbeAudio after a heartbeat on m, so the probe's AudioProbeTimeout lies
// inside the delivery's ack deadline. A failed heartbeat runs no probe and
// is kept in *hbErr, which the caller returns.
func (w *Worker) audioProbe(m events.Message, hbErr *error) AudioProber {
	if w.ProbeAudio == nil {
		return nil
	}
	return func(ctx context.Context, path string) (mediainfo.AudioProbe, error) {
		if err := heartbeat(ctx, m); err != nil {
			*hbErr = err
			return mediainfo.AudioProbe{}, err
		}
		return w.ProbeAudio(ctx, path)
	}
}

func (w *Worker) now() time.Time {
	if w.Clock != nil {
		return w.Clock()
	}
	return time.Now()
}

// Handle implements events.Handler: it dispatches on the envelope's schema
// to the inspect or the execute. A v1 ImportTask -- the single-phase import
// a release before ADR-0019 published -- is discarded: the manager inspects
// every Completed entry again.
func (w *Worker) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "fileimport.Worker.Handle")
	defer span.End()
	if env == nil {
		return events.Discard("import task has no envelope", errors.New("fileimport: nil envelope"))
	}
	switch env.Schema {
	case schema.ImportInspectTask{}.Schema():
		return w.handleInspect(ctx, m, env)
	case schema.ImportExecuteTask{}.Schema():
		return w.handleExecute(ctx, m, env)
	case schema.ImportTask{}.Schema():
		return events.Discard("superseded by ADR-0019 two-phase imports", nil)
	}
	return events.Discard("fileimport: unknown task schema "+env.Schema, nil)
}

// resolveProfile loads the QualityProfile the import scores files against:
// the grab's own, else the item's. A profile that cannot be named or found
// blocks the import.
func (w *Worker) resolveProfile(ctx context.Context, grabRef, itemRef string) (quality.Profile, error) {
	ref := grabRef
	if ref == "" {
		ref = itemRef
	}
	if ref == "" {
		return quality.Profile{}, blocked("neither the grab nor its item names a quality profile")
	}
	var qp catalogv1alpha1.QualityProfile
	if err := w.Client.Get(ctx, client.ObjectKey{Name: ref}, &qp); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return quality.Profile{}, blocked("quality profile %q does not exist", ref)
		}
		return quality.Profile{}, fmt.Errorf("fileimport: get quality profile %s: %w", ref, err)
	}
	profile, ferrs := quality.FromCRD(&qp, w.Catalogue)
	if len(ferrs) > 0 {
		return quality.Profile{}, blocked("quality profile %q is invalid: %v", ref, errors.Join(ferrs...))
	}
	return profile, nil
}

// beat extends the delivery's ack deadline when HeartbeatInterval has
// elapsed.
func (w *Worker) beat(ctx context.Context, m events.Message, last *time.Time) error {
	now := w.now()
	if !last.IsZero() && now.Sub(*last) < HeartbeatInterval {
		return nil
	}
	*last = now
	return heartbeat(ctx, m)
}

// heartbeat extends the delivery's ack deadline now.
func heartbeat(ctx context.Context, m events.Message) error {
	if err := m.InProgress(ctx); err != nil {
		return fmt.Errorf("fileimport: heartbeat: %w", err)
	}
	return nil
}

// statDir reports whether dir exists and is a directory.
func statDir(dir string) (bool, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return false, err
	}
	if !fi.IsDir() {
		return false, fmt.Errorf("fileimport: %s is not a directory", dir)
	}
	return true, nil
}
