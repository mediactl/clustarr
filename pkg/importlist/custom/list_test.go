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

package custom_test

import (
	"errors"
	"testing"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/importlist/custom"
	"github.com/stretchr/testify/assert"
)

func TestFetchReturnsNotImplemented(t *testing.T) {
	l := custom.New("custom-feed", commonv1.MediaKindMovie, importlist.CustomConfig{})
	assert.Equal(t, "custom-feed", l.Name())
	assert.Equal(t, commonv1.MediaKindMovie, l.Kind())

	items, err := l.Fetch(t.Context())
	assert.Nil(t, items)
	var nie *importlist.NotImplementedError
	assert.True(t, errors.As(err, &nie))
	assert.Equal(t, "custom", nie.Provider)
	assert.ErrorIs(t, err, importlist.ErrNotImplemented)
}
