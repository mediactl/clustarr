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

// Package schema holds the JSON payload structs carried on the Clustarr bus.
//
// Every payload is versioned by the Clustarr-Schema header, never by the
// subject, so a subject can carry a v1 and a v2 payload during a rollout. A
// new version is a new struct with a new Schema() value; existing structs are
// never changed incompatibly.
//
// Payloads carry plain data and object references only. They never embed
// custom resources, so a consumer can decode a message without the producing
// service's API group.
package schema

import (
	"encoding/json"
	"fmt"
	"time"
)

// Payload is any versioned bus payload.
type Payload interface {
	// Schema returns the value of the Clustarr-Schema header, e.g.
	// "catalog.SearchTask.v1".
	Schema() string
}

// Ref points at a Kubernetes object without embedding it.
type Ref struct {
	// Namespace is the object namespace.
	Namespace string `json:"namespace,omitempty"`

	// Name is the object name.
	Name string `json:"name"`

	// UID is the object UID, when the producer knew it.
	UID string `json:"uid,omitempty"`
}

// String renders the reference as "<namespace>/<name>".
func (r Ref) String() string {
	if r.Namespace == "" {
		return r.Name
	}
	return r.Namespace + "/" + r.Name
}

// Key is the Clustarr-Key header value for this reference.
func (r Ref) Key() string { return r.String() }

// Encode marshals p to JSON and returns it with its schema string, ready for
// an Envelope.
func Encode(p Payload) (schema string, data []byte, err error) {
	data, err = json.Marshal(p)
	if err != nil {
		return "", nil, fmt.Errorf("schema: encode %s: %w", p.Schema(), err)
	}
	return p.Schema(), data, nil
}

// Decode unmarshals data into out, checking that schema names the payload out
// expects. An empty schema skips the check, for messages published before the
// header was mandatory.
func Decode(schema string, data []byte, out Payload) error {
	if schema != "" && schema != out.Schema() {
		return fmt.Errorf("schema: message is %q, want %q", schema, out.Schema())
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("schema: decode %s: %w", out.Schema(), err)
	}
	return nil
}

// Window is a closed time interval carried by progress and history payloads.
type Window struct {
	// Start is when the interval began.
	Start time.Time `json:"start"`

	// End is when the interval ended.
	End time.Time `json:"end,omitempty"`
}
