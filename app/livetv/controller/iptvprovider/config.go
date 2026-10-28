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

package iptvprovider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/iptv"
	"github.com/mediactl/clustarr/pkg/iptv/filter"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// resolved is a provider's server and credentials, its Secret read.
type resolved struct {
	server, username, password string
}

// NormalizeServer is spec.server as the panel's root: a scheme (https when
// it names none), a host[:port] and any path, with no trailing slash, query,
// fragment or userinfo.
func NormalizeServer(server string) (string, error) {
	s := strings.TrimSpace(server)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return "", err
	case u.Scheme != "http" && u.Scheme != "https":
		return "", fmt.Errorf("scheme %q is neither http nor https", u.Scheme)
	case u.Host == "":
		return "", errors.New("no host")
	case u.User != nil, u.RawQuery != "", u.Fragment != "":
		return "", errors.New("credentials, a query or a fragment do not belong in server; the credentials go in credentialsRef")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.EscapedPath(), "/"), nil
}

// ValidateSourceURL checks spec.guideURL and spec.playlistURL: an http or
// https URL with a host, its credential placeholders left in (they parse as
// path or query text).
func ValidateSourceURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case err != nil:
		return errors.New("does not parse as a URL")
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("scheme %q is neither http nor https", u.Scheme)
	case u.Host == "":
		return errors.New("no host")
	case u.User != nil:
		return errors.New("credentials do not belong in the URL's userinfo; name {username} and {password} instead")
	}
	return nil
}

// resolve reads the provider's server and credentials. A bad server, or a
// missing Secret or key, is a Valid=False condition naming it, never a
// credential.
func (r *Reconciler) resolve(ctx context.Context, p *clustarrv1.IPTVProvider) (resolved, metav1.Condition) {
	invalid := func(reason, format string, args ...any) (resolved, metav1.Condition) {
		return resolved{}, metav1.Condition{Type: clustarrv1.ConditionValid, Status: metav1.ConditionFalse, Reason: reason, Message: fmt.Sprintf(format, args...)}
	}
	m3u := p.Spec.TypeOrDefault() == clustarrv1.ProviderTypeM3U
	var out resolved
	if m3u {
		if err := ValidateSourceURL(p.Spec.PlaylistURL); err != nil {
			return invalid(clustarrv1.ReasonInvalidPlaylistURL, "playlistURL %s: %v", iptv.Redact(p.Spec.PlaylistURL), err)
		}
	} else {
		server, err := NormalizeServer(p.Spec.Server)
		if err != nil {
			return invalid(clustarrv1.ReasonInvalidServer, "server %q: %v", p.Spec.Server, err)
		}
		out.server = server
	}
	if g := p.Spec.GuideURL; g != "" {
		if err := ValidateSourceURL(g); err != nil {
			return invalid(clustarrv1.ReasonInvalidGuideURL, "guideURL %s: %v", iptv.Redact(g), err)
		}
	}
	name := p.Spec.CredentialsRef.Name
	if m3u && name == "" {
		// An M3U provider whose URLs name no credential needs no Secret.
		return out, metav1.Condition{Type: clustarrv1.ConditionValid, Status: metav1.ConditionTrue, Reason: "Valid", Message: "the playlist URL parses"}
	}
	var s corev1.Secret
	err := r.SecretReader.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: name}, &s)
	switch {
	case apierrors.IsNotFound(err):
		return invalid(clustarrv1.ReasonSecretNotFound, "Secret %s/%s does not exist", p.Namespace, name)
	case err != nil:
		return invalid(clustarrv1.ReasonSecretNotFound, "Secret %s/%s cannot be read: %v", p.Namespace, name, err)
	}
	for key, dst := range map[string]*string{clustarrv1.CredentialsKeyUsername: &out.username, clustarrv1.CredentialsKeyPassword: &out.password} {
		// kubectl --from-file and a YAML "|" block both end the value in a
		// newline, which no credential carries.
		v := bytes.TrimSpace(s.Data[key])
		if len(v) == 0 {
			return invalid(clustarrv1.ReasonSecretKeyNotFound, "Secret %s/%s has no key %q", p.Namespace, name, key)
		}
		*dst = string(v)
	}
	msg := "the server parses and the credentials Secret holds both keys"
	if m3u {
		msg = "the playlist URL parses and the credentials Secret holds both keys"
	}
	return out, metav1.Condition{Type: clustarrv1.ConditionValid, Status: metav1.ConditionTrue, Reason: "Valid", Message: msg}
}

// buildConfig is the provider as the tuner sees it: every spec field read
// through its accessor, its credentials resolved, its channels mapped, its
// blocks as planned.
func buildConfig(p *clustarrv1.IPTVProvider, blocks []tuner.BlockConfig, res resolved, channels []clustarrv1.IPTVChannel) tuner.Config {
	c := tuner.Config{
		Key:                 types.NamespacedName{Namespace: p.Namespace, Name: p.Name},
		Blocks:              blocks,
		Type:                string(p.Spec.TypeOrDefault()),
		Server:              res.server,
		PlaylistURLTemplate: p.Spec.PlaylistURL,
		Username:            res.username,
		Password:            res.password,
		GuideURL:            p.Spec.GuideURL,
		Tuners:              p.Spec.TunersOverride(),
		UserAgent:           p.Spec.UserAgentOrDefault(),
		Every:               p.Spec.RefreshOrDefault(),
		EPGSource:           string(p.Spec.EPGSourceOrDefault()),
		Buffer:              int(p.Spec.Stream.BufferOrDefault()),
		Linger:              p.Spec.Stream.LingerOrDefault(),
		Language:            p.Spec.Stream.LanguageOrDefault(),
		Refresh:             p.Annotations[clustarrv1.AnnotationLiveTVRefresh],
	}
	for _, f := range p.Spec.Filters {
		c.Filters = append(c.Filters, filter.Rule{
			Name: f.Name, Type: string(f.Type), Match: f.Match, Include: f.Include,
			Exclude: f.Exclude, CaseSensitive: f.CaseSensitive, Enabled: f.EnabledOrDefault(),
		})
	}
	for i := range channels {
		ch := &channels[i]
		cc := tuner.ChannelConfig{
			Object: ch.Name, Key: ch.Spec.Key, Number: ch.Spec.Number, Name: ch.Spec.Name,
			Group: ch.Spec.Group, Logo: ch.Spec.Logo, Active: ch.Spec.Active, TimeshiftMinutes: int(ch.Spec.TimeshiftMinutes),
			Language: ch.Spec.Language,
		}
		if e := ch.Spec.EPG; e != nil {
			if e.Dummy {
				cc.DummyMinutes = int(e.DummyMinutesOrDefault())
			} else if e.ChannelID != "" {
				cc.Map = &xmltv.Mapping{Guide: tuner.GuideName, ChannelID: e.ChannelID}
			}
		}
		c.Channels = append(c.Channels, cc)
	}
	return c
}
