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
	"errors"
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

// EmbeddedSchema returns a copy of the v11 schema this package validates
// against -- for a tool that must know whether an upstream corpus was
// written against the same schema (hack/sync-cardigann).
func EmbeddedSchema() []byte { return bytes.Clone(schemaJSON) }

// SchemaURL is the identifier the embedded schema is registered under. It is
// a name, not a location: nothing ever fetches it (jsonschema/v6's default
// loader only reads file URLs, and the resource is added before Compile asks
// for it). It must be absolute. A bare name such as "schema-v11.json" is
// resolved against the process working directory, and every validation
// error then renders as file:///<cwd>/schema-v11.json#... -- the binary's
// working directory, in `kubectl describe`, for a file that is not on disk.
const SchemaURL = "https://clustarr.io/schemas/cardigann/v11.json"

// MaxSchemaErrorBytes bounds SchemaError.Error()'s detail. jsonschema/v6
// renders every failed branch of every oneOf/anyOf, so its message grows
// faster than the definition: a 73 KB definition produced a 105,599-byte
// error, over three times the 32,768-byte maxLength on a condition message,
// and an apply over that limit is rejected rather than truncated. 4 KiB
// keeps the first, most specific failures and leaves a caller room for its
// own prefix.
const MaxSchemaErrorBytes = 4096

// ErrInvalidDefinition is what every definition rejected by Validate or Load
// matches with errors.Is: a schema violation, or a value the schema admits
// but this engine cannot honour.
var ErrInvalidDefinition = errors.New("cardigann: invalid definition")

// SchemaError is a schema violation with a bounded message. It unwraps to
// both ErrInvalidDefinition and the *jsonschema.ValidationError itself, so
// errors.As still reaches the full, unbounded detail for a caller that
// wants it and is prepared to bound it.
type SchemaError struct {
	// Message is the rendered validation failure, at most
	// MaxSchemaErrorBytes bytes and always valid UTF-8.
	Message string
	cause   error
}

func (e *SchemaError) Error() string { return "cardigann: schema validation: " + e.Message }

// Unwrap exposes ErrInvalidDefinition and the underlying validation error.
func (e *SchemaError) Unwrap() []error { return []error{ErrInvalidDefinition, e.cause} }

// newSchemaError bounds cause's rendering to MaxSchemaErrorBytes, marking the
// cut so a reader knows the list goes on.
func newSchemaError(cause error) *SchemaError {
	const marker = " ... (truncated)"
	msg := cause.Error()
	if len(msg) > MaxSchemaErrorBytes {
		msg = truncateRunes(msg, MaxSchemaErrorBytes-len(marker)) + marker
	}
	return &SchemaError{Message: msg, cause: cause}
}

var compiledSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, fmt.Errorf("cardigann: parse embedded schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(SchemaURL, doc); err != nil {
		return nil, fmt.Errorf("cardigann: load embedded schema: %w", err)
	}
	return c.Compile(SchemaURL)
})

// Validate checks data (a raw Cardigann YAML document, not yet decoded)
// against the embedded schema-v11.json. A schema violation is a *SchemaError
// (errors.Is ErrInvalidDefinition, errors.As *jsonschema.ValidationError)
// whose Error() is bounded by MaxSchemaErrorBytes; nil on success. Load calls
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
	data = trimBOM(data)
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
		return newSchemaError(err)
	}
	return nil
}

// utf8BOM is the byte-order mark some editors write at the start of a UTF-8
// file. YAML allows one at the start of a stream, and one of the v11
// corpus's definitions (torrent-pirat.yml) starts with it, but
// goccy/go-yaml reads it as the first character of the first key.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// trimBOM drops a leading UTF-8 byte-order mark.
func trimBOM(data []byte) []byte { return bytes.TrimPrefix(data, utf8BOM) }
