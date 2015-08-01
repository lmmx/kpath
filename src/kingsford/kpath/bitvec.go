package main

type BitVec struct {
    length uint64
    data []uint64
}

func NewBitVec(length uint64) *BitVec {
    numRec := length / 64
    if length % 64 != 0 { numRec++ }
    return &BitVec{
        length: length,
        data: make([]uint64, numRec),
    }
}

func (bv *BitVec) Get(i uint64) bool {
    return bv.data[i/64] & (1 << (i%64)) != 0 
}

func (bv *BitVec) SetOn(i uint64) {
    bv.data[i/64] |= (1 << (i%64))
}


func (bv *BitVec) Set(i uint64, b bool) {
    word := i / 64
    bit := 63 - (i % 64)
    if b {
        bv.data[word] |= (1 << bit)
    } else {
        bv.data[word] &= ^(1 << bit)
    }
}
