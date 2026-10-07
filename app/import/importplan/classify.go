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

package importplan

import (
	"fmt"
	"strings"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// RetryLadder is how long a transient import waits before each re-inspect,
// by attempt (the old import consumer's backoff): after the last, it is
// held. A release fault is inspected once more, after the first step.
var RetryLadder = []time.Duration{1 * time.Minute, 10 * time.Minute, 45 * time.Minute}

// Rejection is why the planner, or the inspect before it, refused one file,
// with the class that decides, together with every other file's, what
// becomes of an import that imports nothing (Classify). An incidental
// rejection -- a promo clip beside the release, a second file for an item
// this download already filled, a duplicate of a planned file -- has no
// class: it explains a file and decides nothing.
type Rejection struct {
	Class commonv1.ImportRejectionClass
	Text  string
	// Transcoded marks a refusal over a transcoded file
	// (MessageExistingFileFinal).
	Transcoded bool
	// Sample marks a suspected sample's, incidental once real media is
	// beside it.
	Sample bool
	// Blocked marks a refusal the whole import shares: the item or its root
	// folder cannot take the files.
	Blocked bool
}

func (r Rejection) none() bool { return r.Text == "" }

func rejection(class commonv1.ImportRejectionClass, format string, args ...any) Rejection {
	return Rejection{Class: class, Text: fmt.Sprintf(format, args...)}
}

func itemStateRejection(format string, args ...any) Rejection {
	return rejection(commonv1.ImportClassItemState, format, args...)
}

func needsPersonRejection(format string, args ...any) Rejection {
	return rejection(commonv1.ImportClassNeedsPerson, format, args...)
}

func releaseFaultRejection(format string, args ...any) Rejection {
	return rejection(commonv1.ImportClassReleaseFault, format, args...)
}

func incidentalRejection(format string, args ...any) Rejection {
	return Rejection{Text: fmt.Sprintf(format, args...)}
}

func blockedRejection(format string, args ...any) Rejection {
	r := needsPersonRejection(format, args...)
	r.Blocked = true
	return r
}

// FromInspect is the planner's reading of an inspect's rejection.
func FromInspect(r schema.InspectRejection) Rejection {
	out := Rejection{Class: commonv1.ImportRejectionClass(r.Class), Text: r.Text}
	switch r.Kind {
	case schema.InspectKindBlocked:
		out.Blocked = true
		if out.Class == "" {
			out.Class = commonv1.ImportClassNeedsPerson
		}
	case schema.InspectKindSample:
		out.Sample = true
	case schema.InspectKindIncidental:
		out.Class = ""
	}
	return out
}

// classPrecedence orders the classes for Classify: any doubt holds a
// download, and only a release whose every decisive file is at fault is
// condemned.
var classPrecedence = []commonv1.ImportRejectionClass{
	commonv1.ImportClassTransient,
	commonv1.ImportClassNeedsPerson,
	commonv1.ImportClassItemState,
	commonv1.ImportClassReleaseFault,
}

// Classify is the class of an import that imported nothing, from every
// rejection it made: the first of classPrecedence any file was refused for.
// An import that offered no decisive file at all -- nothing in it, or only
// incidental files such as a promo clip -- held no media: the release is at
// fault.
func Classify(rs []Rejection) commonv1.ImportRejectionClass {
	seen := map[commonv1.ImportRejectionClass]bool{}
	for _, r := range rs {
		seen[r.Class] = true
	}
	for _, c := range classPrecedence {
		if seen[c] {
			return c
		}
	}
	return commonv1.ImportClassReleaseFault
}

// blocked is the first rejection the whole import shares, if any.
func blocked(rs []Rejection) (Rejection, bool) {
	for _, r := range rs {
		if r.Blocked {
			return r, true
		}
	}
	return Rejection{}, false
}

// texts renders the decisive rejections first, then the incidental ones,
// for the summary's message.
func texts(rs []Rejection) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		if r.Class != "" {
			out = append(out, r.Text)
		}
	}
	for _, r := range rs {
		if r.Class == "" {
			out = append(out, r.Text)
		}
	}
	return out
}

// detail joins head with up to three rejection texts.
func detail(head string, rs []Rejection) string {
	t := texts(rs)
	if len(t) == 0 {
		return head
	}
	more := ""
	if len(t) > 3 {
		more = fmt.Sprintf(" (and %d more)", len(t)-3)
		t = t[:3]
	}
	return head + ": " + strings.Join(t, "; ") + more
}
