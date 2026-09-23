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

package indexer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/ratelimit"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// Client is the one call the search fan-out and the RSS poll make against a
// live indexer. It is satisfied by *torznab.Client for a spec.generic
// Indexer and by the Cardigann engine adapter for a definition-backed one,
// which is the whole of ruling R5: a Cardigann indexer is one more client
// behind the fan-out's existing interface, so it inherits the fan-out's
// dedupe, query-limit window and health/backoff instead of getting a
// parallel search path that skips all three.
//
// It is method-for-method search.IndexerClient and rss.Searcher, so the
// value [ClientCache.For] returns is assignable to both.
type Client interface {
	Search(ctx context.Context, q torznab.Query) ([]torznab.Release, error)
}

// Condition reasons for a definition-backed Indexer.
const (
	ReasonDefinitionNotFound = "DefinitionNotFound"
	ReasonDefinitionInvalid  = "DefinitionInvalid"
	ReasonCaptchaRequired    = "CaptchaRequired"
	ReasonLoginFailed        = "LoginFailed"
	ReasonProxyUnavailable   = "ProxyUnavailable"
)

// ErrDefinitionNotFound is returned when spec.definition or
// spec.definitionRef names nothing this process can load.
var ErrDefinitionNotFound = errors.New("indexer: cardigann definition not found")

// errDefinitionInvalid wraps a definition that exists but does not load.
var errDefinitionInvalid = errors.New("indexer: cardigann definition is invalid")

// maxDefinitionErr bounds a definition load error before it reaches a
// condition message. A schema error can run to 100 KB (the carried
// remaining-work entry measured 105,599 bytes for a 73 KB definition),
// three times conditions[].message's maxLength -- and over that the apply is
// REJECTED, so the condition explaining the problem could not be written.
// indexerdefinition truncates at the same 800 for the same reason.
const maxDefinitionErr = 800

// resolveDefinition loads the Cardigann definition an Indexer names.
//
// spec.definitionRef is an IndexerDefinition's name. spec.definition is a
// bundled id, and the bundled corpus is NOT shipped yet (the remaining-work
// list carries sourcing it as unbuilt work) -- so an id resolves only
// through an IndexerDefinition that declares it, by spec.replaces first and
// its parsed status.id second. That is exactly how an override would
// resolve once a corpus exists, so the lookup order does not change when it
// lands; the corpus becomes the final fallback.
//
// A missing definition is ErrDefinitionNotFound and an unloadable one is
// errDefinitionInvalid: both are the operator's to fix, neither is a
// transient failure, and the caller reports them as conditions.
func resolveDefinition(ctx context.Context, c client.Client, spec indexv1alpha1.IndexerSpec) (*cardigann.Definition, error) {
	var d indexv1alpha1.IndexerDefinition
	switch {
	case spec.DefinitionRef != nil:
		if err := c.Get(ctx, types.NamespacedName{Name: *spec.DefinitionRef}, &d); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("%w: IndexerDefinition %q does not exist", ErrDefinitionNotFound, *spec.DefinitionRef)
			}
			return nil, err
		}
	case spec.Definition != nil:
		found, err := definitionByID(ctx, c, *spec.Definition)
		if err != nil {
			return nil, err
		}
		d = *found
	default:
		return nil, errors.New("indexer: not a definition-backed Indexer")
	}
	def, err := cardigann.Load([]byte(d.Spec.YAML))
	if err != nil {
		return nil, fmt.Errorf("%w: IndexerDefinition %q: %s", errDefinitionInvalid, d.Name,
			truncateBytes(err.Error(), maxDefinitionErr))
	}
	return def, nil
}

// definitionByID finds the IndexerDefinition that provides id. Candidates
// are ordered by name so two definitions claiming one id resolve the same
// way on every replica and every reconcile.
func definitionByID(ctx context.Context, c client.Client, id string) (*indexv1alpha1.IndexerDefinition, error) {
	var list indexv1alpha1.IndexerDefinitionList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	items := list.Items
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	for i := range items {
		if r := items[i].Spec.Replaces; r != nil && *r == id {
			return &items[i], nil
		}
	}
	for i := range items {
		if items[i].Status.ID == id {
			return &items[i], nil
		}
	}
	return nil, fmt.Errorf("%w: no IndexerDefinition provides id %q, and the bundled definition corpus is not shipped",
		ErrDefinitionNotFound, id)
}

