// Copyright 2016 Attic Labs, Inc. All rights reserved.
// Licensed under the Apache License, version 2.0:
// http://www.apache.org/licenses/LICENSE-2.0

package types

type listLazyLeafSequence struct {
	leafSequence
	offsets []uint32
	end     uint32
	reader  nomsReader
}

func newListLazyLeafSequence(vr ValueReader, offsets []uint32, end uint32, reader nomsReader) sequence {
	return listLazyLeafSequence{leafSequence{vr, len(offsets), ListKind}, offsets, end, reader}
}

// sequence interface

func (ll listLazyLeafSequence) getCompareFn(other sequence) compareFn {
	oll := other.(listLazyLeafSequence)
	return func(idx, otherIdx int) bool {
		return ll.getItem(idx).(Value).Equals(oll.getItem(otherIdx).(Value))
	}
}

func (ll listLazyLeafSequence) getItem(idx int) sequenceItem {
	// This is an internal part of the data of the chunk so we do not have
	// the hash and therefore we do not need handle the hashCacher logic
	// like we do in DecodeValue.
	pos := ll.reader.pos()
	ll.reader.setPos(ll.offsets[idx])
	defer ll.reader.setPos(pos)
	dec := newValueDecoder(ll.reader, ll.valueReader())
	return dec.readValue()

}

func (ll listLazyLeafSequence) WalkRefs(cb RefCallback) {
	for i := range ll.offsets {
		ll.getItem(i).(Value).WalkRefs(cb)
	}
}

func (ll listLazyLeafSequence) typeOf() *Type {
	ts := make([]*Type, len(ll.offsets))
	for i := range ll.offsets {
		ts[i] = ll.getItem(i).(Value).typeOf()
	}
	return makeCompoundType(ListKind, makeCompoundType(UnionKind, ts...))
}
