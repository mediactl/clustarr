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

// Package grouplang learns the language a release group's dual-audio
// titles carry beside the original (release.GroupLanguages) from the files
// the library imports. The manager records each imported file's probed
// audio against its release group in the clustarr-release-groups bucket
// (Observe), before a transcode drops the languages it does not keep, and
// every process reads the bucket through an Index on its root context
// (release.WithGroupLanguages), which falls back to the built-in table.
package grouplang

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// maxSeen caps a record's observation keys: enough to absorb the redeliveries
// and re-inspections of an import, small enough for any group's record.
const maxSeen = 64

// Record is one release group's observed audio, at
// events.KVKeyToken(release.NormalizeGroup(group)).
type Record struct {
	Group string `json:"group"`
	// Files counts the files observed with known audio.
	Files int `json:"files"`
	// Votes counts, by canonical tag, the files that carried exactly one
	// language beside English and their item's original: a dual-audio file
	// of the group's own language.
	Votes map[string]int `json:"votes,omitempty"`
	// Multi counts the files that carried several such languages, a
	// multi-language release that says nothing about the group's own.
	Multi int `json:"multi,omitempty"`
	// Seen are the latest observation keys, so an import observed twice
	// counts once.
	Seen      []string  `json:"seen,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Verdict is the language r's group carries beside the original, a
// canonical tag: the one its single-extra-language files name at least
// twice, for at least two thirds of those files and a third of every file
// observed. A group whose files carry no extra language, or several, has
// none.
func (r Record) Verdict() (string, bool) {
	top, n, total := "", 0, 0
	for tag, v := range r.Votes {
		total += v
		if v > n || (v == n && tag < top) {
			top, n = tag, v
		}
	}
	if n < 2 || 3*n < 2*total || 3*n < r.Files {
		return "", false
	}
	return top, true
}

// Extra is the languages of audio (canonical tags) beside English and the
// original language originalTag, by base language, each once.
func Extra(audio []string, originalTag string) []string {
	orig := base(originalTag)
	var out []string
	for _, a := range audio {
		b := base(a)
		if b == "" || b == "en" || b == orig || slices.Contains(out, b) {
			continue
		}
		out = append(out, b)
	}
	return out
}

func base(tag string) string {
	t, ok := lang.Normalize(tag)
	if !ok {
		return ""
	}
	b, _, _ := strings.Cut(string(t), "-")
	return strings.ToLower(b)
}

// Observe records one file of group whose probed audio is audio (canonical
// tags; nil when unknown, which records nothing) against its item's
// original language, once per key, by compare-and-swap.
func Observe(ctx context.Context, kv events.KV, group, key string, audio []string, originalTag string) error {
	g := release.NormalizeGroup(group)
	if g == "" || key == "" || len(audio) == 0 {
		return nil
	}
	extra := Extra(audio, originalTag)
	k := events.KVKeyToken(g)
	for range 5 {
		var r Record
		var rev uint64
		e, err := kv.Get(ctx, k)
		switch {
		case err == nil:
			if jerr := json.Unmarshal(e.Value, &r); jerr != nil {
				r = Record{}
			}
			rev = e.Revision
		case !errors.Is(err, events.ErrKeyNotFound):
			return err
		}
		if slices.Contains(r.Seen, key) {
			return nil
		}
		r.Group = group
		r.Files++
		switch len(extra) {
		case 0:
		case 1:
			if r.Votes == nil {
				r.Votes = map[string]int{}
			}
			r.Votes[extra[0]]++
		default:
			r.Multi++
		}
		r.Seen = append(r.Seen, key)
		if len(r.Seen) > maxSeen {
			r.Seen = r.Seen[len(r.Seen)-maxSeen:]
		}
		r.UpdatedAt = time.Now().UTC()
		val, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if rev == 0 {
			_, err = kv.Create(ctx, k, val)
		} else {
			_, err = kv.Update(ctx, k, val, rev)
		}
		if errors.Is(err, events.ErrKeyExists) || errors.Is(err, events.ErrRevisionMismatch) {
			continue
		}
		return err
	}
	return errors.New("grouplang: observe " + group + ": the record kept changing")
}

// Index is the learned group languages, read from the bucket, over the
// built-in table: a group with a verdict answers it, any other group what
// the built-in table says. It answers from the built-in table until Run has
// read the bucket.
type Index struct {
	mu      sync.RWMutex
	learned map[string]string // release.NormalizeGroup -> English display name
}

var _ release.GroupLanguages = (*Index)(nil)

// NewIndex is an empty Index.
func NewIndex() *Index { return &Index{learned: map[string]string{}} }

// GroupLanguage implements release.GroupLanguages.
func (ix *Index) GroupLanguage(group string) (string, bool) {
	ix.mu.RLock()
	l, ok := ix.learned[release.NormalizeGroup(group)]
	ix.mu.RUnlock()
	if ok {
		return l, true
	}
	return release.BuiltinGroupLanguages().GroupLanguage(group)
}

// apply folds one bucket entry into the index.
func (ix *Index) apply(e events.Entry) {
	var r Record
	name, ok := "", false
	if e.Value != nil && json.Unmarshal(e.Value, &r) == nil && r.Group != "" {
		if tag, v := r.Verdict(); v {
			name, ok = catalogue.LanguageName(tag)
		}
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	g := release.NormalizeGroup(r.Group)
	if g == "" {
		return
	}
	if ok {
		ix.learned[g] = name
	} else {
		delete(ix.learned, g)
	}
}

// rewatchDelay is how long Run waits to watch the bucket again after a
// watch failed or ended.
const rewatchDelay = 10 * time.Second

// Run watches the bucket into the index until ctx ends, watching again
// after a failure. It is a controller-runtime runnable's body.
func (ix *Index) Run(ctx context.Context, kv events.KV) error {
	log := logging.FromContext(ctx)
	for {
		ch, err := kv.Watch(ctx, ">")
		if err != nil {
			log.Warn("grouplang: watching the release groups failed; retrying", "error", err)
		} else {
			for e := range ch {
				ix.apply(e)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(rewatchDelay):
		}
	}
}
