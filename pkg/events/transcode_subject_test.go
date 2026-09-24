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

package events_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/events"
)

// TestWorkTranscodeTaskSubjectAnyPoolMatchesEveryTaskOfTheJob is what
// deletion's purge rests on (final-review M6): every task subject of the job,
// under any profile and any class, and none of another job's.
func TestWorkTranscodeTaskSubjectAnyPoolMatchesEveryTaskOfTheJob(t *testing.T) {
	filter := events.WorkTranscodeTaskSubjectAnyPool("job.1")
	for _, subject := range []string{
		events.WorkTranscodeTaskSubject("profA", "cpu", "job.1"),
		events.WorkTranscodeTaskSubject("profB", "nvidia", "job.1"),
		events.WorkTranscodeTaskSubject("profA", "", "job.1"),
	} {
		assert.True(t, events.SubjectMatches(filter, subject), "%s must match %s", filter, subject)
	}
	for _, subject := range []string{
		events.WorkTranscodeTaskSubject("profA", "cpu", "job.2"),
		events.WorkTranscodeResultSubject("job.1"),
	} {
		assert.False(t, events.SubjectMatches(filter, subject), "%s must not match %s", filter, subject)
	}
}
