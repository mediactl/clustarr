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

package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
)

// ErrUnavailable is returned when an Indexer's proxies cannot be routed
// through: a spec.proxyRef that names nothing, missing credentials, a
// selector that cannot be evaluated, or two proxies competing for one slot.
//
// It is an ERROR, deliberately, never a silent fall back to a direct
// connection. An operator names a proxy to keep the cluster's real address
// away from a tracker; quietly bypassing it on a misconfiguration is the one
// outcome worse than failing the request.
var ErrUnavailable = errors.New("app/indexer/proxy: indexer proxy unavailable")

// Selection is the proxies that apply to one Indexer: at most one Route (an
// http, socks4 or socks5 proxy the connection goes through) and at most one
// FlareSolverr (applied last, outermost, to answer challenges).
type Selection struct {
	Route       *indexv1alpha1.IndexerProxy
	FlareSolver *indexv1alpha1.IndexerProxy
}

// Empty reports whether no proxy applies.
func (s Selection) Empty() bool { return s.Route == nil && s.FlareSolver == nil }

// Fingerprint identifies the selection by each proxy's name and
// resourceVersion, so a cached client built for an older selection -- a
// proxy edited, added or removed -- can be told apart from a current one.
func (s Selection) Fingerprint() string {
	var parts []string
	for _, p := range []*indexv1alpha1.IndexerProxy{s.Route, s.FlareSolver} {
		if p == nil {
			parts = append(parts, "-")
			continue
		}
		parts = append(parts, p.Name+"@"+p.ResourceVersion)
	}
	return strings.Join(parts, "|")
}

// Select decides which of proxies (the IndexerProxies in idx's namespace)
// apply to idx. It is pure.
//
//   - spec.proxyRef names one proxy explicitly, of any type, and it takes its
//     type's slot; a name that is not in proxies is ErrUnavailable.
//   - Every other proxy applies when its spec.selector matches idx's labels.
//     An EMPTY selector matches nothing: selector is a non-pointer field a Go
//     client always sends as {}, and "{} means every Indexer" would route the
//     whole namespace through a proxy that never asked to be.
//   - An explicit proxyRef wins its slot over selector matches. Two selector
//     matches for one slot (two routes, or -- the CRD's own rule -- two
//     FlareSolverrs) is ErrUnavailable: picking one silently could route a
//     private tracker through the wrong egress.
//   - A selector that does not parse is ErrUnavailable for every Indexer it
//     could have been meant for: it cannot be evaluated, and treating it as
//     "matches nothing" would leak the address it was written to hide.
func Select(idx *indexv1alpha1.Indexer, proxies []indexv1alpha1.IndexerProxy) (Selection, error) {
	sorted := make([]indexv1alpha1.IndexerProxy, len(proxies))
	copy(sorted, proxies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var sel Selection
	ref := ""
	if idx.Spec.ProxyRef != nil {
		ref = *idx.Spec.ProxyRef
	}
	if ref != "" {
		found := false
		for i := range sorted {
			if sorted[i].Name == ref {
				found = true
				p := &sorted[i]
				if p.Spec.Type == indexv1alpha1.IndexerProxyTypeFlareSolverr {
					sel.FlareSolver = p
				} else {
					sel.Route = p
				}
			}
		}
		if !found {
			return Selection{}, fmt.Errorf("%w: IndexerProxy %s/%s does not exist", ErrUnavailable, idx.Namespace, ref)
		}
	}

	var routes, flares []*indexv1alpha1.IndexerProxy
	set := labels.Set(idx.Labels)
	for i := range sorted {
		p := &sorted[i]
		if p.Name == ref || selectorEmpty(p.Spec.Selector) {
			continue
		}
		s, err := metav1.LabelSelectorAsSelector(&p.Spec.Selector)
		if err != nil {
			return Selection{}, fmt.Errorf("%w: IndexerProxy %s has an invalid spec.selector: %w", ErrUnavailable, p.Name, err)
		}
		if !s.Matches(set) {
			continue
		}
		if p.Spec.Type == indexv1alpha1.IndexerProxyTypeFlareSolverr {
			flares = append(flares, p)
		} else {
			routes = append(routes, p)
		}
	}
	if sel.Route == nil {
		switch len(routes) {
		case 0:
		case 1:
			sel.Route = routes[0]
		default:
			return Selection{}, fmt.Errorf("%w: %d proxies select this Indexer (%s); name one with spec.proxyRef",
				ErrUnavailable, len(routes), names(routes))
		}
	}
	if sel.FlareSolver == nil {
		switch len(flares) {
		case 0:
		case 1:
			sel.FlareSolver = flares[0]
		default:
			return Selection{}, fmt.Errorf("%w: at most one FlareSolverr proxy may select an Indexer, %d do (%s)",
				ErrUnavailable, len(flares), names(flares))
		}
	}
	return sel, nil
}

func selectorEmpty(s metav1.LabelSelector) bool {
	return len(s.MatchLabels) == 0 && len(s.MatchExpressions) == 0
}

func names(ps []*indexv1alpha1.IndexerProxy) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return strings.Join(out, ", ")
}

// Selected lists idx's namespace's IndexerProxies through c (the manager's
// cached client) and returns [Select]'s answer.
func Selected(ctx context.Context, c client.Client, idx *indexv1alpha1.Indexer) (Selection, error) {
	var list indexv1alpha1.IndexerProxyList
	if err := c.List(ctx, &list, client.InNamespace(idx.Namespace)); err != nil {
		return Selection{}, fmt.Errorf("%w: list IndexerProxies: %w", ErrUnavailable, err)
	}
	return Select(idx, list.Items)
}

