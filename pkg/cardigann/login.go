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
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
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

// CaptchaRequiredError signals that def.Login.Captcha is set and the login
// page actually served a captcha challenge. pkg/cardigann never solves
// captchas; Engine.Login returns this instead of attempting the submit so
// the indexer controller can surface it (Indexer.status condition
// Authenticated=False, reason CaptchaRequired) rather than looping forever.
type CaptchaRequiredError struct{ Type string } // "image" | "text"

func (e *CaptchaRequiredError) Error() string {
	return "cardigann: captcha required (" + e.Type + ")"
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
func (e Engine) Login(ctx context.Context, def *Definition, cfg Config) (*Session, error) {
	lb := def.Login
	if lb == nil {
		return nil, nil
	}

	if lb.Captcha != nil {
		found, err := e.captchaPresent(ctx, cfg, lb)
		if err != nil {
			return nil, err
		}
		if found {
			return nil, &CaptchaRequiredError{Type: lb.Captcha.Type}
		}
	}

	switch lb.Method {
	case "", "form":
		return e.loginForm(ctx, def, cfg, lb)
	case "cookie":
		return e.loginCookie(ctx, cfg, lb)
	case "post":
		return e.loginPost(ctx, def, cfg, lb)
	case "get":
		return e.loginGet(ctx, def, cfg, lb)
	case "oneurl":
		return e.loginOneURL(ctx, def, cfg, lb)
	default:
		return nil, fmt.Errorf("cardigann: unknown login method %q", lb.Method)
	}
}

// captchaPresent GETs lb.Path (the login page) and reports whether
// lb.Captcha.Selector matches — i.e. whether the site actually served a
// captcha challenge this time, as opposed to merely declaring that it
// might.
func (e Engine) captchaPresent(ctx context.Context, cfg Config, lb *LoginBlock) (bool, error) {
	body, err := e.get(ctx, cfg, lb.Path)
	if err != nil {
		return false, err
	}
	doc, err := ParseDoc(ResponseHTML, body)
	if err != nil {
		return false, fmt.Errorf("cardigann: parse login page: %w", err)
	}
	_, ok := doc.Select(lb.Captcha.Selector)
	return ok, nil
}

// loginForm GETs lb.Path, scrapes lb.SelectorInputs (e.g. a CSRF token)
// out of that page alongside lb.Inputs (rendered), and POSTs the combined
// form to lb.SubmitPath (or lb.Path when unset).
func (e Engine) loginForm(ctx context.Context, def *Definition, cfg Config, lb *LoginBlock) (*Session, error) {
	tc := e.templateContext(def, cfg)
	page, err := e.get(ctx, cfg, lb.Path)
	if err != nil {
		return nil, err
	}
	doc, err := ParseDoc(ResponseHTML, page)
	if err != nil {
		return nil, fmt.Errorf("cardigann: parse login page: %w", err)
	}

	form := url.Values{}
	for k, v := range lb.Inputs {
		rendered, err := render(string(v), tc)
		if err != nil {
			return nil, err
		}
		form.Set(k, rendered)
	}
	for name, sel := range lb.SelectorInputs {
		val, ok, err := sel.Extract(ctx, doc, tc)
		if err != nil {
			return nil, err
		}
		if !ok && !sel.Optional {
			return nil, fmt.Errorf("cardigann: login selector input %q not found", name)
		}
		form.Set(name, val)
	}

	submitPath := lb.SubmitPath
	if submitPath == "" {
		submitPath = lb.Path
	}
	resp, respBody, err := e.postForm(ctx, cfg, submitPath, form)
	if err != nil {
		return nil, err
	}
	if err := checkLoginErrors(respBody, lb.Error, tc); err != nil {
		return nil, err
	}
	return &Session{Cookies: resp.Cookies(), ExpiresAt: e.now().Add(sessionTTL)}, nil
}

// loginPost is loginForm minus the initial page GET and SelectorInputs
// scrape: lb.Inputs (rendered) POST directly to lb.Path/lb.SubmitPath.
func (e Engine) loginPost(ctx context.Context, def *Definition, cfg Config, lb *LoginBlock) (*Session, error) {
	tc := e.templateContext(def, cfg)
	form := url.Values{}
	for k, v := range lb.Inputs {
		rendered, err := render(string(v), tc)
		if err != nil {
			return nil, err
		}
		form.Set(k, rendered)
	}
	submitPath := lb.SubmitPath
	if submitPath == "" {
		submitPath = lb.Path
	}
	resp, respBody, err := e.postForm(ctx, cfg, submitPath, form)
	if err != nil {
		return nil, err
	}
	if err := checkLoginErrors(respBody, lb.Error, tc); err != nil {
		return nil, err
	}
	return &Session{Cookies: resp.Cookies(), ExpiresAt: e.now().Add(sessionTTL)}, nil
}

// loginGet is a GET to lb.Path with lb.Inputs as query parameters plus
// lb.Headers, checked against lb.Error.
func (e Engine) loginGet(ctx context.Context, def *Definition, cfg Config, lb *LoginBlock) (*Session, error) {
	tc := e.templateContext(def, cfg)
	u, err := resolveURL(cfg.BaseURL, lb.Path)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, fmt.Errorf("cardigann: login get url %q: %w", redactRawURL(u), RedactErr(err))
	}
	q := parsed.Query()
	for k, v := range lb.Inputs {
		rendered, err := render(string(v), tc)
		if err != nil {
			return nil, err
		}
		q.Set(k, rendered)
	}
	parsed.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("cardigann: build request: %w", RedactErr(err))
	}
	if err := renderHeaders(req, lb.Headers, tc); err != nil {
		return nil, err
	}
	attachSession(req, cfg.Session)

	resp, respBody, err := e.do(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := checkLoginErrors(respBody, lb.Error, tc); err != nil {
		return nil, err
	}
	return &Session{Cookies: resp.Cookies(), ExpiresAt: e.now().Add(sessionTTL)}, nil
}

