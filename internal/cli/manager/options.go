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

// Package manager is cmd/manager's command tree (spec §3.4): every reconciler
// in one leader-elected process under the lease manager.clustarr.io.
package manager

import (
	"errors"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/app/grab/controller/downloadclient"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/squash/controller/audiograft"
	"github.com/mediactl/clustarr/app/squash/controller/pool"
	"github.com/mediactl/clustarr/app/squash/controller/transcodejob"
	squashmanager "github.com/mediactl/clustarr/app/squash/manager"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// LeaderElectionID is the one lease every reconciler runs under (spec §3.11):
// the six <svc>.clustarr.io leases became this one.
const LeaderElectionID = "manager.clustarr.io"

// Options is everything cmd/manager's flags set (spec §3.4.1).
type Options struct {
	k8s.Options
	Logging logging.Options
	Tracing tracing.Options

	// DataDir is the RWX media volume: grabarr's, squasharr's and
	// captionarr's --data-dir and importarr's --data-path, merged. It is
	// also the mount path stamped onto the engines and the pools.
	DataDir string
	// EngineScratchDir is the scratch mount the DownloadClient controller
	// stamps onto the engines: grabarr's controller --scratch-dir, renamed.
	EngineScratchDir string
	// NativeImage is the image the engines and the transcode pools run:
	// grabarr's --engine-image and squasharr's --worker-image, merged
	// (§13 OD3).
	NativeImage string
	// DataClaim is the RWX claim the engines and the pools mount at DataDir.
	DataClaim string
	// EngineServiceAccount is the ServiceAccount the engine pods run as.
	EngineServiceAccount string

	Slots             map[string]int32
	IntelRenderGroups []int64
	NodeLabelNVIDIA   string
	NodeLabelIntel    string
	JobWindow         int
	JobRetention      time.Duration
	GraftConcurrency  int

	CardigannDefinitionsDir string
	CardigannBundled        bool
	TraktBaseURL            string

	// The remediation loop's tuning (loop spec §3.1, §3.7, §3.17).
	RemediationConcurrency         int
	RemediationBulkWritesPerSecond int
	RemediationIOWorkers           int

	Autoscale                  bool
	ExternalMetricsBindAddress string
	ExternalMetricsService     string
	ExternalMetricsSecret      string
	LegacyLeaseCheck           bool

	// Test seams; production leaves them zero. RESTConfig nil reads the
	// kubeconfig; SkipNameValidation lets one test binary start the manager
	// twice (controller names are process-global).
	RESTConfig         *rest.Config
	SkipNameValidation bool
}

// DefaultOptions is what an unflagged `manager` runs with.
func DefaultOptions() Options {
	o := Options{Options: k8s.DefaultOptions()}
	o.LeaderElect = true
	o.DataDir, o.EngineScratchDir = "/data", "/scratch"
	o.DataClaim, o.EngineServiceAccount = downloadclient.DefaultDataClaimName, downloadclient.DefaultEngineServiceAccount
	o.Slots = squashmanager.DefaultSlots()
	o.NodeLabelNVIDIA, o.NodeLabelIntel = pool.DefaultNodeLabelNVIDIA, pool.DefaultNodeLabelIntel
	o.JobWindow, o.JobRetention = squashmanager.DefaultJobWindow, squashmanager.DefaultJobRetention
	o.GraftConcurrency = audiograft.DefaultConcurrency
	o.CardigannBundled = true
	o.RemediationConcurrency = remediation.DefaultConcurrency
	o.RemediationBulkWritesPerSecond = remediation.DefaultBulkWritesPerSecond
	o.RemediationIOWorkers = remediation.DefaultIOWorkers
	o.Autoscale = true
	o.ExternalMetricsBindAddress, o.ExternalMetricsService, o.ExternalMetricsSecret = ":6443", "external-metrics", "external-metrics-tls"
	o.LegacyLeaseCheck = true
	return o
}

// Validate is §3.4.1's check, run before anything touches the cluster.
func (o Options) Validate() error {
	var errs []error
	for _, req := range []struct{ flag, value string }{
		{"--nats-url", o.NATSURL}, {"--native-image", o.NativeImage}, {"--data-claim", o.DataClaim},
		{"--engine-service-account", o.EngineServiceAccount}, {"--data-dir", o.DataDir},
	} {
		if strings.TrimSpace(req.value) == "" {
			errs = append(errs, fmt.Errorf("%s is required", req.flag))
		}
	}
	for _, l := range []struct{ flag, key string }{
		{"--gpu-node-label-nvidia", o.NodeLabelNVIDIA}, {"--gpu-node-label-intel", o.NodeLabelIntel},
	} {
		if msgs := validation.IsQualifiedName(l.key); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("%s %q: %s", l.flag, l.key, strings.Join(msgs, "; ")))
		}
	}
	if o.RemediationConcurrency < 1 || o.RemediationConcurrency > 64 {
		errs = append(errs, fmt.Errorf("--remediation-concurrency %d: must be 1 to 64", o.RemediationConcurrency))
	}
	if o.RemediationIOWorkers < 1 || o.RemediationIOWorkers > 64 {
		errs = append(errs, fmt.Errorf("--remediation-io-workers %d: must be 1 to 64", o.RemediationIOWorkers))
	}
	if o.RemediationBulkWritesPerSecond < 0 {
		errs = append(errs, fmt.Errorf("--remediation-bulk-writes-per-second %d: must be 0 (off) or more", o.RemediationBulkWritesPerSecond))
	}
	if o.Autoscale && (o.ExternalMetricsBindAddress == "" || o.ExternalMetricsBindAddress == k8s.DisabledBindAddress) {
		errs = append(errs, errors.New("--autoscale needs --external-metrics-bind-address; pass --autoscale=false to run without the External Metrics API"))
	}
	if err := o.Options.Validate(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// managerOptions is §5.6's one merged cache: the lease manager.clustarr.io,
// the pinned default field owner, Secret and ConfigMap never cached (every
// read is a by-name Get; Wave 4e's Trakt token compare-and-swap reads the
// Secret live), the pool Jobs selected by squasharr's managed-by label, and
// no MediaFile status.mediaInfo strip (the manager reads it).
func managerOptions(o Options) ctrl.Options {
	opts := o.Options.ManagerOptions(LeaderElectionID, o.LeaderElect)
	opts.Client.FieldOwner = string(k8s.DefaultFieldOwner)
	opts.Client.Cache = &client.CacheOptions{DisableFor: []client.Object{&corev1.Secret{}, &corev1.ConfigMap{}}}
	opts.Cache.ByObject = map[client.Object]cache.ByObject{&batchv1.Job{}: transcodejob.PoolJobCache(o.Namespace)}
	if o.SkipNameValidation {
		opts.Controller.SkipNameValidation = ptr.To(true)
	}
	return opts
}
