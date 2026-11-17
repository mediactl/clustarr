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

package forms

import (
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"

	"github.com/mediactl/clustarr/pkg/cardigann"
)

// The Indexer form's Definition section (owner, 2026-11-16: "The Cardigann
// definition select in the indexer settings pane should fill all known
// settings defined by its definition file"). A Cardigann definition declares
// its own settings -- text, select, checkbox, password and notes -- and the
// form draws each as an input of its type instead of a free key/value table:
// a non-secret one posts into spec.settings under its name, a secret one
// into the Indexer's Secret under the same name, which the engine reads over
// spec.settings (app/indexer/clients' cardigannSettings).

// DefinitionSectionTitle heads the section.
const DefinitionSectionTitle = "Definition"

// DefinitionURL is where the page fetches the section again when the
// definition choice changes.
const DefinitionURL = "/settings/indexer-definition"

// definitionIndexBase is the first map index a declared setting posts under,
// far past any row the free "Other settings" table numbers from 0.
const definitionIndexBase = 1000

// secretKeyName is what a Secret's data key may be.
var secretKeyName = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

// sensitiveName matches a text setting that is a credential whatever its
// declared type: definitions declare a cookie, a passkey or an RSS key as
// plain text.
var sensitiveName = regexp.MustCompile(`(?i)(pass|key|cookie|token|secret|pin$|^user(name)?$|^email$|2fa|otp)`)

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// plainText is an info setting's text without its markup.
func plainText(s string) string {
	s = htmlTag.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}

// SecretSetting reports whether a declared setting is kept in the Secret.
func SecretSetting(f cardigann.SettingsField) bool {
	switch f.Type {
	case "password":
		return secretKeyName.MatchString(f.Name)
	case "text":
		return sensitiveName.MatchString(f.Name) && secretKeyName.MatchString(f.Name)
	}
	return false
}

// DefinitionInfo is what the section says about a definition beside its
// settings.
type DefinitionInfo struct {
	// Protocol is the definition's transfer protocol ("torrent").
	Protocol string
	// Privacy is its class: public, semiPrivate or private.
	Privacy string
}

// DefinitionSection builds the section for def with the indexer's stored
// spec.settings. A nil def is no definition chosen: the section then holds
// only what must not be lost, the stored settings as a free table.
func DefinitionSection(def *cardigann.Definition, info DefinitionInfo, settings map[string]string) Section {
	s := Section{
		Title: DefinitionSectionTitle,
		Help:  "The chosen definition's own settings. A credential is kept in the indexer's Secret under the setting's name and is never shown again.",
		When:  unless("definition", ""),
		Dynamic: &Dynamic{
			URL:     DefinitionURL,
			Watch:   "definition",
			Include: []string{"definition", "__name", "__namespace"},
		},
	}
	s.Controls = DefinitionControls(def, info, settings)
	return s
}

// DefinitionControls are the section's controls.
func DefinitionControls(def *cardigann.Definition, info DefinitionInfo, settings map[string]string) []Control {
	if def == nil {
		out := []Control{{Type: ControlHidden, Name: MetaProtocol}}
		if len(settings) > 0 {
			out = append(out, otherSettings(settings, nil))
		}
		return out
	}
	out := []Control{{Type: ControlHidden, Name: MetaProtocol, Value: info.Protocol}}
	if len(def.Links) > 0 {
		out = append(out, Control{Type: ControlHidden, Name: "__fill.baseURL", Value: def.Links[0], Fill: "baseURL"})
	}
	out = append(out, Control{Type: ControlNote, Label: def.Name, Help: definitionSummary(def, info), Wide: true})

	// declared are the definition's setting names; drawn those of them an
	// input here posts into spec.settings. A credential posts into the
	// Secret instead, so one an indexer keeps in spec.settings (written
	// there by hand) is no input's to post: it stays in the free table,
	// where a save keeps it.
	declared, drawn := map[string]bool{}, map[string]bool{}
	for i, f := range def.Settings {
		if f.Name != "" {
			declared[f.Name] = true
			drawn[f.Name] = !SecretSetting(f) && !strings.HasPrefix(f.Type, "info")
		}
		label := f.Label
		if label == "" {
			label = Humanize(f.Name)
		}
		key := fmt.Sprintf("settings.__k.%d", definitionIndexBase+i)
		val := fmt.Sprintf("settings.__v.%d", definitionIndexBase+i)
		stored, has := settings[f.Name]
		switch {
		case strings.HasPrefix(f.Type, "info"):
			if note := infoNote(f, label); note.Help != "" || note.Label != "" {
				out = append(out, note)
			}
		case SecretSetting(f):
			out = append(out, Control{Type: ControlPassword, Name: "__secret.secretRef." + f.Name, Label: label})
		case f.Type == "checkbox":
			checked := string(f.Default) == "true"
			if has {
				checked = stored == "true"
			}
			out = append(out,
				Control{Type: ControlHidden, Name: key, Value: f.Name},
				Control{Type: ControlCheckbox, Name: val, Label: label, Checked: checked})
		case f.Type == "select":
			c := Control{Type: ControlSelect, Name: val, Label: label, Value: string(f.Default)}
			if _, known := f.Options[stored]; has && known {
				c.Value = stored
			}
			for v, l := range f.Options {
				c.Options = append(c.Options, Option{Value: v, Label: l})
			}
			sort.Slice(c.Options, func(a, b int) bool {
				if c.Options[a].Label != c.Options[b].Label {
					return c.Options[a].Label < c.Options[b].Label
				}
				return c.Options[a].Value < c.Options[b].Value
			})
			out = append(out, Control{Type: ControlHidden, Name: key, Value: f.Name}, c)
		default:
			// text, and any type this form does not know: a value by name.
			out = append(out,
				Control{Type: ControlHidden, Name: key, Value: f.Name},
				Control{Type: ControlText, Name: val, Label: label, Value: stored, Placeholder: string(f.Default)})
		}
	}
	if def.Login != nil && !declared["cookie"] {
		out = append(out, Control{
			Type: ControlPassword, Name: "__secret.secretRef.cookie", Label: "Cookie",
			Help: "A logged-in session cookie, taken in place of the login: for a login behind a captcha.",
		})
	}
	if other := otherSettings(settings, drawn); len(other.Pairs) > 0 || len(def.Settings) == 0 {
		out = append(out, other)
	}
	return out
}

