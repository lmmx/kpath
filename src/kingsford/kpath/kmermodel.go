package main

import (
    "math"
    "log"
)

//===================================================================
// Kmer types
//===================================================================

// Represents a m-order model of kmer distributions
type ArrayKmerModel struct {
    order       uint
    overflow    [][len(ALPHA)]KmerCount
    dist        [][len(ALPHA)]uint8
}

// Create a new kmer model (uses a lot of memory)
func NewArrayKmerModel(order uint) *ArrayKmerModel {
    log.Println("Using big memory array model to hold kmer counts")
    var s uint64 = 1 << (2*order)
    return &ArrayKmerModel{
        order: order,
        overflow: make([][len(ALPHA)]KmerCount, 0, 100000),
        dist: make([][len(ALPHA)]uint8, s),
    }
}

// Return count for given kmer
func (km *ArrayKmerModel) NextCount(k Kmer, c byte) KmerCount {
    if idx, over := km.hasOverflow(k); over  {
        return KmerCount(km.overflow[idx][c])
    } else {
        return KmerCount(km.dist[k][c])
    }
}

// return true if "dist" has overflowed; if true also returns
// the index into overflow
func (km *ArrayKmerModel) hasOverflow(k Kmer) (uint32, bool) {
    if km.dist[k][0] == math.MaxUint8 {
        var idx uint32
        idx = (uint32(km.dist[k][1]) << 16) | (uint32(km.dist[k][2]) << 8) | uint32(km.dist[k][3]) 
        return idx, true
    }
    return 0, false
}

func (km *ArrayKmerModel) createOverflow(k Kmer) uint32 {
    var d [len(ALPHA)]KmerCount
    for c, v := range km.dist[k] {
        d[c] = KmerCount(v)
    }
    
    km.overflow = append(km.overflow, d)
    id := uint32(len(km.overflow)-1)

    DIE_IF(id >= (1<<24), "Too many overflow entries")

    var f [len(ALPHA)]uint8

    f[3] = uint8(id & 0xFF)
    f[2] = uint8((id >> 8) & 0xFF)
    f[1] = uint8((id >> 16) & 0xFF)
    f[0] = math.MaxUint8
    km.dist[k] = f

    return id
}

// return the distribution for the given kmer
func (km *ArrayKmerModel) Distribution(k Kmer) (exists bool, d [len(ALPHA)]KmerCount) {
    if idx, over := km.hasOverflow(k); over {
        return true, km.overflow[idx]
    } else {
        for c, v := range km.dist[k] {
            if v > 0 { exists = true }
            d[c] = KmerCount(v)
        }
        return
    }
}

/*
// returns true if the kmer distribution has been initialized
func (km *ArrayKmerModel) KmerExists(k Kmer) bool {
    for _, v := range km.dist[k] {
        if v > 0 { return true }
    }
    return false
}
*/

// set the value of the given parameter
func (km *ArrayKmerModel) SetCount(k Kmer, c, v byte) {
    km.dist[k][c] = uint8(v)
}


// increment the value of the given count
func (km *ArrayKmerModel) Increment(k Kmer, c, by byte) {
    if idx, over := km.hasOverflow(k); over {
        if uint64(km.overflow[idx][c]) + uint64(by) < MAX_OBSERVATION {
            km.overflow[idx][c] += KmerCount(by)
        }
    } else if uint64(km.dist[k][c])+uint64(by) >= math.MaxUint8 {
        idx := km.createOverflow(k)
        km.overflow[idx][c] += KmerCount(by)
    } else {
        km.dist[k][c] += by
    }
}