// truncateBytes shortens s to at most n bytes on a rune boundary.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// definitionPrivacy maps the schema's tracker-privacy enum onto the CRD's
// spelling. They agree on two of three: pkg/cardigann.DefinitionType spells
// the middle one "semi-private", the CRD (and IndexerDefinition.status.type,
// whose enum makes the Cardigann spelling an apiserver rejection) says
// "semiPrivate". status.privacy carries no enum marker, so a wrong spelling
// here would be ACCEPTED -- and then disagree with the IndexerDefinition
// describing the same tracker, silently.
var definitionPrivacy = map[cardigann.DefinitionType]string{
	"public":       PrivacyPublic,
	"semi-private": PrivacySemiPrivate,
	"private":      PrivacyPrivate,
}

// definitionCaps projects a definition's declared capabilities onto the
// CRD's Caps. It is pure: a Cardigann indexer has no t=caps endpoint, its
// capabilities ARE the definition, so there is nothing to probe.
//
// Modes are renamed to Torznab's wire values ("tv-search" -> "tvsearch") --
// status.SupportsMode and the fan-out's paramSupported compare against
// those, and a Cardigann spelling would match nothing and silently search no
// indexer. Categories are grouped under their Newznab parent, the tree
// shape queryCategories expands.
func definitionCaps(def *cardigann.Definition) indexv1alpha1.Caps {
	caps := def.Capabilities()
	out := indexv1alpha1.Caps{SupportsRawSearch: caps.AllowRawSearch}
	for name, params := range caps.Modes {
		mode, ok := cardigann.TorznabMode(name)
		if !ok {
			continue
		}
		if out.Modes == nil {
			out.Modes = make(map[string][]string, len(caps.Modes))
		}
		p := slices.Clone(params)
		sort.Strings(p)
		out.Modes[string(mode)] = p
	}

	names := map[newznab.CategoryID]string{}
	for _, c := range newznab.Tree() {
		names[c.ID] = c.Name
		for _, s := range c.Sub {
			names[s.ID] = s.Name
		}
	}
	byParent := map[newznab.CategoryID]*indexv1alpha1.Category{}
	var order []newznab.CategoryID
	parentOf := func(id newznab.CategoryID) *indexv1alpha1.Category {
		p := id.Parent()
		if cat, ok := byParent[p]; ok {
			return cat
		}
		cat := &indexv1alpha1.Category{ID: int32(p), Name: names[p]}
		byParent[p] = cat
		order = append(order, p)
		return cat
	}
	for _, id := range caps.Categories {
		cat := parentOf(id)
		if id != id.Parent() {
			cat.Sub = append(cat.Sub, indexv1alpha1.SubCategory{ID: int32(id), Name: names[id]})
		}
	}
	slices.Sort(order)
	if len(order) > maxCapsItems {
		order = order[:maxCapsItems]
	}
	for _, p := range order {
		cat := *byParent[p]
		sort.SliceStable(cat.Sub, func(i, j int) bool { return cat.Sub[i].ID < cat.Sub[j].ID })
		if len(cat.Sub) > maxCapsItems {
			cat.Sub = cat.Sub[:maxCapsItems]
		}
		out.Categories = append(out.Categories, cat)
	}
	return out
}

// definitionProtocol is status.protocol for a definition-backed Indexer.
// A Cardigann v11 definition describes a torrent tracker: the schema has no
// protocol key and no usenet notion. indexerdefinition's summarise makes the
// same call, and the two must agree.
const definitionProtocol = commonv1alpha1.ProtocolTorrent

// cardigannSettings merges spec.settings with the Secret's keys (the Secret
// wins), which is the raw map cardigann.ResolveSettings types against the
// definition's declared settings.
func cardigannSettings(spec indexv1alpha1.IndexerSpec, secret map[string][]byte) map[string]string {
	raw := make(map[string]string, len(spec.Settings)+len(secret))
	for k, v := range spec.Settings {
		raw[k] = v
	}
	for k, v := range secret {
		raw[k] = string(v)
	}
	return raw
}

// siteBase is spec.baseURL with the trailing slash every cardigann.Config
// carries, so a definition's relative "browse" resolves under the site root
// rather than replacing its last path segment.
func siteBase(baseURL string) string {
	if strings.HasSuffix(baseURL, "/") {
		return baseURL
	}
	return baseURL + "/"
}

