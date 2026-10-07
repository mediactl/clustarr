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

package lifecycle

import (
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// ParseIntents parses an owner's download annotations (§6.10). Entry ids are
// DNS subdomains, so commas and spaces separate them. A one-shot value that
// does not parse is listed in Invalid and its intent left nil.
func ParseIntents(annotations map[string]string) Intents {
	in := Intents{Paused: map[string]bool{}, Priority: map[string]string{}, raw: map[string]string{}}
	if v := annotations[catalogv1alpha1.AnnotationDownloadPaused]; v != "" {
		for _, id := range fields(v) {
			in.Paused[id] = true
		}
	}
	if v := annotations[catalogv1alpha1.AnnotationDownloadPriority]; v != "" {
		for _, kv := range fields(v) {
			id, p, ok := strings.Cut(kv, "=")
			switch commonv1.DownloadPriority(p) {
			case commonv1.DownloadPriorityHigh, commonv1.DownloadPriorityNormal, commonv1.DownloadPriorityLow:
			default:
				ok = false
			}
			if !ok || id == "" {
				in.Invalid = append(in.Invalid, InvalidIntent{
					Annotation: catalogv1alpha1.AnnotationDownloadPriority, Value: v,
					Reason: "priority wants <id>=high|normal|low",
				})
				continue
			}
			in.Priority[id] = p
		}
	}
	for _, a := range []string{
		catalogv1alpha1.AnnotationDownloadRemove, catalogv1alpha1.AnnotationDownloadResume,
		catalogv1alpha1.AnnotationDownloadImport, catalogv1alpha1.AnnotationDownloadUnblock,
	} {
		v, ok := annotations[a]
		if !ok || v == "" {
			continue
		}
		in.raw[a] = v
		if reason := in.parseOneShot(a, v); reason != "" {
			in.Invalid = append(in.Invalid, InvalidIntent{Annotation: a, Value: v, Reason: reason})
		}
	}
	return in
}

// parseOneShot parses one one-shot annotation into in; it returns why the
// value is invalid, or "".
func (in *Intents) parseOneShot(a, v string) string {
	f := strings.Fields(v)
	if len(f) < 2 {
		return "wants <nonce> <id> ..."
	}
	nonce := f[0]
	if !catalogv1alpha1.ValidNonce(nonce) {
		return "the nonce is not ^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$"
	}
	rest := f[2:]
	switch a {
	case catalogv1alpha1.AnnotationDownloadRemove:
		r := &RemoveIntent{Nonce: nonce, ID: f[1]}
		for _, t := range rest {
			switch t {
			case "data":
				r.Data = true
			case "blocklist":
				r.Blocklist = true
			default:
				return "remove takes only data and blocklist after the id"
			}
		}
		in.Remove = r
	case catalogv1alpha1.AnnotationDownloadResume:
		if len(rest) > 0 {
			return "resume takes only <nonce> <id>"
		}
		in.Resume = &ResumeIntent{Nonce: nonce, ID: f[1]}
	case catalogv1alpha1.AnnotationDownloadImport:
		im := &ImportIntent{Nonce: nonce, ID: f[1]}
		for _, t := range rest {
			switch {
			case t == "override":
				im.Override = true
			case strings.HasPrefix(t, "target="):
				parts := strings.Split(strings.TrimPrefix(t, "target="), "/")
				if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
					return "target wants <kind>/<name>[/<key>]"
				}
				im.Target = &schema.ItemRef{Kind: kindName(parts[0]), Ref: schema.Ref{Name: parts[1]}}
				if len(parts) == 3 {
					im.TargetKey = parts[2]
				}
			default:
				return "import takes only target=<kind>/<name>[/<key>] and override after the id"
			}
		}
		in.Import = im
	case catalogv1alpha1.AnnotationDownloadUnblock:
		u := &UnblockIntent{Nonce: nonce}
		if indexer, guid, ok := strings.Cut(f[1], "/"); ok {
			if indexer == "" || guid == "" {
				return "unblock wants <infoHash> or <indexer>/<guid>"
			}
			u.Indexer, u.GUID = indexer, guid
		} else {
			u.InfoHash = strings.ToLower(f[1])
		}
		for _, t := range rest {
			if t != "global" {
				return "unblock takes only global after the release"
			}
			u.Global = true
		}
		in.Unblock = u
	}
	return ""
}

// NonceKey is what an owner records in downloadNonces for annotation a's
// value: the nonce of a value that parsed, else HandledNonce of the value
// (an invalid one is handled once, with one Warning).
func (in Intents) NonceKey(a string) string {
	v := in.raw[a]
	if v == "" {
		return ""
	}
	switch a {
	case catalogv1alpha1.AnnotationDownloadRemove:
		if in.Remove != nil {
			return in.Remove.Nonce
		}
	case catalogv1alpha1.AnnotationDownloadResume:
		if in.Resume != nil {
			return in.Resume.Nonce
		}
	case catalogv1alpha1.AnnotationDownloadImport:
		if in.Import != nil {
			return in.Import.Nonce
		}
	case catalogv1alpha1.AnnotationDownloadUnblock:
		if in.Unblock != nil {
			return in.Unblock.Nonce
		}
	}
	return catalogv1alpha1.HandledNonce(v)
}

// pending reports annotation a's value as not yet handled against the
// recorded nonce.
func (in Intents) pending(a, recorded string) bool {
	k := in.NonceKey(a)
	return k != "" && k != recorded
}

// fields splits on commas and whitespace.
func fields(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
}

// kindName is a target kind as an object Kind: "movie" and "Movie" both read
// "Movie".
func kindName(k string) string {
	switch strings.ToLower(k) {
	case "movie":
		return "Movie"
	case "series":
		return "Series"
	case "episode":
		return "Episode"
	case "album":
		return "Album"
	case "book":
		return "Book"
	case "audiobook":
		return "Audiobook"
	case "comic":
		return "Comic"
	case "issue":
		return "Issue"
	}
	return k
}
