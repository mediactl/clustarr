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

package naming

import (
	"strings"
	"unicode/utf8"
)

// Filesystem limits SanitizeOptions defaults against.
const (
	DefaultMaxComponentBytes = 255  // ext4/XFS/NFSv4 NAME_MAX
	DefaultMaxTotalBytes     = 4096 // Linux PATH_MAX
)

// SanitizeOptions controls SanitizePath's behaviour.
type SanitizeOptions struct {
	// ReplaceIllegal is true to map each illegal character to a safe
	// substitute, false to delete it outright.
	ReplaceIllegal bool

	// IsPath is true when '/' is a path separator: each '/'-delimited
	// segment is sanitised independently and the separators are kept.
	// False treats '/' as itself illegal, like any other character in
	// illegalReplacements.
	IsPath bool

	// MaxComponentBytes caps each path segment (or the whole string, when
	// !IsPath) in bytes. Zero means DefaultMaxComponentBytes.
	MaxComponentBytes int

	// MaxTotalBytes caps the fully joined result in bytes. Zero means
	// DefaultMaxTotalBytes.
	MaxTotalBytes int
}

// DefaultSanitizeOptions returns the recommended defaults: replace illegal
// characters, treat the input as a path, and cap at the ext4/NFSv4
// component limit and the Linux PATH_MAX total limit.
func DefaultSanitizeOptions() SanitizeOptions {
	return SanitizeOptions{ReplaceIllegal: true, IsPath: true, MaxComponentBytes: DefaultMaxComponentBytes, MaxTotalBytes: DefaultMaxTotalBytes}
}

// illegalReplacements is this package's own substitution table for
// characters that are illegal or discouraged in file and folder names on
// Windows/exFAT-influenced media-server conventions (: \ / > < ? * | "),
// chosen to stay readable rather than lossy. The character set itself is
// the *arr convention (note section A2, "Illegal characters and CleanTitle");
// the specific substitute for each one is Clustarr's own choice, since the
// note verifies the illegal set but not what each one becomes.
var illegalReplacements = map[rune]string{
	':':  "-",
	'\\': "-",
	'/':  "-",
	'>':  "",
	'<':  "",
	'"':  "'",
	'|':  "-",
	'?':  "",
	'*':  "",
}

// SanitizePath removes or replaces filesystem-illegal characters from s,
// trims trailing dots and spaces from each component (Windows shells
// silently drop them, so keeping them invites confusing mismatches), and
// caps component and total byte lengths. Non-ASCII text (accents, CJK, ...)
// passes through untouched -- only the fixed illegal-character set is
// touched.
func SanitizePath(s string, opts SanitizeOptions) string {
	if opts.MaxComponentBytes <= 0 {
		opts.MaxComponentBytes = DefaultMaxComponentBytes
	}
	if opts.MaxTotalBytes <= 0 {
		opts.MaxTotalBytes = DefaultMaxTotalBytes
	}
	segments := []string{s}
	if opts.IsPath {
		segments = strings.Split(s, "/")
	}
	for i, seg := range segments {
		segments[i] = sanitizeComponent(seg, opts)
	}
	sep := ""
	if opts.IsPath {
		sep = "/"
	}
	out := strings.Join(segments, sep)
	return capTotal(out, opts.MaxTotalBytes, opts.IsPath)
}

func sanitizeComponent(s string, opts SanitizeOptions) string {
	var b strings.Builder
	for _, r := range s {
		repl, illegal := illegalReplacements[r]
		switch {
		case !illegal:
			b.WriteRune(r)
		case opts.ReplaceIllegal:
			b.WriteString(repl)
		}
	}
	out := strings.TrimRight(b.String(), ". ")
	return truncateBytes(out, opts.MaxComponentBytes)
}

// truncateBytes trims s to at most max bytes without splitting a
// multi-byte rune.
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return string([]rune(s)[:runesForBytes(s, max)])
}

// runesForBytes returns the number of leading runes of s whose combined
// UTF-8 encoding fits within max bytes.
func runesForBytes(s string, max int) int {
	n, used := 0, 0
	for _, r := range s {
		rl := utf8.RuneLen(r)
		if used+rl > max {
			break
		}
		used += rl
		n++
	}
	return n
}

// capTotal caps the fully joined path (or plain string, when !isPath) at
// max bytes. For a path, only the final '/'-segment is shrunk, so a long
// leaf name is truncated but the directory structure above it survives
// intact; for a plain string the whole thing is truncated.
func capTotal(s string, max int, isPath bool) string {
	if len(s) <= max || !isPath {
		return truncateBytes(s, max)
	}
	i := strings.LastIndexByte(s, '/')
	if i < 0 {
		return truncateBytes(s, max)
	}
	head, tail := s[:i+1], s[i+1:]
	room := max - len(head)
	if room <= 0 {
		return truncateBytes(s, max) // pathological: head alone already over budget
	}
	return head + truncateBytes(tail, room)
}
