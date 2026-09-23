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

package cardigann

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Session is what Engine.Login produces and every later Search/Download
// call on the same indexer carries forward.
type Session struct {
	Cookies []*http.Cookie
	// Headers is rare; most definitions authenticate via search.headers
	// instead (a Config value rendered into the request, not a Session).
	Headers   http.Header
	ExpiresAt time.Time
}

// sessionTTL is how long a freshly built Session is considered valid. The
// schema carries no explicit session lifetime, so this is a conservative
// default the indexer controller can override by re-running Login.
const sessionTTL = 30 * 24 * time.Hour

// LoginError signals that Engine.Login ran but the indexer rejected the
// credentials (an ErrorBlock matched the response).
type LoginError struct{ Message string }

func (e *LoginError) Error() string { return "cardigann: login failed: " + e.Message }

// CaptchaRequiredError signals that def.Login.Captcha is set, the login page
// actually served a captcha challenge, and no manual cookie was supplied.
// pkg/cardigann never solves captchas (45 bundled definitions declare one);
// Engine.Login returns this instead of attempting the submit so the indexer
// controller can surface it (Indexer.status condition Authenticated=False,
// reason CaptchaRequired, this error's text as the message) rather than
// looping forever.
//
// The workaround is a manual cookie: sign in to the tracker with a browser,
// copy the request's Cookie header, and put it in the Indexer Secret's
// "cookie" key (the `cookie` setting). When the login page serves its
// captcha, Login then takes that cookie as the session instead of failing
// -- still proved by the definition's login.test -- and keeps doing so on
// every renewal until the tracker expires it, when the test fails and the
// cookie must be replaced.
type CaptchaRequiredError struct {
	Type     string // "image" | "text"
	Selector string // login.captcha.selector, what matched on the page
}

func (e *CaptchaRequiredError) Error() string {
	what := "a captcha"
	if e.Type != "" {
		what = e.Type + " captcha"
		if strings.ContainsRune("aeiouAEIOU", rune(e.Type[0])) {
			what = "an " + what
		} else {
			what = "a " + what
		}
	}
	return fmt.Sprintf("cardigann: captcha required: the login page serves %s (%q), which is never solved; "+
		`sign in with a browser and set the Indexer Secret's "cookie" key to its Cookie header to use that session instead`,
		what, e.Selector)
}

// ErrSessionRequired is returned by Search/Download when def.Login != nil
// and cfg.Session is nil — Engine.Login is always explicit; Search never
// logs in on the caller's behalf, matching the indexer controller's own
// division of labour (login once, persist cookies into an owned Secret,
// mirror into the clustarr-indexer-sessions KV bucket — spec §5's KV
// table — then pass the reconstructed Session into every later call).
//
// This only applies to login methods that actually produce a session
// (form/post/cookie, or the "" default which is form): a "get"/"oneurl"
// login authenticates every request straight from Config (an API key
// rendered into search.headers or search.inputs, as 0dayfiles-api.yml
// does — see Session.Headers' own doc comment, "most definitions
// authenticate via search.headers instead"), so there is no session to
// require before Search runs. See loginRequiresSession.
var ErrSessionRequired = errors.New("cardigann: definition requires login; call Engine.Login first")

// loginRequiresSession reports whether lb's method produces a Session
// that Search/Download must see before they can run (a cookie carried on
// every subsequent request), as opposed to a method that authenticates
// every request independently via Config values.
func loginRequiresSession(lb *LoginBlock) bool {
	if lb == nil {
		return false
	}
	switch lb.Method {
	case "get", "oneurl":
		return false
	default: // "", "form", "post", "cookie"
		return true
	}
}

