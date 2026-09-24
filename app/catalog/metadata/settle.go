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

package metadata

import (
	"errors"
	"reflect"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// defaultRetryAfter is used when a provider's rate-limit error carries no
// Retry-After hint (ErrRateLimited bare, not wrapped in RateLimitedError).
const defaultRetryAfter = 30 * time.Second

// settlement maps a pkg/metadata provider error onto the events.Handler
// vocabulary §8.8 defines: Retry for a transient, provider-told-us-when
// condition; Discard for one no amount of retrying fixes; anything else
// passes through so the subscription's own backoff schedule
// (catalogarr-metadata: 30s,2m,10m,1h,6h) applies.
//
// Registry.Lookup joins every provider's error when none succeeds, and
// errors.Is answers true if any one of them matches -- so asking
// errors.Is(err, ErrNotFound) of the joined error discarded a task whenever
// one provider had no record, even while another had merely failed for
// the moment and would answer on a retry. The verdict is therefore taken
// per provider: a task is discarded only when every provider's answer is
// final (no record, credentials rejected, or an id it cannot use), and a
// single transient failure among them keeps it retryable.
func settlement(err error) error {
	if err == nil {
		return nil
	}
	var rl *pkgmetadata.RateLimitedError
	if errors.As(err, &rl) {
		after := rl.RetryAfter
		if after <= 0 {
			after = defaultRetryAfter
		}
		return events.Retry(after, err)
	}
	if errors.Is(err, pkgmetadata.ErrRateLimited) {
		return events.Retry(defaultRetryAfter, err)
	}

	var notFound, auth, transient bool
	for _, e := range perProvider(err) {
		switch {
		case errors.Is(e, pkgmetadata.ErrNotFound):
			notFound = true
		case errors.Is(e, pkgmetadata.ErrAuth):
			auth = true
		case errors.Is(e, pkgmetadata.ErrUnsupported):
			// This provider cannot serve these ids at all; its answer
			// neither ends the task nor argues for retrying it.
		default:
			transient = true
		}
	}
	switch {
	case transient:
		return err
	case notFound:
		return events.Discard("metadata: no such record at the provider", err)
	case auth:
		return events.Discard("metadata: provider rejected credentials", err)
	default:
		return err
	}
}

// perProvider splits the error Registry.Lookup returns into one error per
// provider it asked. Lookup joins them with errors.Join, so the first
// errors.Join found down the single-wrap chain is split and nothing below
// it: a provider's own error may itself wrap several sentinels (fmt.Errorf
// with two %w verbs) and must stay whole, or an id it refused as both
// invalid and unsupported would read as two answers. An error with no
// errors.Join in it is one provider's.
func perProvider(err error) []error {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if j, ok := e.(interface{ Unwrap() []error }); ok && isJoin(e) {
			return j.Unwrap()
		}
	}
	return []error{err}
}

// isJoin reports whether err was built by errors.Join rather than by
// fmt.Errorf with several %w verbs; both unwrap to a slice, but only the
// first is a list of separate failures.
func isJoin(err error) bool {
	return reflect.TypeOf(err) == joinErrorType
}

var joinErrorType = reflect.TypeOf(errors.Join(errors.New("")))
