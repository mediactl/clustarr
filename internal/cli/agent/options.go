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

// Package agent is cmd/agent's command tree (spec §3.5): one domain per
// process, never elected, never ensuring topology.
package agent

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const (
	// DefaultIndexPath is the index domain's SQLite release index file, on
	// the RWO volume config/manager mounts at /var/lib/clustarr/index.
	DefaultIndexPath = "/var/lib/clustarr/index/releases.db"
	// DefaultFacadeBindAddress is the Torznab facade's address, the port the
	// installers' Service routes.
	DefaultFacadeBindAddress = ":8080"
	// DefaultFacadeAPIKeySecret is the Secret, in --namespace, holding the
	// facade's API keys.
	DefaultFacadeAPIKeySecret = "indexarr-facade"
	// defaultTopologyWait is how long an agent waits for the manager's
	// topology (§3.5.2 step 7): below the liveness budget (15 s initial
	// delay plus 3 x 20 s), so a timeout exits before the kubelet kills.
	defaultTopologyWait = 45 * time.Second
)

// Options is everything cmd/agent's flags set (spec §3.5.1).
type Options struct {
	k8s.Options
	Domain  string
	Logging logging.Options
	Tracing tracing.Options

	// DrainTimeout is how long running handlers keep their context after
	// shutdown begins (events.Subscription.Drain); 0 derives it (§3.5.6).
	DrainTimeout time.Duration
	// TopologyWait bounds the wait for the manager's topology.
	TopologyWait time.Duration

	DataDir                   string // import, caption, engines
	SampleMaxBytes            int64  // import
	TraktBaseURL, PlexBaseURL string // import
	IndexPath, IndexDSN       string // index
	FacadeBindAddress         string // index
	FacadeAPIKeySecret        string // index
	DownloadClient            string // torrent-engine
	Engine                    string // engines
	ScratchDir, PublishDir    string // engines

	// Test seams; production leaves them zero. RESTConfig nil reads the
	// kubeconfig; WrapBus wraps the domain's bus (a recording bus);
	// SkipNameValidation lets one test binary start a domain twice.
	RESTConfig         *rest.Config
	WrapBus            func(events.Bus) events.Bus
	SkipNameValidation bool
}

// DefaultOptions is what an unflagged `agent --domain <d>` runs with.
func DefaultOptions() Options {
	return Options{
		Options: k8s.DefaultOptions(), TopologyWait: defaultTopologyWait,
		DataDir: "/data", SampleMaxBytes: fsops.DefaultSampleMaxBytes, ScratchDir: "/scratch",
		IndexPath: DefaultIndexPath, FacadeBindAddress: DefaultFacadeBindAddress, FacadeAPIKeySecret: DefaultFacadeAPIKeySecret,
	}
}

var errDomain = fmt.Errorf("--domain takes exactly one of %s", strings.Join(domainNames, ", "))

// Validate is §3.5.1's check, run before anything touches the cluster.
func (o Options) Validate() error {
	if _, ok := domains[o.Domain]; !ok {
		return errDomain
	}
	var errs []error
	if strings.TrimSpace(o.NATSURL) == "" {
		errs = append(errs, errors.New("--nats-url is required"))
	}
	switch o.Domain {
	case "import", "caption", "torrent-engine", "usenet-engine":
		if o.DataDir == "" {
			errs = append(errs, fmt.Errorf("--domain %s needs --data-dir", o.Domain))
		}
	}
	switch o.Domain {
	case "torrent-engine":
		if o.Engine == "" {
			errs = append(errs, errors.New("--domain torrent-engine needs exactly one of --engine or --download-client"))
		}
	case "usenet-engine":
		if o.Engine == "" {
			errs = append(errs, errors.New("--domain usenet-engine needs --engine"))
		}
		if o.ScratchDir == "" {
			errs = append(errs, errors.New("--domain usenet-engine needs --scratch-dir"))
		}
	case "index":
		if o.FacadeBindAddress != k8s.DisabledBindAddress && (o.FacadeAPIKeySecret == "" || o.Namespace == "") {
			errs = append(errs, errors.New("the Torznab facade fails closed: it needs --facade-api-key-secret and --namespace, or --facade-bind-address=0"))
		}
		if o.IndexPath == "" && o.IndexDSN == "" {
			errs = append(errs, errors.New("--index-path is required unless --index-dsn is set"))
		}
	}
	if err := o.Options.Validate(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// managerOptions is AgentManagerOptions plus the test seam.
func managerOptions(o Options) ctrl.Options {
	opts := o.Options.AgentManagerOptions()
	if o.SkipNameValidation {
		opts.Controller.SkipNameValidation = ptr.To(true)
	}
	return opts
}

// drainFor is §3.5.6: the longest declared AckWait among the domain's
// durables. The engines own no durable, so theirs is 0 and the graceful
// shutdown stays the default.
func drainFor(domain string) time.Duration {
	var longest time.Duration
	top := events.Default()
	for _, d := range agentdomain.Domains() {
		if d.Name != domain {
			continue
		}
		for _, name := range d.Consumers {
			if c, ok := top.Consumer(name); ok && c.AckWait > longest {
				longest = c.AckWait
			}
		}
	}
	return longest
}
