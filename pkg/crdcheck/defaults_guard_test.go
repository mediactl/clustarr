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

package crdcheck

import (
	"go/ast"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// defaultClass is how a +kubebuilder:default fares against a typed Go
// client. The apiserver applies a default only to an ABSENT field (or a
// null one, which it prunes first), so what matters is what encoding/json
// sends for the field's Go zero value.
type defaultClass string

const (
	// The field is a pointer: nil is omitted (or sent null) and defaulted,
	// and any value, zero included, can be sent explicitly.
	okPointer defaultClass = "ok: pointer"
	// A nil list or map is omitted (or sent null) and defaulted.
	okListOrMap defaultClass = "ok: list or map"
	// metav1.Time's zero marshals as null, which is pruned and defaulted.
	okNullWhenZero defaultClass = "ok: zero marshals as null"
	// omitzero drops a zero struct, so the default applies.
	okOmitZero defaultClass = "ok: omitzero"
	// A struct defaulted to {} only materialises the object so its own
	// fields' defaults run. A Go client always sends the object, so those
	// run anyway; the default and the Go zero value agree.
	okMaterialiser defaultClass = "ok: {} materialises the object"
	// The default equals the Go zero value, so it does not matter whether
	// it is applied.
	okDefaultIsZero defaultClass = "ok: default is the Go zero value"
	// omitempty drops the zero and the apiserver defaults it, and the
	// schema rejects the zero anyway (Minimum, Enum, MinLength, Pattern),
	// so nothing a client could have meant is lost.
	okZeroRejected defaultClass = "ok: zero omitted and rejected by the schema anyway"
	// omitempty drops the zero and the apiserver defaults it (E-4's
	// correction: such a field DOES get its default from Go). The zero
	// itself cannot be sent from Go, which matters only if the zero means
	// something -- so this passes unless the field's own doc comment says
	// it does (see zeroMeaning).
	okZeroOmitted defaultClass = "ok: zero omitted and defaulted; the zero itself is unreachable from Go"

	// A bool with omitempty defaulted to true: a typed client drops false,
	// the apiserver re-applies true, so the field cannot be set false from
	// Go. kubectl YAML sends false explicitly, which is why this survives
	// review.
	badBoolCannotBeFalse defaultClass = "omitempty bool defaulted true: cannot be set false from Go"
	// A value marshalled as a JSON scalar (metav1.Duration, resource.Quantity)
	// or object (a struct, corev1.ResourceRequirements) is always sent, so
	// its default never applies to anything created from Go.
	badStructNeverDefaulted defaultClass = "struct-valued: always sent, default never applies from Go (needs a documented floor)"
	// A scalar without omitempty is always sent present-but-zero, so its
	// default never applies to anything created from Go.
	badScalarNeverDefaulted defaultClass = "scalar without omitempty: always sent, default never applies from Go (needs a documented floor)"
)

// defaultFinding is one defaulted field and its class.
type defaultFinding struct {
	key     string // "<group>.<Type>.<Field>"
	pos     string
	goType  string
	value   string
	class   defaultClass
	doc     string
	markers []string
}

// shape is how encoding/json renders a field's Go type.
type shape int

const (
	shapePointer shape = iota
	shapeListOrMap
	shapeScalar       // bool, string, integer, or a named type over one
	shapeObject       // a struct rendered as a JSON object
	shapeScalarStruct // a struct rendered as a JSON scalar, never omitted
	shapeNullWhenZero // a struct whose zero renders as null
)

// externalShapes are the out-of-tree types whose JSON form is not an object.
// Every other external type is treated as an object.
var externalShapes = map[string]shape{
	"k8s.io/apimachinery/pkg/apis/meta/v1.Duration":   shapeScalarStruct,
	"k8s.io/apimachinery/pkg/api/resource.Quantity":   shapeScalarStruct,
	"k8s.io/apimachinery/pkg/util/intstr.IntOrString": shapeScalarStruct,
	"k8s.io/apimachinery/pkg/apis/meta/v1.Time":       shapeNullWhenZero,
	"k8s.io/apimachinery/pkg/apis/meta/v1.MicroTime":  shapeNullWhenZero,
}

var basicKinds = map[string]string{
	"bool": "bool", "string": "string",
	"int": "int", "int8": "int", "int16": "int", "int32": "int", "int64": "int",
	"uint": "int", "uint8": "int", "uint16": "int", "uint32": "int", "uint64": "int",
	"byte": "int",
}

// shapeOf resolves a field type to its JSON shape. For scalars it also
// returns the basic kind ("bool", "string", "int") and the markers of every
// named type on the way down, which is where an Enum usually lives.
func (m *apiModel) shapeOf(expr ast.Expr, from *apiType) (shape, string, []string) {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return shapePointer, "", nil
	case *ast.ArrayType, *ast.MapType:
		return shapeListOrMap, "", nil
	case *ast.StructType:
		return shapeObject, "", nil
	case *ast.Ident:
		if kind, ok := basicKinds[e.Name]; ok {
			return shapeScalar, kind, nil
		}
	}
	if at := m.resolveLocal(expr, from); at != nil {
		if _, ok := at.spec.Type.(*ast.StructType); ok {
			return shapeObject, "", nil
		}
		s, kind, markers := m.shapeOf(at.spec.Type, at)
		return s, kind, append(markers, at.markers...)
	}
	if s, ok := externalShapes[externalName(expr, from)]; ok {
		return s, "", nil
	}
	return shapeObject, "", nil
}