// Login dispatches on def.Login.Method (default "form"): form, post, cookie,
// get, oneurl. Returns nil, nil when def.Login is nil (public tracker).
//
// Every HTTP login collects cookies in a cookie jar that lives for this one
// call, so the Session carries every cookie the tracker set along the way:
// the landing page's (a form login's CSRF token is bound to it, and the
// submit must send it back), and a redirect's. The classic login answers the
// POST with a 302 that sets the session cookie and redirects to the index;
// Go's client follows that redirect and, without a jar, the only cookies
// left are the index page's. Prowlarr's HttpClient keeps a cookie container
// across its redirect loop for the same reason.
//
// Whatever the method, a definition's login.test then proves the Session
// authenticates (runLoginTest) before Login returns it: Jackett's
// ApplyConfiguration runs TestLogin after every DoLogin, and Prowlarr runs
// the same selector over every response (CheckIfLoginIsNeeded). Until gap
// fix Z6 only a cookie login was tested, so a form or post login whose
// credentials the tracker silently ignored -- no login.error selector
// matching, a 200 back -- was reported Authenticated with a dead session;
// 302 of the 752 bundled definitions carry a test on a form or post login.
func (e Engine) Login(ctx context.Context, def *Definition, cfg Config) (*Session, error) {
	lb := def.Login
	if lb == nil {
		return nil, nil
	}
	sess, err := e.login(ctx, def, cfg, lb)
	if err != nil {
		return nil, err
	}
	if lb.Test != nil {
		if err := e.runLoginTest(ctx, def, cfg, sess, lb.Test); err != nil {
			return nil, err
		}
	}
	return sess, nil
}

// login is Login's per-method dispatch, before login.test.
func (e Engine) login(ctx context.Context, def *Definition, cfg Config, lb *LoginBlock) (*Session, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cardigann: cookie jar: %w", err)
	}
	lf := loginFlow{e: e, def: def, cfg: cfg, lb: lb, tc: e.templateContext(def, cfg), jar: jar}
	switch lb.Method {
	case "", "form", "post":
		// Prowlarr sends login.cookies with the form and post logins --
		// the corpus uses it for a "JAVA=OK" that gets past a JavaScript
		// check -- so they start in the jar.
		lf.seedCookies()
	}

	switch lb.Method {
	case "", "form":
		return lf.form(ctx)
	}
	if lb.Captcha != nil {
		page, _, err := lf.landing(ctx)
		if err != nil {
			return nil, err
		}
		if sess, err := lf.captcha(page); sess != nil || err != nil {
			return sess, err
		}
	}
	switch lb.Method {
	case "cookie":
		return e.loginCookie(ctx, def, cfg, lb)
	case "post":
		return lf.post(ctx)
	case "get", "oneurl":
		return lf.get(ctx)
	default:
		return nil, fmt.Errorf("cardigann: unknown login method %q", lb.Method)
	}
}

// loginFlow is one Login call's state: what every step renders against and
// the jar every step's cookies land in.
type loginFlow struct {
	e   Engine
	def *Definition
	cfg Config
	lb  *LoginBlock
	tc  *TemplateContext
	jar http.CookieJar
}

// captcha checks the landing page for login.captcha. With no captcha on the
// page it returns nil, nil and the login proceeds. With one, the operator's
// manual cookie (the `cookie` setting, a browser's Cookie header) becomes
// the session; without one, the login fails with *CaptchaRequiredError,
// whose text says how to supply it.
func (lf loginFlow) captcha(page Doc) (*Session, error) {
	cb := lf.lb.Captcha
	if cb == nil {
		return nil, nil
	}
	if _, ok := page.Select(cb.Selector); !ok {
		return nil, nil
	}
	if raw, ok := lf.cfg.stringValue("cookie"); ok {
		if cookies := parseCookieHeader(raw); len(cookies) > 0 {
			return &Session{Cookies: cookies, ExpiresAt: lf.e.now().Add(sessionTTL)}, nil
		}
	}
	return nil, &CaptchaRequiredError{Type: cb.Type, Selector: cb.Selector}
}

// exchange is how a login request is sent. The landing page follows
// redirects only when the definition sets followredirect (Prowlarr's
// GetConfigurationForSetup); the form and post submits always follow
// (AllowAutoRedirect = true); a get/oneurl login does not, and checks its
// error selectors against the response it got.
func (lf loginFlow) exchange(follow bool) exchange {
	return exchange{def: lf.def, site: lf.cfg.BaseURL, follow: follow, jar: lf.jar}
}

