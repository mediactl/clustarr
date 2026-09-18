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
	"errors"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

func TestDefaultTopologyIsValid(t *testing.T) {
	if err := events.Default().Validate(); err != nil {
		t.Fatalf("Default().Validate(): %v", err)
	}
	if err := events.Default().ForSingleNode().Validate(); err != nil {
		t.Fatalf("ForSingleNode().Validate(): %v", err)
	}
}

// TestEveryConsumerHasDeliveryHeadroom pins the invariant the spec calls out:
// a consumer whose MaxDeliver equals the length of its backoff schedule burns
// its last attempt on a delay it never uses.
func TestEveryConsumerHasDeliveryHeadroom(t *testing.T) {
	for _, c := range events.Default().Consumers {
		if c.MaxDeliver <= len(c.BackOff) {
			t.Errorf("consumer %s: MaxDeliver = %d, len(BackOff) = %d",
				c.Name, c.MaxDeliver, len(c.BackOff))
		}
	}
}

func TestWorkStreamsAllowSchedules(t *testing.T) {
	for _, s := range events.Default().Streams {
		if s.Retention != events.RetentionWorkQueue {
			continue
		}
		if !s.AllowMsgSchedules {
			t.Errorf("work stream %s does not allow message schedules", s.Name)
		}
		if s.Discard == events.DiscardNew {
			t.Errorf("work stream %s pairs schedules with DiscardNew, which "+
				"nats-server refuses", s.Name)
		}
	}
}

func TestTopologyValidateCatchesBadConsumer(t *testing.T) {
	top := events.Default()
	top.Consumers = append(top.Consumers, events.ConsumerSpec{
		Name:       "broken",
		Stream:     events.StreamWorkCatalogarr,
		Filters:    []string{events.FilterCatalogGrab},
		MaxDeliver: 2,
		BackOff:    []time.Duration{time.Second, time.Second},
	})
	if err := top.Validate(); err == nil {
		t.Fatal("Validate accepted MaxDeliver == len(BackOff)")
	}

	top = events.Default()
	top.Consumers = append(top.Consumers, events.ConsumerSpec{
		Name:       "stray",
		Stream:     events.StreamWorkIndexarr,
		Filters:    []string{events.FilterCatalogGrab},
		MaxDeliver: 3,
	})
	if err := top.Validate(); err == nil {
		t.Fatal("Validate accepted a filter outside its stream")
	}

	top = events.Default()
	top.Streams = nil
	if err := top.Validate(); err == nil {
		t.Fatal("Validate accepted a topology without the dead-letter stream")
	}
}

func TestStreamForSubject(t *testing.T) {
	top := events.Default()
	cases := map[string]string{
		events.CatalogItemSubject("movie", events.ActionAdded, "u"): events.StreamEvents,
		events.ReleaseSubject("torrent", "nzb-su", 2000):            events.StreamReleases,
		events.WorkSearchSubject(events.PriorityHigh, "m1"):         events.StreamWorkCatalogarr,
		events.WorkRSSSubject("idx"):                                events.StreamWorkIndexarr,
		events.WorkFetchSubject(events.PriorityLow, "r", "en"):      events.StreamWorkCaptionarr,
		events.DLQSubject("catalogarr", "import", "1"):              events.StreamDLQ,
	}
	for subject, want := range cases {
		got, ok := top.StreamForSubject(subject)
		if !ok {
			t.Errorf("no stream for %q", subject)
			continue
		}
		if got.Name != want {
			t.Errorf("%q resolved to %s, want %s", subject, got.Name, want)
		}
	}
	if _, ok := top.StreamForSubject("clustarr.progress.download.u1"); ok {
		t.Error("progress subjects must not resolve to a stream; they are core NATS")
	}
}

func TestSubjectMatches(t *testing.T) {
	cases := []struct {
		filter, subject string
		want            bool
	}{
		{"clustarr.evt.>", "clustarr.evt.catalog.movie.added.u1", true},
		{"clustarr.evt.>", "clustarr.rel.torrent.x.2000", false},
		{"clustarr.work.catalogarr.search.high.>", "clustarr.work.catalogarr.search.high.m1", true},
		{"clustarr.work.catalogarr.search.high.>", "clustarr.work.catalogarr.search.low.m1", false},
		{"clustarr.work.catalogarr.*.normal.>", "clustarr.work.catalogarr.grab.normal.m1", true},
		{"a.b", "a.b", true},
		{"a.b", "a.b.c", false},
		{"a.>", "a", false},
	}
	for _, c := range cases {
		if got := events.SubjectMatches(c.filter, c.subject); got != c.want {
			t.Errorf("SubjectMatches(%q, %q) = %v, want %v",
				c.filter, c.subject, got, c.want)
		}
	}
}