// Resolve builds the transport every request of idx must use, or returns nil
// for an Indexer no proxy applies to.
//
// It is called by the ONE builder shared by the caps probe, the search
// fan-out, the RSS poll and the download verb, so the proxies apply to all
// four or to none -- a probe that honoured the proxy while searches bypassed
// it would report the proxy healthy while leaking the real IP.
func Resolve(ctx context.Context, c client.Client, idx *indexv1alpha1.Indexer) (http.RoundTripper, error) {
	sel, err := Selected(ctx, c, idx)
	if err != nil {
		return nil, err
	}
	return Build(ctx, c, sel)
}

// Build renders a Selection as a transport, reading the route proxy's
// credentials live (indexarr does not cache Secrets).
func Build(ctx context.Context, c client.Client, sel Selection) (http.RoundTripper, error) {
	if sel.Empty() {
		return nil, nil
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("%w: the default transport is not an *http.Transport", ErrUnavailable)
	}
	var (
		rt       http.RoundTripper = base.Clone()
		routeURL string
	)
	if p := sel.Route; p != nil {
		creds, err := readSecret(ctx, c, p.Namespace, p.Spec.SecretRef)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		tr, u, err := RouteTransport(base, p.Spec, creds)
		if err != nil {
			return nil, err
		}
		rt, routeURL = tr, u
	}
	if p := sel.FlareSolver; p != nil {
		if sel.Route != nil && routeURL == "" {
			// FlareSolverr's proxy parameter carries no credentials, so a
			// route that needs them cannot be handed to it -- and a solve
			// that went direct would reveal the address the route hides.
			return nil, fmt.Errorf("%w: FlareSolverr %s cannot fetch through proxy %s, which needs credentials",
				ErrUnavailable, p.Name, sel.Route.Name)
		}
		if p.Spec.Host == "" || p.Spec.Port <= 0 {
			return nil, fmt.Errorf("%w: FlareSolverr %s has no host:port", ErrUnavailable, p.Name)
		}
		rt = &FlareSolverr{
			Endpoint:   "http://" + net.JoinHostPort(p.Spec.Host, strconv.Itoa(int(p.Spec.Port))) + "/v1",
			MaxTimeout: requestTimeoutFor(p.Spec.RequestTimeout),
			Proxy:      routeURL,
			Next:       rt,
		}
	}
	return rt, nil
}

// RouteTransport clones base to go through one http, socks4 or socks5 proxy.
// It also returns the route as a credential-free URL FlareSolverr can be
// told to fetch through, or "" when the route carries credentials it cannot
// be given.
//
// http and socks5 use net/http's own proxy support (http.ProxyURL
// understands both schemes) with the Secret's username/password; socks4 --
// which net/http cannot speak -- dials through [Socks4Dialer], whose only
// credential is a user id, taken from the Secret's username.
func RouteTransport(base *http.Transport, spec indexv1alpha1.IndexerProxySpec, creds map[string][]byte) (*http.Transport, string, error) {
	if spec.Host == "" {
		return nil, "", fmt.Errorf("%w: proxy has no host", ErrUnavailable)
	}
	if spec.Port <= 0 || spec.Port > 65535 {
		return nil, "", fmt.Errorf("%w: proxy port %d is not addressable", ErrUnavailable, spec.Port)
	}
	addr := net.JoinHostPort(spec.Host, strconv.Itoa(int(spec.Port)))
	user, pass := string(creds["username"]), string(creds["password"])
	tr := base.Clone()
	switch spec.Type {
	case indexv1alpha1.IndexerProxyTypeHTTP, indexv1alpha1.IndexerProxyTypeSocks5:
		scheme := "http"
		if spec.Type == indexv1alpha1.IndexerProxyTypeSocks5 {
			scheme = "socks5"
		}
		u := &url.URL{Scheme: scheme, Host: addr}
		plain := u.String()
		if user != "" {
			u.User = url.UserPassword(user, pass)
			plain = ""
		}
		tr.Proxy = http.ProxyURL(u)
		return tr, plain, nil
	case indexv1alpha1.IndexerProxyTypeSocks4:
		d := &Socks4Dialer{Addr: addr, UserID: user}
		if base.DialContext != nil {
			d.Forward = base.DialContext
		}
		tr.Proxy = nil
		tr.DialContext = d.DialContext
		plain := "socks4://" + addr
		if user != "" {
			plain = ""
		}
		return tr, plain, nil
	default:
		return nil, "", fmt.Errorf("%w: proxy type %q is not a route", ErrUnavailable, spec.Type)
	}
}

// readSecret reads a proxy's spec.secretRef. A missing Secret is an error:
// the proxy named credentials it does not have.
func readSecret(ctx context.Context, c client.Client, ns string, ref *corev1.LocalObjectReference) (map[string][]byte, error) {
	if ref == nil || ref.Name == "" {
		return nil, nil
	}
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("proxy secret %s/%s not found", ns, ref.Name)
		}
		return nil, err
	}
	return s.Data, nil
}

// requestTimeoutFor floors a proxy's spec.requestTimeout, which a Go client
// always sends (as "0s" when unset).
func requestTimeoutFor(d metav1.Duration) time.Duration {
	if d.Duration <= 0 {
		return defaultMaxTimeout
	}
	return d.Duration
}
