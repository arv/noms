// Copyright 2016 Attic Labs, Inc. All rights reserved.
// Licensed under the Apache License, version 2.0:
// http://www.apache.org/licenses/LICENSE-2.0

package types

import (
	"fmt"

	"github.com/attic-labs/noms/go/d"
	"github.com/attic-labs/noms/go/hash"
)

type valueDecoder struct {
	nomsReader
	vr         ValueReader
	validating bool
}

// |tc| must be locked as long as the valueDecoder is being used
func newValueDecoder(nr nomsReader, vr ValueReader) *valueDecoder {
	return &valueDecoder{nr, vr, false}
}

func newValueDecoderWithValidation(nr nomsReader, vr ValueReader) *valueDecoder {
	return &valueDecoder{nr, vr, true}
}

func (r *valueDecoder) readKind() NomsKind {
	return NomsKind(r.readUint8())
}

func (r *valueDecoder) readRef() Ref {
	h := r.readHash()
	targetType := r.readType()
	height := r.readCount()
	return constructRef(h, targetType, height)
}

func (r *valueDecoder) skipRef() {
	r.skipHash()
	r.skipType()
	r.readCount()
}

func (r *valueDecoder) readType() *Type {
	t := r.readTypeInner(map[string]*Type{})
	if r.validating {
		validateType(t)
	}
	return t
}

func (r *valueDecoder) skipType() {
	if r.validating {
		// Sorry, we have to create the type to validate it.
		r.readType()
	}
	r.skipTypeInner()
}

func (r *valueDecoder) readTypeInner(seenStructs map[string]*Type) *Type {
	k := r.readKind()
	switch k {
	case ListKind:
		return makeCompoundType(ListKind, r.readTypeInner(seenStructs))
	case MapKind:
		return makeCompoundType(MapKind, r.readTypeInner(seenStructs), r.readTypeInner(seenStructs))
	case RefKind:
		return makeCompoundType(RefKind, r.readTypeInner(seenStructs))
	case SetKind:
		return makeCompoundType(SetKind, r.readTypeInner(seenStructs))
	case StructKind:
		return r.readStructType(seenStructs)
	case UnionKind:
		return r.readUnionType(seenStructs)
	case CycleKind:
		name := r.readString()
		d.PanicIfTrue(name == "") // cycles to anonymous structs are disallowed
		t, ok := seenStructs[name]
		d.PanicIfFalse(ok)
		return t
	}

	d.PanicIfFalse(IsPrimitiveKind(k))
	return MakePrimitiveType(k)
}

func (r *valueDecoder) skipTypeInner() {
	k := r.readKind()
	switch k {
	case ListKind:
		r.skipTypeInner()
	case MapKind:
		r.skipTypeInner()
		r.skipTypeInner()
	case RefKind:
		r.skipTypeInner()
	case SetKind:
		r.skipTypeInner()
	case StructKind:
		r.skipStructType()
	case UnionKind:
		r.skipUnionType()
	case CycleKind:
		r.skipString()
	default:
		d.PanicIfFalse(IsPrimitiveKind(k))
	}
}

func (r *valueDecoder) readBlobLeafSequence() sequence {
	b := r.readBytes()
	return newBlobLeafSequence(r.vr, b)
}

func (r *valueDecoder) skipBlobLeafSequence() {
	r.skipBytes()
}

func (r *valueDecoder) readValueSequence() ValueSlice {
	count := uint32(r.readCount())

	data := ValueSlice{}
	for i := uint32(0); i < count; i++ {
		v := r.readValue()
		data = append(data, v)
	}

	return data
}

func (r *valueDecoder) skipValueSequence() {
	count := uint32(r.readCount())

	for i := uint32(0); i < count; i++ {
		r.skipValue()
	}
}

func (r *valueDecoder) readLazyValueOffsets() []uint32 {
	count := uint32(r.readCount())
	offsets := make([]uint32, count)

	for i := uint32(0); i < count; i++ {
		offsets[i] = r.pos()
		r.skipValue()
	}

	return offsets
}

func (r *valueDecoder) readListLeafSequence() sequence {
	offsets := r.readLazyValueOffsets()
	return newListLazyLeafSequence(r.vr, offsets, r.pos(), r.nomsReader)
}

func (r *valueDecoder) skipListLeafSequence() {
	r.skipValueSequence()
}

func (r *valueDecoder) readSetLeafSequence() orderedSequence {
	data := r.readValueSequence()
	return setLeafSequence{leafSequence{r.vr, len(data), SetKind}, data}
}

func (r *valueDecoder) skipSetLeafSequence() {
	r.skipValueSequence()
}

func (r *valueDecoder) readMapLeafSequence() orderedSequence {
	count := r.readCount()
	data := []mapEntry{}
	for i := uint64(0); i < count; i++ {
		k := r.readValue()
		v := r.readValue()
		data = append(data, mapEntry{k, v})
	}

	return mapLeafSequence{leafSequence{r.vr, len(data), MapKind}, data}
}

func (r *valueDecoder) skipMapLeafSequence() {
	count := r.readCount()
	for i := uint64(0); i < count; i++ {
		r.skipValue()
		r.skipValue()
	}
}