// seedCookies puts each login.cookies entry written as a Cookie header into
// the jar for the site.
func (lf loginFlow) seedCookies() {
	site, err := url.Parse(lf.cfg.BaseURL)
	if err != nil || site.Host == "" {
		return
	}
	for _, entry := range lf.lb.Cookies {
		if c := parseCookieHeader(entry); len(c) > 0 {
			lf.jar.SetCookies(site, c)
		}
	}
}

// headers is login.headers, else search.headers (Prowlarr's
// `Login?.Headers ?? Search?.Headers`).
func (lf loginFlow) headers() map[string][]string {
	if lf.lb.Headers != nil {
		return lf.lb.Headers
	}
	return lf.def.Search.Headers
}

// loginURL renders and resolves login.path.
func (lf loginFlow) loginURL() (string, error) {
	path, err := render(lf.lb.Path, lf.tc)
	if err != nil {
		return "", err
	}
	return resolveURL(lf.cfg.BaseURL, path)
}

// landing GETs login.path and parses it as HTML.
func (lf loginFlow) landing(ctx context.Context) (Doc, string, error) {
	u, err := lf.loginURL()
	if err != nil {
		return Doc{}, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Doc{}, "", fmt.Errorf("cardigann: build request: %w", RedactErr(err))
	}
	if err := renderHeaders(req, lf.headers(), lf.tc); err != nil {
		return Doc{}, "", err
	}
	req.Header.Set("Referer", lf.cfg.BaseURL)
	_, body, err := lf.e.do(ctx, req, lf.exchange(lf.def.FollowRedirect))
	if err != nil {
		return Doc{}, "", err
	}
	doc, err := lf.parse(body)
	if err != nil {
		return Doc{}, "", fmt.Errorf("cardigann: parse login page: %w", err)
	}
	return doc, u, nil
}

// parse decodes body from the definition's charset and parses it as HTML.
func (lf loginFlow) parse(body []byte) (Doc, error) {
	body, err := decodeBody(lf.tc.enc, body)
	if err != nil {
		return Doc{}, err
	}
	return ParseDoc(ResponseHTML, body)
}

// session builds the Session from every cookie the jar holds for the site,
// the login page and the submit target (a cookie scoped to a login
// sub-path is still the session's).
func (lf loginFlow) session(urls ...string) *Session {
	seen := map[string]int{}
	var cookies []*http.Cookie
	for _, raw := range append([]string{lf.cfg.BaseURL}, urls...) {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		for _, c := range lf.jar.Cookies(u) {
			if i, ok := seen[c.Name]; ok {
				cookies[i] = c
				continue
			}
			seen[c.Name] = len(cookies)
			cookies = append(cookies, c)
		}
	}
	return &Session{Cookies: cookies, ExpiresAt: lf.e.now().Add(sessionTTL)}
}