// unquote strips one layer of marker quoting: `"60s"` and `60s` both mean
// the string 60s.
func unquote(v string) string {
	if s, err := strconv.Unquote(v); err == nil {
		return s
	}
	return v
}

func zeroLiteral(kind string) string {
	switch kind {
	case "bool":
		return "false"
	case "int":
		return "0"
	}
	return ""
}

// schemaRejectsZero reports whether the field's validation markers already
// refuse the Go zero value of kind, in which case omitting the zero loses
// nothing a client could have meant.
func schemaRejectsZero(kind string, markers []string) bool {
	if v, ok := markerValue(markers, "kubebuilder:validation:Enum"); ok {
		zero := zeroLiteral(kind)
		for _, e := range strings.Split(v, ";") {
			if unquote(e) == zero {
				return false
			}
		}
		return true
	}
	switch kind {
	case "int":
		if v, ok := markerValue(markers, "kubebuilder:validation:Minimum"); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				if n > 0 {
					return true
				}
				if excl, _ := markerValue(markers, "kubebuilder:validation:ExclusiveMinimum"); n == 0 && excl == "true" {
					return true
				}
			}
		}
		if v, ok := markerValue(markers, "kubebuilder:validation:Maximum"); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n < 0 {
				return true
			}
		}
	case "string":
		if v, ok := markerValue(markers, "kubebuilder:validation:MinLength"); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return true
			}
		}
		if v, ok := markerValue(markers, "kubebuilder:validation:Pattern"); ok {
			if re, err := regexp.Compile(unquote(v)); err == nil && !re.MatchString("") {
				return true
			}
		}
	}
	return false
}

// classifyDefault decides one defaulted field's class.
func (m *apiModel) classifyDefault(f *ast.Field, owner *apiType, value string, fieldMarkers []string) defaultClass {
	tag := parseJSONTag(f)
	s, kind, typeMarkers := m.shapeOf(f.Type, owner)
	switch s {
	case shapePointer:
		return okPointer
	case shapeListOrMap:
		return okListOrMap
	case shapeNullWhenZero:
		return okNullWhenZero
	case shapeScalarStruct:
		if tag.omitzero {
			return okOmitZero
		}
		return badStructNeverDefaulted
	case shapeObject:
		if tag.omitzero {
			return okOmitZero
		}
		if strings.TrimSpace(value) == "{}" {
			return okMaterialiser
		}
		return badStructNeverDefaulted
	}

	if unquote(value) == zeroLiteral(kind) {
		return okDefaultIsZero
	}
	if !tag.omitempty {
		return badScalarNeverDefaulted
	}
	if kind == "bool" {
		return badBoolCannotBeFalse
	}
	if schemaRejectsZero(kind, append(append([]string{}, fieldMarkers...), typeMarkers...)) {
		return okZeroRejected
	}
	return okZeroOmitted
}

// defaultFindings walks every struct field under api/ carrying a
// +kubebuilder:default and classifies it.
func (m *apiModel) defaultFindings() []defaultFinding {
	var out []defaultFinding
	for _, at := range m.sortedTypes() {
		for _, f := range structFields(at) {
			markers := append(markersOf(f.Doc), markersOf(f.Comment)...)
			value, ok := markerValue(markers, "kubebuilder:default")
			if !ok {
				continue
			}
			out = append(out, defaultFinding{
				key:     at.qualified() + "." + fieldName(f, at),
				pos:     m.position(f.Pos()),
				goType:  exprString(f.Type, at),
				value:   value,
				class:   m.classifyDefault(f, at, value, markers),
				doc:     docText(f.Doc),
				markers: markers,
			})
		}
	}
	return out
}

