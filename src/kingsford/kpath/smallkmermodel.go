package main

import (
    "math"
    "log"
)

//===================================================================
// Kmer types
//===================================================================

// Represents a m-order model of kmer distributions
type SmallKmerModel struct {
    order       uint
    overflow    [][len(ALPHA)]KmerCount
    dist        map[Kmer][len(ALPHA)]uint8
}

// Create a new kmer model (uses a lot of memory)
func NewSmallKmerModel(order uint) *SmallKmerModel {
    log.Println("Creating small kmer count model.")
    return &SmallKmerModel{
        order: order,
        overflow: make([][len(ALPHA)]KmerCount, 0),
        dist: make(map[Kmer][len(ALPHA)]uint8, 100000),
    }
}

// Return count for given kmer; assumes kmer exists
func (km *SmallKmerModel) NextCount(k Kmer, c byte) KmerCount {
    if idx, entry, over := km.hasOverflow(k); over  {
        return KmerCount(km.overflow[idx][c])
    } else {
        return KmerCount(entry[c])
    }
}

// return true if "dist" has overflowed; if true also returns
// the index into overflow
func (km *SmallKmerModel) hasOverflow(k Kmer) (uint32, [len(ALPHA)]uint8, bool) {
    entry := km.dist[k]
    if entry[0] == math.MaxUint8 {
        var idx = (uint32(entry[1]) << 16) | (uint32(entry[2]) << 8) | uint32(entry[3]) 
        return idx, entry, true
    }
    return 0, entry, false
}

func (km *SmallKmerModel) createOverflow(k Kmer) uint32 {
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
func (km *SmallKmerModel) Distribution(k Kmer) (exists bool, d [len(ALPHA)]KmerCount) {
    // if kmer exists, compute its distribution
    if entry, ok := km.dist[k]; ok {
        // if we overflow, return overflowed dist
        if entry[0] == math.MaxUint8 {
            var idx = (uint32(entry[1]) << 16) | (uint32(entry[2]) << 8) | uint32(entry[3])
            return true, km.overflow[idx]
        } else {
            // if we didn't overflow, must covert
            exists = true
            for c, v := range entry {
                d[c] = KmerCount(v)
            }
            return
        }
    }
    return
}

/*
// returns true if the kmer distribution has been initialized
func (km *SmallKmerModel) KmerExists(k Kmer) bool {
    _, ok := km.dist[k]
    return ok
}
*/

// set the value of the given parameter
func (km *SmallKmerModel) SetCount(k Kmer, c, v byte) {
    entry := km.dist[k]
    entry[c] = uint8(v)
    km.dist[k] = entry
}


// increment the value of the given count
func (km *SmallKmerModel) Increment(k Kmer, c, by byte) {
    if idx, entry, over := km.hasOverflow(k); over {
        if uint64(km.overflow[idx][c]) + uint64(by) < MAX_OBSERVATION {
            km.overflow[idx][c] += KmerCount(by)
        }
    } else {
        if uint64(entry[c])+uint64(by) >= math.MaxUint8 {
            idx := km.createOverflow(k)
            km.overflow[idx][c] += KmerCount(by)
        } else {
            entry[c] += by
            km.dist[k] = entry
        }
    }
}
