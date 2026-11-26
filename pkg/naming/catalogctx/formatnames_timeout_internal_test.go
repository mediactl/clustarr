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

package catalogctx

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// The two names One Piece S18E28 was renamed between on talos, and the
// formats frozen on its MediaFile, which records no release title.
const (
	loopShort = "One Piece (1999) - S18E28 - 774 - A Battle to Defend Zou! Luffy and Zunisha! [v2][WEBDL-1080p v3][AAC 2.0][JA+EN][h265 8bit]-Kitsune"
	loopLong  = "One Piece (1999) - S18E28 - 774 - A Battle to Defend Zou! Luffy and Zunisha! [v2 v3][WEBDL-1080p v3][AAC 2.0][JA+EN][h265 8bit]-Kitsune"
)

var loopFrozen = []string{"anime-web-tier-03", "repack-proper", "v2", "web-tier-01"}

func emptyFormatNamesMemo() {
	formatNamesMemo.Lock()
	formatNamesMemo.m = map[formatNamesKey][]string{}
	formatNamesMemo.Unlock()
}

// regexTimeout is the error a regex of pkg/release gives when it runs out
// of time, made by one that does.
func regexTimeout(t *testing.T) error {
	t.Helper()
	re, err := regexp2.Compile(`^(a+)+$`, regexp2.None)
	require.NoError(t, err)
	re.MatchTimeout = 1 * time.Nanosecond
	_, err = re.MatchString(strings.Repeat("a", 40) + "!")
	require.Error(t, err)
	err = fmt.Errorf("release: %w", err)
	require.True(t, release.IsTimeout(err))
	return err
}

// Both of the file's names name the same formats, so neither proposes the
// other: the rename loop was not in what the names say.
func TestBothNamesOfTheRenamedFileNameTheSameFormats(t *testing.T) {
	emptyFormatNamesMemo()
	for _, title := range []string{loopShort, loopLong} {
		c := naming.Context{Kind: commonv1.MediaKindEpisode, Anime: true, ReleaseTitle: title}
		assert.Equal(t, []string{"v2", "v3"}, formatNames(context.Background(), c, loopFrozen), title)
	}
}

// A parse that runs out of time is tried again at once, and an answer that
// is still short after renameSlugsTries is given but never remembered. It
// was remembered for the life of the process: "[v2 v3]" read "[v2]" from
// then on while "[v2]" read "[v2 v3]", and the file was renamed between
// them eight times a second for eight days.
func TestAnAnswerARegexRanOutOfTimeOnIsNotRemembered(t *testing.T) {
	timeout := regexTimeout(t)
	c := naming.Context{Kind: commonv1.MediaKindEpisode, Anime: true, ReleaseTitle: loopLong}
	ctx := context.Background()
	failing := func(n int) (calls *int) {
		calls = new(int)
		parseRelease = func(title string, o release.Options) (*release.ParsedRelease, error) {
			*calls++
			if *calls <= n {
				return nil, timeout
			}
			return release.Parse(title, o)
		}
		return calls
	}
	t.Cleanup(func() { parseRelease = release.Parse })

	// One parse out of time: the next try, in the same call, answers.
	emptyFormatNamesMemo()
	calls := failing(1)
	assert.Equal(t, []string{"v2", "v3"}, formatNames(ctx, c, loopFrozen), "a parse that ran out of time once is tried again")
	assert.Equal(t, 2, *calls)

	// Every try out of time: the frozen formats alone, and not remembered.
	emptyFormatNamesMemo()
	calls = failing(renameSlugsTries)
	assert.Equal(t, []string{"v2"}, formatNames(ctx, c, loopFrozen), "the answer it knows is short")
	assert.Equal(t, renameSlugsTries, *calls)
	assert.Equal(t, []string{"v2", "v3"}, formatNames(ctx, c, loopFrozen), "a short answer was not remembered")
	before := *calls
	assert.Equal(t, []string{"v2", "v3"}, formatNames(ctx, c, loopFrozen))
	assert.Equal(t, before, *calls, "the whole answer is")
}

// The same for a format's condition that runs out of time: the match reads
// "no match", the answer is short a format, and it is not remembered.
func TestAnAnswerAConditionRanOutOfTimeOnIsNotRemembered(t *testing.T) {
	re, err := regexp2.Compile(`^(a+)+$`, regexp2.None)
	require.NoError(t, err)
	re.MatchTimeout = 1 * time.Nanosecond
	cat := catalogue.LoadedCatalogue()
	const slug = "zz-test-out-of-time"
	cat.Formats[slug] = &catalogue.Format{
		Slug: slug, Name: "OutOfTime", IncludeInRename: true,
		Conditions: []catalogue.Condition{{Kind: catalogue.CondReleaseTitle, Name: "evil", Required: true, Pattern: re}},
	}
	t.Cleanup(func() { delete(cat.Formats, slug) })
	emptyFormatNamesMemo()
	t.Cleanup(emptyFormatNamesMemo)

	c := naming.Context{Kind: commonv1.MediaKindMovie, ReleaseTitle: strings.Repeat("a", 40) + "!.1995.REPACK2.1080p.AMZN.WEB-DL.DDP5.1.H.264-GROUP"}
	slugs, whole := renameSlugs(context.Background(), cat, c, nil)
	assert.False(t, whole, "a condition ran out of time on every try")
	assert.NotContains(t, slugs, slug)
	formatNames(context.Background(), c, nil)
	formatNamesMemo.Lock()
	n := len(formatNamesMemo.m)
	formatNamesMemo.Unlock()
	assert.Zero(t, n, "the short answer is not in the memo")

	delete(cat.Formats, slug)
	_, whole = renameSlugs(context.Background(), cat, c, nil)
	assert.True(t, whole)
	formatNames(context.Background(), c, nil)
	formatNamesMemo.Lock()
	n = len(formatNamesMemo.m)
	formatNamesMemo.Unlock()
	assert.Equal(t, 1, n, "a whole answer is")
}