// zeroMeaning matches a doc comment that gives a field's zero value a
// meaning of its own ("0 disables the check", "zero means unlimited"). On an
// omitempty scalar with a non-zero default that promise is unreachable from
// Go: the zero is dropped and defaulted. policy.maxOutputToSourcePercent
// shipped exactly that way ("0 disables the check") until G4-0.
var zeroMeaning = regexp.MustCompile(`(?i)\b(0|zero)\b[^.;]*?\b(disables?|means|turns? off|unlimited|no limit|never)\b`)

// documentsFloor reports whether a field's doc comment says a consumer
// floors its zero -- the D1 Indexer.spec.timeout precedent, and the only
// acceptable answer for a struct-valued default that stays a value.
func documentsFloor(doc string) bool {
	return strings.Contains(strings.ToLower(doc), "floor")
}

// TestNoCRDDefaultIsUnreachableFromGo is G4-0's permanent guard. The same
// defect turned up five times in one phase, each found by hand: a
// +kubebuilder:default that a typed Go client can never reach, because the
// apiserver defaults only an ABSENT field and encoding/json decides what is
// absent. It fails on:
//
//   - a bool with omitempty defaulted to true: false cannot be sent from Go.
//     Make it a *bool -- the only honest fix when false is meaningful.
//   - a struct-valued field (metav1.Duration, resource.Quantity,
//     corev1.ResourceRequirements, a nested struct defaulted to anything but
//     {}) or a scalar without omitempty: always sent, so the default never
//     applies to anything created from Go. Make it a pointer, or floor the
//     zero where it is read and say so in the doc comment ("floors").
//   - an omitempty scalar whose doc comment gives its zero a meaning the
//     default overrides: make it a pointer.
//
// A zero scalar WITH omitempty is omitted and does receive its default
// (E-4's correction), so that alone is not flagged.
func TestNoCRDDefaultIsUnreachableFromGo(t *testing.T) {
	m := loadAPIModel(t)
	findings := m.defaultFindings()
	// A floor, so a walker that silently stopped reading markers cannot pass.
	if len(findings) < 150 {
		t.Fatalf("the walker saw only %d +kubebuilder:default markers under api/; it has stopped reading them", len(findings))
	}

	counts := map[defaultClass]int{}
	for _, f := range findings {
		counts[f.class]++
		switch f.class {
		case badBoolCannotBeFalse:
			t.Errorf("%s (%s): %s %s defaulted to %s: a typed Go client drops false, so it can never be set false. "+
				"Make it a *bool, and read it through an accessor that applies the default to nil",
				f.key, f.pos, f.goType, "with omitempty", f.value)
		case badStructNeverDefaulted, badScalarNeverDefaulted:
			if !documentsFloor(f.doc) {
				t.Errorf("%s (%s): %s defaulted to %s is always sent by a Go client, so the default never applies "+
					"to anything created from Go. Make it a pointer, or floor the zero where it is read and say so "+
					"in the doc comment (\"...floors a zero one to this default\")", f.key, f.pos, f.goType, f.value)
			}
		case okZeroOmitted:
			if loc := zeroMeaning.FindString(f.doc); loc != "" {
				t.Errorf("%s (%s): the doc comment promises %q, but %s with omitempty and default %s drops a zero "+
					"from Go and the apiserver re-defaults it. Make it a pointer", f.key, f.pos, loc, f.goType, f.value)
			}
		}
	}
	classes := make([]string, 0, len(counts))
	for class := range counts {
		classes = append(classes, string(class))
	}
	sort.Strings(classes)
	for _, class := range classes {
		t.Logf("%4d  %s", counts[defaultClass(class)], class)
	}
}

