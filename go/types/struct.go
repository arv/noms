// Copyright 2016 Attic Labs, Inc. All rights reserved.
// Licensed under the Apache License, version 2.0:
// http://www.apache.org/licenses/LICENSE-2.0

package types

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/attic-labs/noms/go/d"
	"github.com/attic-labs/noms/go/hash"
)

var EmptyStructType = MakeStructType("")
var EmptyStruct = newStruct("", nil, nil)

type StructData map[string]Value

type Struct struct {
	vrw  ValueReadWriter
	buff []byte
}

// readStruct reads the data provided by a decoder and moves the decoder forward.
func readStruct(dec *valueDecoder) Struct {
	start := dec.pos()
	skipStruct(dec)
	end := dec.pos()
	return Struct{dec.vrw, dec.byteSlice(start, end)}
}

func skipStruct(dec *valueDecoder) {
	dec.skipKind()
	dec.skipString() // name
	count := dec.readCount()
	for i := uint64(0); i < count; i++ {
		dec.skipString()
		dec.skipValue()
	}
}

func (s Struct) writeTo(enc nomsWriter) {
	enc.writeRaw(s.buff)
}

func (s Struct) valueBytes() []byte {
	return s.buff
}

func newStruct(name string, fieldNames []string, values []Value) Struct {
	var vrw ValueReadWriter
	w := newBinaryNomsWriter()
	StructKind.writeTo(&w)
	w.writeString(name)
	w.writeCount(uint64(len(fieldNames)))
	for i := 0; i < len(fieldNames); i++ {
		w.writeString(fieldNames[i])
		if vrw == nil {
			vrw = values[i].(valueReadWriter).valueReadWriter()
		}
		values[i].writeTo(&w)
	}
	return Struct{vrw, w.data()}
}

func NewStruct(name string, data StructData) Struct {
	verifyStructName(name)
	fieldNames := make([]string, len(data))
	values := make([]Value, len(data))

	i := 0
	for name := range data {
		verifyFieldName(name)
		fieldNames[i] = name
		i++
	}

	sort.Sort(sort.StringSlice(fieldNames))
	for i = 0; i < len(fieldNames); i++ {
		values[i] = data[fieldNames[i]]
	}

	return newStruct(name, fieldNames, values)
}

// StructTemplate allows creating a template for structs with a known shape
// (name and fields). If a lot of structs of the same shape are being created
// then using a StructTemplate makes that slightly more efficient.
type StructTemplate struct {
	name       string
	fieldNames []string
}

// MakeStructTemplate creates a new StructTemplate or panics if the name and
// fields are not valid.
func MakeStructTemplate(name string, fieldNames []string) (t StructTemplate) {
	t = StructTemplate{name, fieldNames}

	verifyStructName(name)
	if len(fieldNames) == 0 {
		return
	}
	verifyFieldName(fieldNames[0])
	for i := 1; i < len(fieldNames); i++ {
		verifyFieldName(fieldNames[i])
		d.PanicIfFalse(fieldNames[i] > fieldNames[i-1])
	}
	return
}

// NewStruct creates a new Struct from the StructTemplate. The order of the
// values must match the order of the field names of the StructTemplate.
func (st StructTemplate) NewStruct(values []Value) Struct {
	d.PanicIfFalse(len(st.fieldNames) == len(values))
	return newStruct(st.name, st.fieldNames, values)
}

func (s Struct) Empty() bool {
	return s.Len() == 0
}

// Value interface
func (s Struct) Value() Value {
	return s
}

func (s Struct) Equals(other Value) bool {
	if otherStruct, ok := other.(Struct); ok {
		return bytes.Equal(s.buff, otherStruct.buff)
	}
	return false
}

func (s Struct) Less(other Value) bool {
	return valueLess(s, other)
}

func (s Struct) Hash() hash.Hash {
	return hash.Of(s.buff)
}

func (s Struct) WalkValues(cb ValueCallback) {
	dec, count := s.decoderSkipToFields()
	for i := uint64(0); i < count; i++ {
		dec.skipString()
		cb(dec.readValue())
	}
}

func (s Struct) WalkRefs(cb RefCallback) {
	s.WalkValues(func(v Value) {
		v.WalkRefs(cb)
	})
}

func (s Struct) typeOf() *Type {
	dec := s.decoder()
	return readStructTypeOfValue(&dec)
}

func readStructTypeOfValue(dec *valueDecoder) *Type {
	dec.skipKind()
	name := dec.readString()
	count := dec.readCount()
	typeFields := make(structTypeFields, count)
	for i := uint64(0); i < count; i++ {
		typeFields[i] = StructField{
			Name:     dec.readString(),
			Optional: false,
			Type:     dec.readTypeOfValue(),
		}
	}
	return makeStructTypeQuickly(name, typeFields)
}

func (s Struct) decoder() valueDecoder {
	return newValueDecoder(s.buff, s.vrw)
}