// TestScheduleSubjectAvoidsConsumerFilters is the property the whole delayed
// grab mechanism rests on: the holding subject must sit inside the work
// stream yet match no consumer, or a scheduled task would be handled twice.
func TestScheduleSubjectAvoidsConsumerFilters(t *testing.T) {
	target := events.WorkGrabSubject("movie-1")
	hold, err := events.ScheduleSubject(target)
	if err != nil {
		t.Fatalf("ScheduleSubject: %v", err)
	}
	if hold == target {
		t.Fatal("the holding subject equals the target subject")
	}
	top := events.Default()
	stream, ok := top.StreamForSubject(hold)
	if !ok || stream.Name != events.StreamWorkCatalogarr {
		t.Fatalf("holding subject %q resolved to %v/%v", hold, stream.Name, ok)
	}
	for _, c := range top.Consumers {
		for _, f := range c.Filters {
			if events.SubjectMatches(f, hold) {
				t.Errorf("consumer %s filter %q matches the holding subject %q",
					c.Name, f, hold)
			}
		}
	}
	if _, err := events.ScheduleSubject(
		events.CatalogItemSubject("movie", events.ActionAdded, "u")); err == nil {
		t.Error("ScheduleSubject accepted a non-work subject")
	}
}

func TestEnvelopeHeaderRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	in := &events.Envelope{
		ID:      "uid:3:search",
		Type:    "catalog.search",
		Schema:  "catalog.SearchTask.v1",
		Source:  "catalogarr@1.2.3+abc",
		Key:     "media/the-thing",
		Time:    now,
		Trace:   "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		Headers: map[string]string{"X-Custom": "1"},
		Data:    []byte(`{"a":1}`),
	}
	h := in.ToHeaders()
	if h[events.HeaderContentType] != events.ContentTypeJSON {
		t.Errorf("Content-Type = %q", h[events.HeaderContentType])
	}
	if h[events.HeaderMsgID] != in.ID {
		t.Errorf("%s = %q", events.HeaderMsgID, h[events.HeaderMsgID])
	}
	out := events.EnvelopeFromHeaders(h, in.Data)
	if out.ID != in.ID || out.Type != in.Type || out.Schema != in.Schema ||
		out.Source != in.Source || out.Key != in.Key || out.Trace != in.Trace {
		t.Errorf("round trip lost a field: %+v", out)
	}
	if !out.Time.Equal(in.Time) {
		t.Errorf("Time = %s, want %s", out.Time, in.Time)
	}
	if out.Headers["X-Custom"] != "1" {
		t.Errorf("custom headers = %v", out.Headers)
	}
	if _, ok := out.Headers[events.HeaderContentType]; ok {
		t.Error("Content-Type leaked into the extra headers")
	}
}

func TestEnvelopeCloneIsDeep(t *testing.T) {
	in := &events.Envelope{
		ID:      "a",
		Headers: map[string]string{"k": "v"},
		Data:    []byte("payload"),
	}
	out := in.Clone()
	out.Headers["k"] = "changed"
	out.Data[0] = 'X'
	if in.Headers["k"] != "v" {
		t.Error("Clone shared the headers map")
	}
	if string(in.Data) != "payload" {
		t.Error("Clone shared the payload slice")
	}
}

func TestSettle(t *testing.T) {
	sub := events.Subscription{
		MaxDeliver: 3,
		Backoff:    []time.Duration{time.Second, 5 * time.Second},
	}
	if s := events.Settle(nil, 1, sub); s.Action != events.SettleAck {
		t.Errorf("nil error settled as %q", s.Action)
	}
	if s := events.Settle(errors.New("boom"), 1, sub); s.Action != events.SettleNak ||
		s.Delay != time.Second {
		t.Errorf("first failure settled as %+v, want a 1s nak", s)
	}
	if s := events.Settle(errors.New("boom"), 2, sub); s.Delay != 5*time.Second {
		t.Errorf("second failure delay = %s, want 5s", s.Delay)
	}
	if s := events.Settle(errors.New("boom"), 3, sub); s.Action != events.SettleTerm {
		t.Errorf("last attempt settled as %q, want a term", s.Action)
	}
	if s := events.Settle(events.Discard("bad", nil), 1, sub); s.Action != events.SettleTerm ||
		s.Reason != "bad" {
		t.Errorf("discard settled as %+v", s)
	}
	if s := events.Settle(events.Retry(90*time.Second, nil), 1, sub); s.Delay != 90*time.Second {
		t.Errorf("explicit retry delay = %s, want 90s", s.Delay)
	}
	// An unlimited consumer never dead-letters on attempt count alone.
	if s := events.Settle(errors.New("boom"), 99, events.Subscription{}); s.Action != events.SettleNak {
		t.Errorf("unlimited consumer settled as %q", s.Action)
	}
}