// TestClassifyDefault pins the classifier on one synthetic field per class,
// so the guard above is not trusted only on whatever api/ holds today (which,
// after G4-0, has no bool the first rule could catch).
func TestClassifyDefault(t *testing.T) {
	m := modelFromSource(t, `package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:validation:Enum=a;b
type Mode string

type Nested struct {
	X int32 `+"`json:\"x,omitempty\"`"+`
}

type Spec struct {
	// +kubebuilder:default=true
	BoolTrue bool `+"`json:\"boolTrue,omitempty\"`"+`
	// +kubebuilder:default=true
	BoolPtr *bool `+"`json:\"boolPtr,omitempty\"`"+`
	// +kubebuilder:default=false
	BoolFalse bool `+"`json:\"boolFalse,omitempty\"`"+`
	// +kubebuilder:default=true
	BoolNoOmit bool `+"`json:\"boolNoOmit\"`"+`
	// +kubebuilder:default="30s"
	Dur metav1.Duration `+"`json:\"dur,omitempty\"`"+`
	// Floored: the controller floors a zero one to this default.
	// +kubebuilder:default="30s"
	DurFloored metav1.Duration `+"`json:\"durFloored,omitempty\"`"+`
	// +kubebuilder:default="30s"
	DurOmitZero metav1.Duration `+"`json:\"durOmitZero,omitzero\"`"+`
	// +kubebuilder:default="1Gi"
	Qty resource.Quantity `+"`json:\"qty,omitempty\"`"+`
	// +kubebuilder:default={limits:{cpu:"1"}}
	Res corev1.ResourceRequirements `+"`json:\"res,omitempty\"`"+`
	// +kubebuilder:default={}
	Obj Nested `+"`json:\"obj,omitempty\"`"+`
	// +kubebuilder:default={x:1}
	ObjValued Nested `+"`json:\"objValued,omitempty\"`"+`
	// +kubebuilder:default=5
	IntNoOmit int32 `+"`json:\"intNoOmit\"`"+`
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	IntMin1 int32 `+"`json:\"intMin1,omitempty\"`"+`
	// +kubebuilder:default=5
	IntZeroOK int32 `+"`json:\"intZeroOK,omitempty\"`"+`
	// +kubebuilder:default=a
	Enum Mode `+"`json:\"enum,omitempty\"`"+`
	// +kubebuilder:default={"x"}
	List []string `+"`json:\"list,omitempty\"`"+`
	// +kubebuilder:default=0
	IntZeroDefault int32 `+"`json:\"intZeroDefault\"`"+`
}
`)
	want := map[string]defaultClass{
		"synthetic.Spec.BoolTrue":       badBoolCannotBeFalse,
		"synthetic.Spec.BoolPtr":        okPointer,
		"synthetic.Spec.BoolFalse":      okDefaultIsZero,
		"synthetic.Spec.BoolNoOmit":     badScalarNeverDefaulted,
		"synthetic.Spec.Dur":            badStructNeverDefaulted,
		"synthetic.Spec.DurFloored":     badStructNeverDefaulted,
		"synthetic.Spec.DurOmitZero":    okOmitZero,
		"synthetic.Spec.Qty":            badStructNeverDefaulted,
		"synthetic.Spec.Res":            badStructNeverDefaulted,
		"synthetic.Spec.Obj":            okMaterialiser,
		"synthetic.Spec.ObjValued":      badStructNeverDefaulted,
		"synthetic.Spec.IntNoOmit":      badScalarNeverDefaulted,
		"synthetic.Spec.IntMin1":        okZeroRejected,
		"synthetic.Spec.IntZeroOK":      okZeroOmitted,
		"synthetic.Spec.Enum":           okZeroRejected,
		"synthetic.Spec.List":           okListOrMap,
		"synthetic.Spec.IntZeroDefault": okDefaultIsZero,
	}
	got := map[string]defaultClass{}
	for _, f := range m.defaultFindings() {
		got[f.key] = f.class
		if f.key == "synthetic.Spec.DurFloored" && !documentsFloor(f.doc) {
			t.Errorf("documentsFloor does not see the floor in %q", f.doc)
		}
		if f.key == "synthetic.Spec.Dur" && documentsFloor(f.doc) {
			t.Errorf("documentsFloor sees a floor in %q", f.doc)
		}
	}
	for key, class := range want {
		if got[key] != class {
			t.Errorf("%s: classified %q, want %q", key, got[key], class)
		}
	}
	if len(got) != len(want) {
		t.Errorf("classified %d fields, want %d: %v", len(got), len(want), got)
	}

	for doc, promises := range map[string]bool{
		"MaxOutputToSourcePercent fails a job ... 0 disables the check.":        true,
		"Limit caps results; zero means unlimited.":                             true,
		"Priority orders jobs created from this profile; higher runs first.":    false,
		"CleanupDays is how long a recycled file is kept before it is removed.": false,
	} {
		if got := zeroMeaning.MatchString(doc); got != promises {
			t.Errorf("zeroMeaning(%q) = %v, want %v", doc, got, promises)
		}
	}
}