func (s Struct) decoderSkipToFields() (valueDecoder, uint64) {
	dec := s.decoder()
	dec.skipKind()
	dec.skipString()
	count := dec.readCount()
	return dec, count
}

// Len is the number of fields in the struct.
func (s Struct) Len() int {
	_, count := s.decoderSkipToFields()
	return int(count)
}

// Name is the name of the struct.
func (s Struct) Name() string {
	dec := s.decoder()
	dec.skipKind()
	return dec.readString()
}

// IterFields iterates over the fields, calling cb for every field in the
// struct.
func (s Struct) IterFields(cb func(name string, value Value)) {
	dec, count := s.decoderSkipToFields()
	for i := uint64(0); i < count; i++ {
		cb(dec.readString(), dec.readValue())
	}
}

type structPartCallbacks interface {
	name(n string)
	count(c uint64)
	fieldName(n string)
	fieldValue(v Value)
	end()
}

func (s Struct) iterParts(cbs structPartCallbacks) {
	dec := s.decoder()
	dec.skipKind()
	cbs.name(dec.readString())
	count := dec.readCount()
	cbs.count(count)
	for i := uint64(0); i < count; i++ {
		cbs.fieldName(dec.readString())
		cbs.fieldValue(dec.readValue())
	}
	cbs.end()
}

func (s Struct) Kind() NomsKind {
	return StructKind
}

// MaybeGet returns the value of a field in the struct. If the struct does not a have a field with
// the name name then this returns (nil, false).
func (s Struct) MaybeGet(n string) (v Value, found bool) {
	dec, count := s.decoderSkipToFields()
	for i := uint64(0); i < count; i++ {
		name := dec.readString()
		if name == n {
			found = true
			v = dec.readValue()
			return
		}
		if name > n {
			return
		}
		dec.skipValue()
	}

	return
}

// Get returns the value of a field in the struct. If the struct does not a have a field with the
// name name then this panics.
func (s Struct) Get(n string) Value {
	v, ok := s.MaybeGet(n)
	if !ok {
		d.Chk.Fail(fmt.Sprintf(`Struct has no field "%s"`, n))
	}
	return v
}

// Set returns a new struct where the field name has been set to value. If name is not an
// existing field in the struct or the type of value is different from the old value of the
// struct field a new struct type is created.
func (s Struct) Set(n string, v Value) Struct {
	verifyFieldName(n)
	w := newBinaryNomsWriter()
	return s.set(&w, n, v, 0)
}

func (s Struct) set(w *binaryNomsWriter, n string, v Value, addedCount int) Struct {
	// TODO: Reuse bytes if we end up adding a field
	dec := s.decoder()
	StructKind.writeTo(w)
	dec.skipKind()
	dec.copyString(w)
	count := dec.readCount()
	w.writeCount(count + uint64(addedCount))

	newFieldHandled := false

	for i := uint64(0); i < count; i++ {
		name := dec.readString()

		if n == name {
			w.writeString(name)
			v.writeTo(w)
			dec.skipValue()
			newFieldHandled = true
			continue
		}
		if !newFieldHandled && n < name {
			if addedCount == 0 {
				w.reset()
				return s.set(w, n, v, 1)
			}
			w.writeString(n)
			v.writeTo(w)
			newFieldHandled = true
		}
		w.writeString(name)
		dec.copyValue(w)
	}

	if !newFieldHandled {
		if addedCount == 1 {
			// Already adjusted the count
			w.writeString(n)
			v.writeTo(w)
		} else {
			w.reset()
			return s.set(w, n, v, 1)
		}
	}

	return Struct{s.vrw, w.data()}
}

// IsZeroValue can be used to test if a struct is the same as Struct{}.
func (s Struct) IsZeroValue() bool {
	return s.buff == nil
}

// Delete returns a new struct where the field name has been removed.
// If name is not an existing field in the struct then the current struct is returned.
func (s Struct) Delete(n string) Struct {
	dec := s.decoder()
	w := newBinaryNomsWriter()
	StructKind.writeTo(&w)
	dec.skipKind()
	dec.copyString(&w)
	count := dec.readCount()
	w.writeCount(count - 1) // If not found we just return s

	found := false
	for i := uint64(0); i < count; i++ {
		name := dec.readString()

		if n == name {
			dec.skipValue()
			found = true
		} else {
			w.writeString(name)
			dec.copyValue(&w)
		}
	}

	if found {
		return Struct{s.vrw, w.data()}
	}

	return s
}

