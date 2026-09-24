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

package usenet

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	usenetclient "github.com/mediactl/clustarr/pkg/download/usenet"
)

// PostProcessFromSpec resolves PostProcessSpec's *bool pointers against the
// CRD's own kubebuilder defaults -- par2=true, unpack=true,
// deleteArchives=true -- rather than Go's zero value.
//
// This is the D1 "typed clients are never defaulted" hazard in a new place
// (CLAUDE.md, carried into plan task D2-6 explicitly). PostProcessSpec's three
// switches are +kubebuilder:default=true, but that default is applied by the
// APISERVER to a field ABSENT from submitted JSON; it never touches a
// downloadv1alpha1.DownloadClient built directly in Go (an envtest fixture, a
// unit test, or -- the case that matters here -- usenetclient.Config.PostProcess,
// a plain (non-pointer) struct whose Go zero value is every switch OFF. A
// caller that read a nil *bool as false rather than "operator configured
// nothing" would silently ship an engine that repairs nothing, unpacks
// nothing and cleans up nothing for every DownloadClient nobody has
// explicitly tuned -- which is every DownloadClient today, since D2-3's
// workload reconciler stamps no default PostProcessSpec of its own.
//
// nil (PostProcessSpec itself unset) and a &PostProcessSpec{} with every
// pointer nil are the same case and resolve identically: every switch on, no
// cleanup patterns. Only an explicit non-nil pointer overrides its default.
func PostProcessFromSpec(pp *downloadv1alpha1.PostProcessSpec) usenetclient.PostProcess {
	out := usenetclient.PostProcess{Par2: true, Unpack: true, DeleteArchives: true}
	if pp == nil {
		return out
	}
	if pp.Par2 != nil {
		out.Par2 = *pp.Par2
	}
	if pp.Unpack != nil {
		out.Unpack = *pp.Unpack
	}
	if pp.DeleteArchives != nil {
		out.DeleteArchives = *pp.DeleteArchives
	}
	if len(pp.CleanupPatterns) > 0 {
		out.CleanupPatterns = append([]string(nil), pp.CleanupPatterns...)
	}
	return out
}

// readSecret fetches ref's Secret in ns and returns its Data, the same
// get-by-name shape app/indexer/controller/indexer.readSecret and
// app/indexer/download.readSecretData use for the same reason: a targeted Get,
// not a List, is all a single named SecretRef ever needs.
func readSecret(ctx context.Context, c client.Client, ns string, ref corev1.LocalObjectReference) (map[string][]byte, error) {
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("secret %s/%s not found", ns, ref.Name)
		}
		return nil, err
	}
	return s.Data, nil
}

// LoadDownloadClient fetches the DownloadClient this engine replica belongs
// to and validates that it actually describes a usenet engine -- a
// misconfigured --engine identity (D2-8's job to wire correctly) fails loudly
// here rather than building a Config with a nil UsenetSpec and panicking three
// calls later.
func LoadDownloadClient(ctx context.Context, c client.Client, namespace, name string) (*downloadv1alpha1.DownloadClient, error) {
	var dc downloadv1alpha1.DownloadClient
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &dc); err != nil {
		return nil, fmt.Errorf("usenetengine: get DownloadClient %s/%s: %w", namespace, name, err)
	}
	if dc.Spec.Protocol != commonv1alpha1.ProtocolUsenet {
		return nil, fmt.Errorf("usenetengine: DownloadClient %s/%s is protocol %q, not usenet",
			namespace, name, dc.Spec.Protocol)
	}
	if dc.Spec.Usenet == nil {
		return nil, fmt.Errorf("usenetengine: DownloadClient %s/%s has protocol usenet but spec.usenet is unset",
			namespace, name)
	}
	return &dc, nil
}

// BuildConfig resolves dc's UsenetSpec -- every provider's Secret via
// [usenetclient.ProviderFromSpec], and PostProcess's pointer defaults via
// [PostProcessFromSpec] -- into a pkg/download/usenet.Config. dc must already
// be validated by [LoadDownloadClient] (or an equivalent check); BuildConfig
// itself trusts dc.Spec.Usenet is non-nil and does not re-check the protocol.
func BuildConfig(ctx context.Context, c client.Client, dc *downloadv1alpha1.DownloadClient, dataDir, scratchDir, publishDir string) (usenetclient.Config, error) {
	us := dc.Spec.Usenet

	providers := make([]usenetclient.Provider, 0, len(us.Providers))
	for _, p := range us.Providers {
		data, err := readSecret(ctx, c, dc.Namespace, p.SecretRef)
		if err != nil {
			return usenetclient.Config{}, fmt.Errorf("usenetengine: provider %q: %w", p.Name, err)
		}
		providers = append(providers, usenetclient.ProviderFromSpec(p, string(data["username"]), string(data["password"])))
	}

	cfg := usenetclient.Config{
		Providers:          providers,
		ScratchDir:         scratchDir,
		DataDir:            dataDir,
		PublishDir:         publishDir,
		PostProcess:        PostProcessFromSpec(us.PostProcess),
		PreCheck:           us.PreCheck,
		AbortHealthPercent: us.AbortHealthPercent,
		// "" is the client's pause, which is also the CRD default.
		HealthAction: us.HealthAction,
	}
	if us.PropagationDelay != nil {
		cfg.PropagationDelay = us.PropagationDelay.Duration
	}
	// No CRD default: unset (and "0s") means no deadline, which is also
	// Config's zero value.
	if us.DownloadTimeout != nil && us.DownloadTimeout.Duration > 0 {
		cfg.DownloadTimeout = us.DownloadTimeout.Duration
	}
	return cfg, nil
}

// BuildClient loads dc, resolves it into a Config and builds the client --
// including the synchronous re-attach [usenetclient.New]'s own doc comment
// describes: it walks ScratchDir and restarts every in-flight job before
// returning. That is what lets a caller gate engine readiness on BuildClient
// having returned (plan ruling R4): an engine that reports ready before
// re-attach completes could be handed a transfer it is already running.
func BuildClient(ctx context.Context, c client.Client, namespace, downloadClientName, dataDir, scratchDir, publishDir string) (download.Client, *downloadv1alpha1.DownloadClient, error) {
	dc, err := LoadDownloadClient(ctx, c, namespace, downloadClientName)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := BuildConfig(ctx, c, dc, dataDir, scratchDir, publishDir)
	if err != nil {
		return nil, nil, err
	}
	cl, err := usenetclient.New(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("usenetengine: build client: %w", err)
	}
	return cl, dc, nil
}