// loginOneURL is loginGet's request shape but treats the single fetched
// page as both the login action and its own test (no separate Test block
// is meaningful for a tracker whose "login" is one URL carrying the API
// key).
func (e Engine) loginOneURL(ctx context.Context, def *Definition, cfg Config, lb *LoginBlock) (*Session, error) {
	return e.loginGet(ctx, def, cfg, lb)
}

// loginCookie reads each name in lb.Cookies out of cfg.Values (a plain
// string setting, or — since login.cookies names arbitrary cookie names,
// not setting names — a raw value ResolveSettings passed through
// unchanged) and builds Session.Cookies directly, erroring when a listed
// name has no value. There is no HTTP round trip for the login step
// itself: the user supplies the cookie value directly (note §3.5).
func (e Engine) loginCookie(ctx context.Context, cfg Config, lb *LoginBlock) (*Session, error) {
	var cookies []*http.Cookie
	for _, name := range lb.Cookies {
		val, ok := cfg.stringValue(name)
		if !ok || val == "" {
			return nil, fmt.Errorf("cardigann: login cookie %q has no configured value", name)
		}
		cookies = append(cookies, &http.Cookie{Name: name, Value: val})
	}
	sess := &Session{Cookies: cookies, ExpiresAt: e.now().Add(sessionTTL)}
	if lb.Test != nil {
		if err := e.runLoginTest(ctx, cfg, sess, lb.Test); err != nil {
			return nil, err
		}
	}
	return sess, nil
}

// runLoginTest GETs test.Path (with sess attached) and asserts
// test.Selector matches, confirming a Session actually authenticates.
func (e Engine) runLoginTest(ctx context.Context, cfg Config, sess *Session, test *PageTestBlock) error {
	cfg.Session = sess
	body, err := e.get(ctx, cfg, test.Path)
	if err != nil {
		return err
	}
	doc, err := ParseDoc(ResponseHTML, body)
	if err != nil {
		return fmt.Errorf("cardigann: parse login test page: %w", err)
	}
	if _, ok := doc.Select(test.Selector); !ok {
		return &LoginError{Message: "login test selector did not match"}
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