// form is Prowlarr's form login (CardigannRequestGenerator.DoLogin, method
// "form"): GET login.path; refuse if the page serves the declared captcha;
// find login.form (default "form") and seed the submission with every
// enabled, named <input> in it -- hidden fields included, and a checkbox or
// radio only when checked; overlay login.inputs (whose keys are CSS
// selectors naming the input when login.selectors is set); add
// login.selectorinputs, scraped from the page, to the form and
// login.getselectorinputs to the submit URL's query string; POST to
// login.submitpath, else the form's action, resolved against the login
// page; check login.error. The submit is multipart when the form says so.
func (lf loginFlow) form(ctx context.Context) (*Session, error) {
	lb := lf.lb
	page, loginURL, err := lf.landing(ctx)
	if err != nil {
		return nil, err
	}
	if sess, err := lf.captcha(page); sess != nil || err != nil {
		return sess, err
	}

	formSel := lb.Form
	if formSel == "" {
		formSel = "form"
	}
	form, ok := page.Select(formSel)
	if !ok {
		return nil, fmt.Errorf("cardigann: login: no form matches %q on %s", formSel, redactRawURL(loginURL))
	}
	form = Doc{rt: form.rt, html: form.html.First()}

	pairs := url.Values{}
	form.html.Find("input").Each(func(_ int, in *goquery.Selection) {
		name, ok := in.Attr("name")
		if !ok || name == "" {
			return
		}
		if _, disabled := in.Attr("disabled"); disabled {
			return
		}
		switch strings.ToLower(in.AttrOr("type", "")) {
		case "checkbox", "radio":
			if _, checked := in.Attr("checked"); !checked {
				return
			}
		}
		pairs.Set(name, in.AttrOr("value", ""))
	})

	for k, v := range lb.Inputs {
		rendered, err := render(string(v), lf.tc)
		if err != nil {
			return nil, err
		}
		key := k
		if lb.Selectors {
			el, ok := page.Select(k)
			if !ok {
				return nil, fmt.Errorf("cardigann: login: no input matches selector %q", k)
			}
			key, _ = el.Text("name")
		}
		pairs.Set(key, rendered)
	}
	if err := lf.scrapeInputs(ctx, page, lb.SelectorInputs, pairs, "selector input"); err != nil {
		return nil, err
	}
	query := url.Values{}
	if err := lf.scrapeInputs(ctx, page, lb.GetSelectorInputs, query, "get selector input"); err != nil {
		return nil, err
	}

	action := lb.SubmitPath
	if action == "" {
		action = form.html.AttrOr("action", "")
	}
	submitURL, err := resolveURL(loginURL, action)
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(submitURL, "?") {
			sep = "&"
		}
		submitURL += sep + encodeValues(query, lf.tc.enc, "")
	}

	contentType, body, err := formBody(pairs, lf.tc, strings.EqualFold(form.html.AttrOr("enctype", ""), "multipart/form-data"))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, submitURL, body)
	if err != nil {
		return nil, fmt.Errorf("cardigann: build request: %w", RedactErr(err))
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Referer", loginURL)
	if err := renderHeaders(req, lf.headers(), lf.tc); err != nil {
		return nil, err
	}
	resp, respBody, err := lf.e.do(ctx, req, lf.exchange(true))
	if err != nil {
		return nil, err
	}
	if err := lf.checkErrors(resp, respBody); err != nil {
		return nil, err
	}
	return lf.session(loginURL, submitURL), nil
}

// scrapeInputs evaluates each selector input against the login page into
// dst. A required one that matches nothing fails the login; an optional one
// is left out (Prowlarr skips it rather than sending an empty value).
func (lf loginFlow) scrapeInputs(ctx context.Context, page Doc, inputs map[string]SelectorBlock, dst url.Values, what string) error {
	for name, sel := range inputs {
		val, ok, err := sel.Extract(ctx, page, lf.tc)
		if err != nil {
			return fmt.Errorf("cardigann: login %s %q: %w", what, name, err)
		}
		if !ok {
			if sel.Optional {
				continue
			}
			return fmt.Errorf("cardigann: login %s %q not found", what, name)
		}
		dst.Set(name, val)
	}
	return nil
}

// formBody encodes pairs as the login form's body: urlencoded in the
// definition's charset, or multipart/form-data when the form declares that
// enctype (Prowlarr builds the multipart body by hand for the same case).
func formBody(pairs url.Values, tc *TemplateContext, multi bool) (string, io.Reader, error) {
	if !multi {
		return "application/x-www-form-urlencoded", strings.NewReader(encodeValues(pairs, tc.enc, "")), nil
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range pairs[k] {
			if err := w.WriteField(k, toCharset(v, tc.enc)); err != nil {
				return "", nil, fmt.Errorf("cardigann: multipart login form: %w", err)
			}
		}
	}
	if err := w.Close(); err != nil {
		return "", nil, fmt.Errorf("cardigann: multipart login form: %w", err)
	}
	return w.FormDataContentType(), &buf, nil
}