func (r *valueDecoder) readMetaSequence(k NomsKind, level uint64) metaSequence {
	count := r.readCount()

	data := []metaTuple{}
	for i := uint64(0); i < count; i++ {
		ref := r.readValue().(Ref)
		v := r.readValue()
		var key orderedKey
		if r, ok := v.(Ref); ok {
			// See https://github.com/attic-labs/noms/issues/1688#issuecomment-227528987
			key = orderedKeyFromHash(r.TargetHash())
		} else {
			key = newOrderedKey(v)
		}
		numLeaves := r.readCount()
		data = append(data, newMetaTuple(ref, key, numLeaves, nil))
	}

	return newMetaSequence(k, level, data, r.vr)
}

func (r *valueDecoder) skipMetaSequence(k NomsKind, level uint64) {
	count := r.readCount()
	for i := uint64(0); i < count; i++ {
		r.skipValue() // ref
		r.skipValue() // v
	}
}

func (r *valueDecoder) skipValue() {
	k := r.readKind()
	switch k {
	case BlobKind:
		level := r.readCount()
		if level > 0 {
			r.skipMetaSequence(k, level)
		} else {
			r.skipBlobLeafSequence()
		}
	case BoolKind:
		r.readBool()
	case NumberKind:
		r.readNumber()
	case StringKind:
		r.skipString()
	case ListKind:
		level := r.readCount()
		if level > 0 {
			r.skipMetaSequence(k, level)
		} else {
			r.skipListLeafSequence()
		}
	case MapKind:
		level := r.readCount()
		if level > 0 {
			r.skipMetaSequence(k, level)
		} else {
			r.skipMapLeafSequence()
		}
	case RefKind:
		r.skipRef()
	case SetKind:
		level := r.readCount()
		if level > 0 {
			r.skipMetaSequence(k, level)
		} else {
			r.skipSetLeafSequence()
		}
	case StructKind:
		r.skipStruct()
	case TypeKind:
		r.skipType()
	case CycleKind, UnionKind, ValueKind:
		d.Chk.Fail(fmt.Sprintf("A value instance can never have type %s", k))
	default:
		panic("not reachable")
	}
}

func (r *valueDecoder) readValue() Value {
	k := r.readKind()
	switch k {
	case BlobKind:
		level := r.readCount()
		if level > 0 {
			return newBlob(r.readMetaSequence(k, level))
		}

		return newBlob(r.readBlobLeafSequence())
	case BoolKind:
		return Bool(r.readBool())
	case NumberKind:
		return r.readNumber()
	case StringKind:
		return String(r.readString())
	case ListKind:
		level := r.readCount()
		if level > 0 {
			return newList(r.readMetaSequence(k, level))
		}

		return newList(r.readListLeafSequence())
	case MapKind:
		level := r.readCount()
		if level > 0 {
			return newMap(r.readMetaSequence(k, level))
		}

		return newMap(r.readMapLeafSequence())
	case RefKind:
		return r.readRef()
	case SetKind:
		level := r.readCount()
		if level > 0 {
			return newSet(r.readMetaSequence(k, level))
		}

		return newSet(r.readSetLeafSequence())
	case StructKind:
		return r.readStruct()
	case TypeKind:
		return r.readType()
	case CycleKind, UnionKind, ValueKind:
		d.Chk.Fail(fmt.Sprintf("A value instance can never have type %s", k))
	}

	panic("not reachable")
}

func (r *valueDecoder) readStruct() Value {
	name := r.readString()
	count := r.readCount()

	fieldNames := make([]string, count)
	values := make([]Value, count)
	for i := uint64(0); i < count; i++ {
		fieldNames[i] = r.readString()
		values[i] = r.readValue()
	}

	return Struct{name, fieldNames, values, &hash.Hash{}}
}

func (r *valueDecoder) skipStruct() {
	r.skipString() // name
	count := r.readCount()

	for i := uint64(0); i < count; i++ {
		r.skipString()
		r.skipValue()
	}
}

func boolToUint32(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

func (r *valueDecoder) readStructType(seenStructs map[string]*Type) *Type {
	name := r.readString()
	count := r.readCount()
	fields := make(structTypeFields, count)

	t := newType(StructDesc{name, fields})
	seenStructs[name] = t

	for i := uint64(0); i < count; i++ {
		t.Desc.(StructDesc).fields[i] = StructField{
			Name: r.readString(),
		}
	}
	for i := uint64(0); i < count; i++ {
		t.Desc.(StructDesc).fields[i].Type = r.readTypeInner(seenStructs)
	}
	for i := uint64(0); i < count; i++ {
		t.Desc.(StructDesc).fields[i].Optional = r.readBool()
	}

	return t
}

func (r *valueDecoder) skipStructType() {
	r.skipString() // name
	count := r.readCount()

	for i := uint64(0); i < count; i++ {
		r.skipString() // field name
	}
	for i := uint64(0); i < count; i++ {
		r.skipTypeInner()
	}
	for i := uint64(0); i < count; i++ {
		r.readBool()
	}
}

func (r *valueDecoder) readUnionType(seenStructs map[string]*Type) *Type {
	l := r.readCount()
	ts := make(typeSlice, l)
	for i := uint64(0); i < l; i++ {
		ts[i] = r.readTypeInner(seenStructs)
	}
	return makeCompoundType(UnionKind, ts...)
}

func (r *valueDecoder) skipUnionType() {
	l := r.readCount()
	for i := uint64(0); i < l; i++ {
		r.skipTypeInner()
	}
}
