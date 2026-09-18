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

package cardigann

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	yaml "github.com/goccy/go-yaml"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema.json
var schemaJSON []byte

// EmbeddedSchemaForTest exposes the embedded schema bytes for a test-only
// byte-identity check against testdata/cardigann/schema-v11.json — the two
// must never drift, since //go:embed cannot reach outside this package
// directory and therefore needs its own copy of the same file.
func EmbeddedSchemaForTest() []byte { return schemaJSON }

var compiledSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, fmt.Errorf("cardigann: parse embedded schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema-v11.json", doc); err != nil {
		return nil, fmt.Errorf("cardigann: load embedded schema: %w", err)
	}
	return c.Compile("schema-v11.json")
})

// Validate checks data (a raw Cardigann YAML document, not yet decoded)
// against the embedded schema-v11.json. It returns a wrapped
// *jsonschema.ValidationError on failure and nil on success. Load calls
// this before decoding; call it directly to schema-check without paying
// for a full Definition (e.g. a fast-path admission check).
//
// The YAML->JSON conversion deliberately goes through yaml.Unmarshal into a
// generic any (which goccy/go-yaml decodes as map[string]any, safe for
// encoding/json to re-marshal) rather than yaml.YAMLToJSON: verified against
// goccy/go-yaml v1.19.2, YAMLToJSON mis-encodes the control character U+000F
// that appears in 1337x.yml's title filter chain as the invalid JSON escape
// `\x0f` (JSON has no \x escape, only \uXXXX), which then fails strict JSON
// decoding. Round-tripping through Go's native map/slice/scalar types and
// encoding/json avoids the bug entirely.
func Validate(data []byte) error {
	sch, err := compiledSchema()
	if err != nil {
		return err
	}
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("cardigann: yaml decode: %w", err)
	}
	jsonBytes, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("cardigann: yaml to json: %w", err)
	}
	var v any
	if err := json.Unmarshal(jsonBytes, &v); err != nil {
		return fmt.Errorf("cardigann: decode definition json: %w", err)
	}
	if err := sch.Validate(v); err != nil {
		return fmt.Errorf("cardigann: schema validation: %w", err)
	}
	return nil
}