// post is the form login minus the landing page: login.inputs (rendered)
// POST straight to login.submitpath, else login.path.
func (lf loginFlow) post(ctx context.Context) (*Session, error) {
	lb := lf.lb
	form := url.Values{}
	for k, v := range lb.Inputs {
		rendered, err := render(string(v), lf.tc)
		if err != nil {
			return nil, err
		}
		form.Set(k, rendered)
	}
	submitPath := lb.SubmitPath
	if submitPath == "" {
		submitPath = lb.Path
	}
	submitPath, err := render(submitPath, lf.tc)
	if err != nil {
		return nil, err
	}
	resp, respBody, err := lf.e.postForm(ctx, lf.cfg, lf.tc, submitPath, form, lf.exchange(true))
	if err != nil {
		return nil, err
	}
	if err := lf.checkErrors(resp, respBody); err != nil {
		return nil, err
	}
	submitURL, _ := resolveURL(lf.cfg.BaseURL, submitPath)
	return lf.session(submitURL), nil
}

// get is a GET to login.path with login.inputs as query parameters plus
// login.headers, checked against login.error. oneurl has the same shape: the
// single URL carrying the API key is both the login and its own test.
func (lf loginFlow) get(ctx context.Context) (*Session, error) {
	lb := lf.lb
	u, err := lf.loginURL()
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, fmt.Errorf("cardigann: login get url %q: %w", redactRawURL(u), RedactErr(err))
	}
	q := parsed.Query()
	for k, v := range lb.Inputs {
		rendered, err := render(string(v), lf.tc)
		if err != nil {
			return nil, err
		}
		q.Set(k, rendered)
	}
	parsed.RawQuery = encodeValues(q, lf.tc.enc, "")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("cardigann: build request: %w", RedactErr(err))
	}
	if err := renderHeaders(req, lf.headers(), lf.tc); err != nil {
		return nil, err
	}
	attachSession(req, lf.cfg.Session)

	resp, respBody, err := lf.e.do(ctx, req, lf.exchange(false))
	if err != nil {
		return nil, err
	}
	if err := lf.checkErrors(resp, respBody); err != nil {
		return nil, err
	}
	return lf.session(u), nil
}

// checkErrors is Prowlarr's CheckForError: a 401 is a failed login whatever
// the body says, and otherwise the first login.error block that matches
// the (decoded) response is.
func (lf loginFlow) checkErrors(resp *http.Response, body []byte) error {
	if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		return &LoginError{Message: "HTTP 401 Unauthorized"}
	}
	body, err := decodeBody(lf.tc.enc, body)
	if err != nil {
		return err
	}
	return checkLoginErrors(body, lf.lb.Error, lf.tc)
}

// loginCookie builds the Session from cookies the operator supplies; there
// is no HTTP round trip for the login itself (note §3.5), only login.test
// when the definition has one (Login runs it for every method).
//
// The cookies come from the `cookie` setting, a Cookie header pasted from a
// browser ("uid=1; pass=abc") -- Prowlarr's semantics (it reads the
// setting and ignores login.cookies for this method), and what all 138
// cookie-method definitions in the v11 corpus declare. Each login.cookies
// entry adds to them: one in header form ("name=value") literally, and a
// bare name as the value of the setting (or raw config key) of that name --
// this package's original reading, kept for definitions written to it.
// Until X8a only the bare-name reading existed, so every corpus cookie
// tracker logged in with no cookies at all.
//
// A cookie login that ends with no cookie is an error: it can only ever
// send unauthenticated requests. No error quotes a cookie value.
func (e Engine) loginCookie(ctx context.Context, def *Definition, cfg Config, lb *LoginBlock) (*Session, error) {
	var cookies []*http.Cookie
	if raw, ok := cfg.stringValue("cookie"); ok {
		cookies = append(cookies, parseCookieHeader(raw)...)
	}
	for _, entry := range lb.Cookies {
		if strings.Contains(entry, "=") {
			cookies = append(cookies, parseCookieHeader(entry)...)
			continue
		}
		val, ok := cfg.stringValue(entry)
		if !ok || val == "" {
			return nil, fmt.Errorf("cardigann: login cookie %q has no configured value", entry)
		}
		cookies = append(cookies, &http.Cookie{Name: entry, Value: val})
	}
	if len(cookies) == 0 {
		return nil, errors.New("cardigann: cookie login: no cookie configured (the cookie setting is empty)")
	}
	return &Session{Cookies: cookies, ExpiresAt: e.now().Add(sessionTTL)}, nil
}

