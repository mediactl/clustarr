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

package importlist

import (
	"context"
	"strconv"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// ExternalIDs mirrors pkg/metadata.ExternalIDs field-for-field. This package
// defines its own copy rather than importing pkg/metadata (Task B9, built in
// parallel in Phase B's wave 1): the two packages must each build, vet and
// test green independently, so neither can depend on the other's unlanded
// output. A follow-up task (the Phase G ImportList controller) converts
// between the two, or type-aliases them if B9's shape matches exactly.
type ExternalIDs struct {
	IMDb        string
	TMDB        string
	TVDB        string
	MusicBrainz string
	ISBN        string
	ASIN        string
	ComicVine   string
	AniList     string
}

// Item is one entry a provider fetched, not yet resolved into a catalog
// kind.
type Item struct {
	Title       string
	Year        int32
	ExternalIDs ExternalIDs

	// Monitored and QualityProfileRef are per-item overrides a provider may
	// carry (e.g. an arr-to-arr sync mirrors the source instance's own
	// monitored flag and profile). Nil means: use the ImportList's
	// ListDefaults; the controller fills the gap.
	Monitored         *bool
	QualityProfileRef *string
}

// ImportList fetches the current contents of one remote list.
type ImportList interface {
	// Name identifies this instance, unique within a Registry.
	Name() string

	// Kind is the single catalog kind this instance fetches. A CRD instance
	// declaring multiple kinds in spec.kinds becomes one ImportList per
	// kind (see config.go's doc comment on Config).
	Kind() commonv1.MediaKind

	// Fetch retrieves the list's current contents from the remote source.
	Fetch(ctx context.Context) ([]Item, error)
}

// NonZeroString renders n as a decimal string, or the empty string when n
// is zero. Several providers (Trakt, MDBList) decode an external ID field
// they omit entirely -- rather than send as an explicit null -- to Go's
// int zero value; this turns that decoded zero back into the "absent"
// ExternalIDs value ("") instead of the misleading literal "0". It is also
// handy for any other optional integer a provider renders as a query
// parameter only when set.
func NonZeroString(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}