func TestBackoffReusesLastEntry(t *testing.T) {
	sched := []time.Duration{time.Second, time.Minute}
	if got := events.Backoff(sched, 1); got != time.Second {
		t.Errorf("attempt 1 = %s", got)
	}
	if got := events.Backoff(sched, 5); got != time.Minute {
		t.Errorf("attempt 5 = %s, want the last entry", got)
	}
	if got := events.Backoff(nil, 1); got != events.DefaultBackoff {
		t.Errorf("empty schedule = %s, want DefaultBackoff", got)
	}
}

func TestMsgIDsAreDeterministic(t *testing.T) {
	if got := events.MsgIDForObject("uid-1", 4, "search"); got != "uid-1:4:search" {
		t.Errorf("MsgIDForObject = %q", got)
	}
	a := events.MsgIDForRelease("nzb.su", "guid-1")
	if a != events.MsgIDForRelease("nzb.su", "guid-1") {
		t.Error("MsgIDForRelease is not deterministic")
	}
	if a == events.MsgIDForRelease("nzb.su", "guid-2") {
		t.Error("MsgIDForRelease collided on different GUIDs")
	}
	if got := events.MsgIDForSubtitle("r1", "en:hi", "h9"); got != "r1/en:hi/h9" {
		t.Errorf("MsgIDForSubtitle = %q", got)
	}
}

// TestSubjectTokensAreSanitised guards against a media key with a dot in it
// silently widening a subject into extra tokens.
func TestSubjectTokensAreSanitised(t *testing.T) {
	got := events.WorkSearchSubject(events.PriorityHigh, "movie/the.thing 1982")
	want := "clustarr.work.catalogarr.search.high.movie-the-thing-1982"
	if got != want {
		t.Errorf("WorkSearchSubject = %q, want %q", got, want)
	}
	// Key/value keys keep dots, which NATS allows, but lose the characters
	// it does not.
	if got := events.LeaseKey("movie.the thing"); got != "grab.movie.the-thing" {
		t.Errorf("LeaseKey = %q, want %q", got, "grab.movie.the-thing")
	}
}

func TestSubscriptionValidate(t *testing.T) {
	ok := events.Subscription{
		Stream: events.StreamEvents, Durable: "d",
		Filters:    []string{events.FilterAllEvents},
		MaxDeliver: 3, Backoff: []time.Duration{time.Second},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid subscription rejected: %v", err)
	}
	bad := ok
	bad.MaxDeliver = 1
	if err := bad.Validate(); err == nil {
		t.Error("Validate accepted MaxDeliver == len(Backoff)")
	}
	bad = ok
	bad.Filters = nil
	if err := bad.Validate(); err == nil {
		t.Error("Validate accepted a subscription with no filters")
	}
}

func TestConsumerSpecSubscription(t *testing.T) {
	spec, ok := events.Default().Consumer(events.ConsumerCatalogImport)
	if !ok {
		t.Fatalf("consumer %s missing from the default topology",
			events.ConsumerCatalogImport)
	}
	sub := spec.Subscription()
	if sub.Durable != spec.Name || sub.Stream != spec.Stream {
		t.Errorf("Subscription = %+v", sub)
	}
	if sub.MaxInFlight != spec.MaxAckPending {
		t.Errorf("MaxInFlight = %d, want %d", sub.MaxInFlight, spec.MaxAckPending)
	}
	if err := sub.Validate(); err != nil {
		t.Errorf("derived subscription is invalid: %v", err)
	}
}

// TestKVBucketNamesHaveNoDots pins the NATS naming rule for buckets.
func TestKVBucketNamesHaveNoDots(t *testing.T) {
	for _, b := range events.Default().Buckets {
		for _, r := range b.Name {
			if r == '.' {
				t.Errorf("bucket %q contains a dot", b.Name)
			}
		}
		if b.LimitMarkerTTL <= 0 {
			t.Errorf("bucket %q has no LimitMarkerTTL, so per-key TTL cannot work",
				b.Name)
		}
	}
}
