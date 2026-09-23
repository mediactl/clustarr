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
	"sync"
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
// definition id, and an id resolves only through an IndexerDefinition that
// provides it -- loaded by indexarr/bundle from the embedded corpus or a
// mounted directory, or operator-applied -- by spec.replaces first,
// its parsed status.id second, and last the retired ids its spec.yaml says
// it replaces (status.replaces), so an Indexer written against a tracker's
// old id keeps working after the definition is renamed. See
// [definitionByID].
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
// way on every replica and every reconcile, and the three keys are tried in
// order of how directly a definition claims the id:
//
//  1. spec.replaces -- an override: this object stands in for that id.
//  2. status.id -- the definition IS that id.
//  3. status.replaces -- the definition supersedes that retired id, from the
//     Cardigann `replaces` key (pkg/cardigann.Definition.Replaces).
//
// The third is Jackett's rename handling, where the `replaces` key and the
// format come from: IndexerManagerService.MigrateRenamedIndexers builds an
// old-id -> new-id map from every indexer's Replaces, keeping the first
// claimant and warning on a second, and GetIndexer resolves an old id
// through it "to maintain backward compatibility"
// (github.com/Jackett/Jackett, src/Jackett.Common/Services/
// IndexerManagerService.cs at abd180d3417e21329f0ef3ce277e6657d40a68bb).
// Prowlarr does not: its CardigannMetaDefinition has no Replaces property
// and nothing in NzbDrone.Core reads the key (Prowlarr/Prowlarr at
// 12c327808314a7cae1b7301935ee10cabc19609f). Jackett consults the alias
// before the live id; here it comes last, because definitions are
// operator-supplied, and one that declares an id itself must not lose it to
// another that only claims to have replaced it.
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
	for i := range items {
		if slices.Contains(items[i].Status.Replaces, id) {
			return &items[i], nil
		}
	}
	return nil, fmt.Errorf("%w: no IndexerDefinition provides id %q (none declares it, overrides it or replaces it); "+
		"neither the embedded corpus nor a --cardigann-definitions-dir bundle provides it", ErrDefinitionNotFound, id)
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
//
// It is cached ([ClientCache]) and shared by every concurrent search, poll
// and download against the Indexer, so the one thing it mutates -- the
// session a re-login replaces -- is behind mu.
type cardigannClient struct {
	engine cardigann.Engine
	def    *cardigann.Definition

	mu      sync.Mutex
	cfg     cardigann.Config
	secrets []string

	// relogin logs in again and persists the new session. nil means this
	// client cannot (a unit test, or a definition with no login block), and
	// an expired session is then an ordinary failure.
	relogin func(ctx context.Context) (*cardigann.Session, error)

	// loginMu single-flights relogin: the fan-out searching one indexer from
	// several requests at once must log in once, not once per request.
	loginMu sync.Mutex
}

// config is the current configuration, session included.
func (c *cardigannClient) config() cardigann.Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// Search runs the definition. A search.error match comes back as a
// *cardigann.SearchError -- an error, which the fan-out records against the
// indexer's escalation, rather than zero results (ruling R6).
//
// A search the tracker redirected to its login page (cardigann's
// ErrSessionExpired) is NOT an indexer failure: the session was killed or
// timed out server-side, which says nothing about the tracker's health. The
// client logs in again and retries the search once, as Prowlarr's
// HttpIndexerBase does when CheckIfLoginNeeded matches a response. Only a
// failed re-login -- which is the credentials or the tracker failing -- or a
// second redirect straight after a fresh login reaches the caller as an
// error, and those escalate like any other failure.
func (c *cardigannClient) Search(ctx context.Context, q torznab.Query) ([]torznab.Release, error) {
	cfg := c.config()
	query := cardigann.QueryFromTorznab(q)
	rels, err := c.engine.Search(ctx, c.def, cfg, query)
	if err == nil || c.relogin == nil || !errors.Is(err, cardigann.ErrSessionExpired) {
		return rels, err
	}
	if lerr := c.renewSession(ctx, cfg.Session); lerr != nil {
		return nil, lerr
	}
	return c.engine.Search(ctx, c.def, c.config(), query)
}

// renewSession replaces stale with a fresh login, unless another caller
// already did while this one waited for loginMu.
func (c *cardigannClient) renewSession(ctx context.Context, stale *cardigann.Session) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	if c.config().Session != stale {
		return nil // renewed by the caller ahead of us
	}
	sess, err := c.relogin(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.Session = sess
	c.secrets = appendSessionSecrets(c.secrets, sess)
	return nil
}

// Download resolves one release link through the definition's download
// block. It is what rpc.indexarr.download dispatches to for this Indexer.
func (c *cardigannClient) Download(ctx context.Context, link string) (io.ReadCloser, error) {
	return c.engine.Download(ctx, c.def, c.config(), link)
}

// Secrets lists the values a diagnostic must never carry: every Secret value
// and the session cookie, including any a re-login added.
func (c *cardigannClient) Secrets() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.secrets)
}

// appendSessionSecrets adds sess's cookie header and values to secrets.
func appendSessionSecrets(secrets []string, sess *cardigann.Session) []string {
	if h := sess.CookieHeader(); h != "" {
		secrets = append(secrets, h)
		for _, c := range sess.Cookies {
			secrets = append(secrets, c.Value)
		}
	}
	return secrets
}

// newEngine builds the engine for one Indexer. The limiter is READ onto it,
// never configured here (applyRateLimit is the only writer of a host's
// Config), and keyed by [rateKey]: the host of def.SiteLink(spec.baseURL),
// where the engine's requests actually go, and the spelling the reconciler
// writes under, so every verb against this tracker draws on one bucket. A
// nil limiter is left as a nil interface -- assigning a nil
// *ratelimit.Limiter would produce a non-nil interface and a nil-receiver
// panic on the first request.
func newEngine(
	spec indexv1alpha1.IndexerSpec, def *cardigann.Definition, lim *ratelimit.Limiter, transport http.RoundTripper,
) cardigann.Engine {
	eng := cardigann.Engine{
		HTTP:    &http.Client{Timeout: timeoutFor(spec.Timeout)},
		RateKey: rateKey(spec, def),
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
	secrets = appendSessionSecrets(secrets, session)
	return &cardigannClient{
		engine:  newEngine(spec, def, lim, transport),
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