// parseCookieHeader reads a Cookie header the way a user pastes one --
// Prowlarr's CookieUtil.CookieHeaderToDictionary: pairs split on ";",
// surrounding space and empty pairs ignored (a trailing "; " is common),
// the value everything after the first "=". http.ParseCookie refuses all of
// that.
func parseCookieHeader(h string) []*http.Cookie {
	var out []*http.Cookie
	for _, part := range strings.Split(h, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		out = append(out, &http.Cookie{Name: name, Value: strings.TrimSpace(value)})
	}
	return out
}

// runLoginTest GETs test.Path with sess and search.headers attached (what
// Jackett's TestLogin sends) and asserts test.Selector matches, confirming a
// Session actually authenticates. A redirect is not followed: a dead session
// is redirected to the login page, which is exactly what the test exists to
// notice. An HTTP error status fails the test, and the selector is checked
// only on an HTML response -- both as Prowlarr's CheckIfLoginIsNeeded does
// (HasHttpRedirect, HasHttpError, and `ContentType?.Contains("text/html")
// ?? true`).
func (e Engine) runLoginTest(ctx context.Context, def *Definition, cfg Config, sess *Session, test *PageTestBlock) error {
	cfg.Session = sess
	u, err := resolveURL(cfg.BaseURL, test.Path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("cardigann: build request: %w", RedactErr(err))
	}
	if err := renderHeaders(req, def.Search.Headers, e.templateContext(def, cfg)); err != nil {
		return err
	}
	attachSession(req, sess)
	resp, body, err := e.do(ctx, req, exchange{def: def, site: cfg.BaseURL})
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return &LoginError{Message: "login test page redirected"}
	}
	if resp.StatusCode >= 400 {
		return &LoginError{Message: fmt.Sprintf("login test page returned HTTP %d", resp.StatusCode)}
	}
	if test.Selector == "" {
		return nil
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "text/html") {
		return nil
	}
	enc, _ := def.textEncoding()
	if body, err = decodeBody(enc, body); err != nil {
		return err
	}
	doc, err := ParseDoc(ResponseHTML, body)
	if err != nil {
		return fmt.Errorf("cardigann: parse login test page: %w", err)
	}
	if _, ok := doc.Select(test.Selector); !ok {
		return &LoginError{Message: fmt.Sprintf("login test selector %q did not match", test.Selector)}
	}
	return nil
}

// checkLoginErrors parses body as HTML (login pages are always HTML per
// the corpus and schema) and, for each ErrorBlock, treats a matching
// Selector as failure — using Message's rendered text (or a generic
// message) as LoginError.Message.
func checkLoginErrors(body []byte, errs []ErrorBlock, tc *TemplateContext) error {
	if len(errs) == 0 {
		return nil
	}
	doc, err := ParseDoc(ResponseHTML, body)
	if err != nil {
		return fmt.Errorf("cardigann: parse error-check page: %w", err)
	}
	for _, eb := range errs {
		if eb.Selector == "" {
			continue
		}
		matched, ok := doc.Select(eb.Selector)
		if !ok {
			continue
		}
		msg := ""
		if eb.Message != nil {
			if rendered, mok, merr := eb.Message.Extract(context.Background(), matched, tc); merr == nil && mok {
				msg = rendered
			}
		}
		if msg == "" {
			// The matched element's own text, as Prowlarr's CheckForError
			// does and as checkSearchErrors does: "Invalid username or
			// password" is what an operator needs in the Authenticated
			// condition, and "login failed" says nothing the reason does not.
			msg, _ = matched.Text("")
		}
		msg = strings.Join(strings.Fields(msg), " ")
		if msg == "" {
			msg = "login failed"
		}
		return &LoginError{Message: truncateRunes(msg, maxSearchErrorMessage)}
	}
	return nil
}