func (s Struct) Diff(last Struct, changes chan<- ValueChanged, closeChan <-chan struct{}) {
	if s.Equals(last) {
		return
	}
	dec1, dec2 := s.decoder(), last.decoder()
	dec1.skipKind()
	dec2.skipKind()
	dec1.skipString() // Ignore names
	dec2.skipString()
	count1, count2 := dec1.readCount(), dec2.readCount()
	i1, i2 := uint64(0), uint64(0)
	var fn1, fn2 string

	for i1 < count1 && i2 < count2 {
		if fn1 == "" {
			fn1 = dec1.readString()
		}
		if fn2 == "" {
			fn2 = dec2.readString()
		}
		var change ValueChanged
		if fn1 == fn2 {
			v1, v2 := dec1.readValue(), dec2.readValue()
			if !v1.Equals(v2) {
				change = ValueChanged{DiffChangeModified, String(fn1), v2, v1}
			}
			i1++
			i2++
			fn1, fn2 = "", ""
		} else if fn1 < fn2 {
			v1 := dec1.readValue()
			change = ValueChanged{DiffChangeAdded, String(fn1), nil, v1}
			i1++
			fn1 = ""
		} else {
			v2 := dec2.readValue()
			change = ValueChanged{DiffChangeRemoved, String(fn2), v2, nil}
			i2++
			fn2 = ""
		}

		if change != (ValueChanged{}) && !sendChange(changes, closeChan, change) {
			return
		}
	}

	for ; i1 < count1; i1++ {
		if fn1 == "" {
			fn1 = dec1.readString()
		}
		v1 := dec1.readValue()
		if !sendChange(changes, closeChan, ValueChanged{DiffChangeAdded, String(fn1), nil, v1}) {
			return
		}
	}

	for ; i2 < count2; i2++ {
		if fn2 == "" {
			fn2 = dec2.readString()
		}
		v2 := dec2.readValue()
		if !sendChange(changes, closeChan, ValueChanged{DiffChangeRemoved, String(fn2), v2, nil}) {
			return
		}
	}
}

func (s Struct) valueReadWriter() ValueReadWriter {
	return s.vrw
}

var escapeChar = "Q"
var headFieldNamePattern = regexp.MustCompile("[a-zA-Z]")
var tailFieldNamePattern = regexp.MustCompile("[a-zA-Z0-9_]")
var spaceRegex = regexp.MustCompile("[ ]")
var escapeRegex = regexp.MustCompile(escapeChar)

var fieldNameComponentRe = regexp.MustCompile("^" + headFieldNamePattern.String() + tailFieldNamePattern.String() + "*")
var fieldNameRe = regexp.MustCompile(fieldNameComponentRe.String() + "$")

type encodingFunc func(string, *regexp.Regexp) string

func CamelCaseFieldName(input string) string {
	//strip invalid struct characters and leave spaces
	encode := func(s1 string, p *regexp.Regexp) string {
		if p.MatchString(s1) || spaceRegex.MatchString(s1) {
			return s1
		}
		return ""
	}

	strippedField := escapeField(input, encode)
	splitField := strings.Fields(strippedField)

	if len(splitField) == 0 {
		return ""
	}

	//Camelcase field
	output := strings.ToLower(splitField[0])
	if len(splitField) > 1 {
		for _, field := range splitField[1:] {
			output += strings.Title(strings.ToLower(field))
		}
	}
	//Because we are removing characters, we may generate an invalid field name
	//i.e. -- 1A B, we will remove the first bad chars and process until 1aB
	//1aB is invalid struct field name so we will return ""
	if !IsValidStructFieldName(output) {
		return ""
	}
	return output
}

func escapeField(input string, encode encodingFunc) string {
	output := ""
	pattern := headFieldNamePattern
	for _, ch := range input {
		output += encode(string([]rune{ch}), pattern)
		pattern = tailFieldNamePattern
	}
	return output
}

// EscapeStructField escapes names for use as noms structs with regards to non CSV imported data.
// Disallowed characters are encoded as 'Q<hex-encoded-utf8-bytes>'.
// Note that Q itself is also escaped since it is the escape character.
func EscapeStructField(input string) string {
	if !escapeRegex.MatchString(input) && IsValidStructFieldName(input) {
		return input
	}
	encode := func(s1 string, p *regexp.Regexp) string {
		if p.MatchString(s1) && s1 != escapeChar {
			return s1
		}

		var hs = fmt.Sprintf("%X", s1)
		var buf bytes.Buffer
		buf.WriteString(escapeChar)
		if len(hs) == 1 {
			buf.WriteString("0")
		}
		buf.WriteString(hs)
		return buf.String()
	}
	return escapeField(input, encode)
}

// IsValidStructFieldName returns whether the name is valid as a field name in a struct.
// Valid names must start with `a-zA-Z` and after that `a-zA-Z0-9_`.
func IsValidStructFieldName(name string) bool {
	return fieldNameRe.MatchString(name)
}

func verifyFields(fs structTypeFields) {
	for i, f := range fs {
		verifyFieldName(f.Name)
		if i > 0 && strings.Compare(fs[i-1].Name, f.Name) >= 0 {
			d.Chk.Fail("Field names must be unique and ordered alphabetically")
		}
	}
}

func verifyName(name, kind string) {
	if !IsValidStructFieldName(name) {
		d.Panic(`Invalid struct%s name: "%s"`, kind, name)
	}
}

func verifyFieldName(name string) {
	verifyName(name, " field")
}

func verifyStructName(name string) {
	if name != "" {
		verifyName(name, "")
	}
}
