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

// Package manager registers Live TV in cmd/manager (spec 2026-10-07 §5,
// §6): the tuner, a leader-only runnable serving every provider on one
// port, and the IPTVProvider and IPTVChannel controllers that feed it and
// write status from its snapshot.
package manager

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/controller/iptvchannel"
	"github.com/mediactl/clustarr/app/livetv/controller/iptvprovider"
	"github.com/mediactl/clustarr/app/livetv/leaderendpoint"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Options configures the livetv step.
type Options struct {
	k8s.Options
	// BindAddress is the tuner's listen address; k8s.DisabledBindAddress
	// ("0") registers nothing.
	BindAddress string
	// Ports is the range blocks' ports come from (Live TV spec §5.0).
	Ports iptvprovider.PortRange
	// Service names the livetv Service the installers render, in the
	// manager's namespace: the address the Plex pods' proxies dial.
	Service string
}

// DefaultPortRange is --livetv-port-range's default.
const DefaultPortRange = "47000-47999"

// ParsePortRange parses "low-high": both 1024-65535, low below high.
func ParsePortRange(s string) (iptvprovider.PortRange, error) {
	lo, hi, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return iptvprovider.PortRange{}, fmt.Errorf("livetv: port range %q: want low-high", s)
	}
	l, err1 := strconv.ParseUint(strings.TrimSpace(lo), 10, 16)
	h, err2 := strconv.ParseUint(strings.TrimSpace(hi), 10, 16)
	if err1 != nil || err2 != nil || l < 1024 || h < 1024 || l >= h {
		return iptvprovider.PortRange{}, fmt.Errorf("livetv: port range %q: want low-high, both 1024-65535, low below high", s)
	}
	return iptvprovider.PortRange{Low: int32(l), High: int32(h)}, nil
}

// notifyBuffer bounds each controller's pending tuner notices. A full
// channel drops a notice: the next snapshot change notifies again.
const notifyBuffer = 256

// TargetPort is the port of a bind address such as ":5004".
func TargetPort(bind string) (int32, error) {
	_, port, err := net.SplitHostPort(bind)
	if err != nil {
		return 0, fmt.Errorf("livetv: bind address %q: %w", bind, err)
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("livetv: bind address %q names no port", bind)
	}
	return int32(n), nil
}

// Register adds the tuner and both controllers to mgr.
func Register(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if o.BindAddress == k8s.DisabledBindAddress {
		return nil
	}
	port, err := TargetPort(o.BindAddress)
	if err != nil {
		return err
	}
	providerEvents := make(chan event.GenericEvent, notifyBuffer)
	channelEvents := make(chan event.GenericEvent, notifyBuffer)
	notify, notifyTuners := notifiers(providerEvents, channelEvents)
	tn := tuner.New(tuner.Options{
		BindAddress: o.BindAddress,
		Store:       bus.ObjectStore(events.BucketLiveTV),
		// No overall timeout: streams are long. A stream's headers must come
		// within streamHeaderTimeout, a playlist's or guide's within
		// fetchHeaderTimeout.
		HTTP:         &http.Client{Transport: transport(streamHeaderTimeout)},
		Fetch:        &http.Client{Transport: transport(fetchHeaderTimeout)},
		Now:          time.Now,
		Debounce:     2 * time.Second,
		Notify:       notify,
		NotifyTuners: notifyTuners,
		// The guide's programme artwork is looked up through the metadata
		// gateway's search, the Add New search (tuner/artwork.go).
		Search: func(ctx context.Context, req schema.MetadataRequest) (schema.MetadataResponse, error) {
			var resp schema.MetadataResponse
			err := bus.Request(ctx, events.RPCMetadataSearch, req, &resp)
			return resp, events.DescribeRequestError(events.RPCMetadataSearch, err)
		},
	})
	if err := mgr.Add(tn); err != nil {
		return fmt.Errorf("livetv: tuner: %w", err)
	}
	// The livetv Service has no selector: the leader publishes itself as its
	// endpoint once the tuner listens (control-plane placement design §5.4).
	if err := mgr.Add(&leaderendpoint.Publisher{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Tuner: tn, Service: o.Service, Namespace: o.Namespace,
		PodName: os.Getenv("POD_NAME"), PodIP: os.Getenv("POD_IP"), Port: port,
	}); err != nil {
		return fmt.Errorf("livetv: endpoint publisher: %w", err)
	}
	provider := &iptvprovider.Reconciler{
		Client: mgr.GetClient(), SecretReader: mgr.GetAPIReader(), Tuner: tn,
		Address: o.Service + "." + o.Namespace + ".svc:80", Ports: o.Ports, Events: providerEvents,
	}
	if err := provider.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("livetv: iptvprovider: %w", err)
	}
	channel := &iptvchannel.Reconciler{Client: mgr.GetClient(), Snapshots: tn, Events: channelEvents}
	if err := channel.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("livetv: iptvchannel: %w", err)
	}
	return nil
}

const (
	// streamHeaderTimeout is how long a stream's upstream may take to answer.
	streamHeaderTimeout = 10 * time.Second
	// fetchHeaderTimeout is how long a playlist's or guide's may take. A
	// panel builds the whole file before it answers: from 18:01 UTC on
	// 2026-10-20 tvload.win took 13 s to start its 588 MB m3u_plus. Every
	// fetch failed against the stream's 10 s, and once the manager restarted
	// no channel had a stream to tune. The tuner's fetchTimeout bounds the
	// whole fetch.
	fetchHeaderTimeout = 2 * time.Minute
)

func transport(headers time.Duration) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = headers
	return t
}

// notifiers are the tuner's two notices: a snapshot change wakes both
// controllers; a change in tuners in use, which only the provider's status
// shows, wakes the provider controller alone. A full channel drops a notice:
// the next change notifies again.
func notifiers(providers, channels chan event.GenericEvent) (notify, notifyTuners func(types.NamespacedName)) {
	send := func(key types.NamespacedName, to ...chan event.GenericEvent) {
		ev := event.GenericEvent{Object: &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}}
		for _, ch := range to {
			select {
			case ch <- ev:
			default:
			}
		}
	}
	notify = func(key types.NamespacedName) { send(key, providers, channels) }
	notifyTuners = func(key types.NamespacedName) { send(key, providers) }
	return notify, notifyTuners
}