// MissingDefinitionNote says a named definition is not among the loaded
// ones, so its settings can only be given by name.
func MissingDefinitionNote(id string) Control {
	return Control{
		Type: ControlNote, Wide: true, Label: "No definition " + id,
		Help: "No loaded definition has this id, so its settings are not known here; give them by name under Other settings.",
	}
}

// otherSettings is the free key/value table of the stored settings no
// input of the section posts: drawn names the ones that have one.
func otherSettings(settings map[string]string, drawn map[string]bool) Control {
	c := Control{
		Type: ControlMap, Name: "settings", Label: "Other settings", Wide: true,
		Help: "Settings by name the definition does not declare, or that are kept here rather than in the Secret.",
	}
	keys := make([]string, 0, len(settings))
	for k := range settings {
		if !drawn[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		c.Pairs = append(c.Pairs, Pair{Key: k, Value: settings[k]})
	}
	return c
}

// definitionSummary is one line on what the definition is and where it
// lives.
func definitionSummary(def *cardigann.Definition, info DefinitionInfo) string {
	var parts []string
	what := strings.TrimSpace(strings.Join([]string{privacyLabel(info.Privacy), info.Protocol, "indexer"}, " "))
	if def.Language != "" {
		what += ", " + def.Language
	}
	parts = append(parts, strings.ToUpper(what[:1])+what[1:]+".")
	if d := plainText(def.Description); d != "" {
		if !strings.HasSuffix(d, ".") {
			d += "."
		}
		parts = append(parts, d)
	}
	if len(def.Links) > 1 {
		parts = append(parts, "Addresses: "+strings.Join(def.Links, ", ")+".")
	}
	return strings.Join(parts, " ")
}

func privacyLabel(p string) string {
	switch p {
	case "semiPrivate", "semi-private":
		return "semi-private"
	}
	return p
}

// infoNote renders an info setting: the definition's own text, or for the
// four kinds Cardigann gives no text a line saying what they mean here.
func infoNote(f cardigann.SettingsField, label string) Control {
	c := Control{Type: ControlNote, Label: label, Help: plainText(string(f.Default)), Wide: true}
	switch f.Type {
	case "info_cookie":
		c.Label, c.Help = "Cookie", "Log in to the site in a browser and copy the request's Cookie header into the Cookie setting."
	case "info_flaresolverr":
		c.Label, c.Help = "FlareSolverr", "The site may sit behind a challenge page: give the indexer a FlareSolverr proxy if its requests are refused."
	case "info_useragent":
		c.Label, c.Help = "User agent", "The cookie is tied to the browser that made it: the site may refuse it from another user agent."
	case "info_category_8000":
		c.Label, c.Help = "Categories", "The site has categories of its own beside the standard ones (8000 and up)."
	}
	return c
}

// PruneDefinitionDefaults drops from settings what only restates def's
// defaults, so a saved indexer records the choices made rather than a copy
// of the definition (which may change its defaults): a text or select equal
// to its default, a checkbox in its default state.
func PruneDefinitionDefaults(def *cardigann.Definition, settings map[string]any) {
	if def == nil {
		return
	}
	for _, f := range def.Settings {
		v, ok := settings[f.Name].(string)
		if !ok {
			continue
		}
		switch f.Type {
		case "checkbox":
			if (v == "true") == (string(f.Default) == "true") {
				delete(settings, f.Name)
			}
		case "select", "text":
			if v == string(f.Default) {
				delete(settings, f.Name)
			}
		}
	}
}
