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

package textdet_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/segments/textdet"
)

// byTime is a fake detector over fake frames: a frame's one byte is its
// time, and it is dense from startS on.
type byTime struct {
	startS float64
	calls  int
}

func (b *byTime) Density(rgb []byte, _, _ int) (int32, error) {
	b.calls++
	if float64(rgb[0])*10 >= b.startS {
		return 100, nil
	}
	return 0, nil
}

func frameAt(_ context.Context, atS float64) ([]byte, int, int, error) {
	return []byte{byte(atS / 10)}, 1, 1, nil
}

// Credits starting at 1,230 s of a 450 s window are found to within the
// fake's 10 s grain, in at most 25 inferences.
func TestFindStartBinarySearch(t *testing.T) {
	d := &byTime{startS: 1230}
	start, ok, err := textdet.FindStart(context.Background(), d, frameAt, 1000, 1450, 50)
	require.NoError(t, err)
	require.True(t, ok)
	assert.InDelta(t, 1230, start, 10)
	assert.LessOrEqual(t, d.calls, 25)
}

func TestFindStartNeedsTextAtTheEnd(t *testing.T) {
	d := &byTime{startS: 9999}
	_, ok, err := textdet.FindStart(context.Background(), d, frameAt, 1000, 1450, 50)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestUnavailableWithoutTheLibrary(t *testing.T) {
	_, err := textdet.NewONNX(filepath.Join(t.TempDir(), "libonnxruntime.so"))
	assert.True(t, errors.Is(err, textdet.ErrUnavailable), "%v", err)
}

// The real model on two frames from Dexter S01E01: a name card over a
// textured picture, and a scene with a little text in it (a name tag).
func TestONNXSeesCreditsText(t *testing.T) {
	lib := os.Getenv("ORT_LIB_PATH")
	if lib == "" {
		t.Skip("ORT_LIB_PATH is not set")
	}
	d, err := textdet.NewONNX(lib)
	require.NoError(t, err)
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "test", "data", "segments", "textdet", name+".rgb"))
		require.NoError(t, err)
		return b
	}
	credits, err := d.Density(read("credits"), 320, 180)
	require.NoError(t, err)
	scene, err := d.Density(read("scene"), 320, 180)
	require.NoError(t, err)
	t.Logf("density: credits %d, scene %d permille", credits, scene)
	assert.GreaterOrEqual(t, credits, textdet.CreditsThreshold)
	assert.Less(t, scene, textdet.CreditsThreshold)
}