// cardigannClient is one definition-backed Indexer's engine, definition and
// resolved configuration. It is the Cardigann counterpart of *torznab.Client
// and is built by the same one function, [buildWireClient], for the same
// reason: the proxy and the limiter are applied in exactly one place.
type cardigannClient struct {
	engine  cardigann.Engine
	def     *cardigann.Definition
	cfg     cardigann.Config
	secrets []string
}

// Search runs the definition. A search.error match comes back as a
// *cardigann.SearchError -- an error, which the fan-out records against the
// indexer's escalation, rather than zero releases (ruling R6).
func (c *cardigannClient) Search(ctx context.Context, q torznab.Query) ([]torznab.Release, error) {
	return c.engine.Search(ctx, c.def, c.cfg, cardigann.QueryFromTorznab(q))
}

// Download resolves one release link through the definition's download
// block. It is what rpc.indexarr.download dispatches to for this Indexer.
func (c *cardigannClient) Download(ctx context.Context, link string) (io.ReadCloser, error) {
	return c.engine.Download(ctx, c.def, c.cfg, link)
}

// Secrets lists the values a diagnostic must never carry: every Secret value
// and the session cookie.
func (c *cardigannClient) Secrets() []string { return c.secrets }

// newEngine builds the engine for one Indexer. The limiter is READ onto it,
// never configured here (applyRateLimit is the only writer of a host's
// Config), and keyed by ratelimit.HostKey(spec.baseURL): the spelling the
// reconciler writes under, so every verb against this tracker draws on one
// bucket. A nil limiter is left as a nil interface -- assigning a nil
// *ratelimit.Limiter would produce a non-nil interface and a nil-receiver
// panic on the first request.
func newEngine(spec indexv1alpha1.IndexerSpec, lim *ratelimit.Limiter, transport http.RoundTripper) cardigann.Engine {
	eng := cardigann.Engine{
		HTTP:    &http.Client{Timeout: timeoutFor(spec.Timeout)},
		RateKey: ratelimit.HostKey(spec.BaseURL),
	}
	if transport != nil {
		eng.Proxy = transport
	}
	if lim != nil {
		eng.Limiter = lim
	}
	return eng
}

// buildCardigann assembles a definition-backed Indexer's client.
func buildCardigann(
	spec indexv1alpha1.IndexerSpec,
	def *cardigann.Definition,
	secret map[string][]byte,
	session *cardigann.Session,
	lim *ratelimit.Limiter,
	transport http.RoundTripper,
) (*cardigannClient, error) {
	cfg, err := cardigann.NewConfig(def, siteBase(spec.BaseURL), cardigannSettings(spec, secret))
	if err != nil {
		return nil, fmt.Errorf("indexer: resolve cardigann settings: %w", err)
	}
	cfg.Session = session
	secrets := make([]string, 0, len(secret)+1)
	for _, v := range secret {
		secrets = append(secrets, string(v))
	}
	if h := session.CookieHeader(); h != "" {
		secrets = append(secrets, h)
		for _, c := range session.Cookies {
			secrets = append(secrets, c.Value)
		}
	}
	return &cardigannClient{
		engine:  newEngine(spec, lim, transport),
		def:     def,
		cfg:     cfg,
		secrets: secrets,
	}, nil
}

// classifyLogin maps a login failure onto conditions. A rejected credential
// and a captcha are the operator's to fix; anything else (a timeout, a 5xx)
// is the tracker being unreachable.
func classifyLogin(err error) probeOutcome {
	if err == nil {
		return probeOutcome{}
	}
	out := probeOutcome{Reason: ReasonProbeFailed, Message: cardigann.RedactErr(err).Error()}
	var le *cardigann.LoginError
	var ce *cardigann.CaptchaRequiredError
	switch {
	case errors.As(err, &ce):
		out.Reason, out.AuthFailed = ReasonCaptchaRequired, true
	case errors.As(err, &le):
		out.Reason, out.AuthFailed = ReasonCredentialsRejected, true
	case errors.Is(err, cardigann.ErrResponseTooLarge):
		out.Reason = ReasonResponseTooLarge
	}
	return out
}

// sessionRenewMargin renews a session this long before it expires, so a
// search never races the expiry.
const sessionRenewMargin = 24 * time.Hour
