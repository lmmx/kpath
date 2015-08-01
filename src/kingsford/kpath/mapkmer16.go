package main

type FullMapKmerModel map[Kmer][len(ALPHA)]KmerCount

func NewFullMapKmerModel(order uint) *FullMapKmerModel {
    v := make(FullMapKmerModel)
    return &v
}

func (km *FullMapKmerModel) NextCount(k Kmer, c byte) KmerCount {
    return (*km)[k][c]
}

func (km *FullMapKmerModel) Distribution(k Kmer) [len(ALPHA)]KmerCount {
    return (*km)[k]
}

// check if the kmer is in the map
func (km *FullMapKmerModel) KmerExists(k Kmer) bool {
    _, ok := (*km)[k]
    return ok
}

// set the value of the 
func (km *FullMapKmerModel) SetCount(k Kmer, c, v byte) {
    entry := (*km)[k]
    entry[c] = KmerCount(v)
    (*km)[k] = entry
}

func (km *FullMapKmerModel) Increment(k Kmer, c, by byte) {
    entry := (*km)[k]
    if uint64(entry[c]) + uint64(by) < MAX_OBSERVATION { 
        entry[c] += KmerCount(by)
        (*km)[k] = entry
    }
}
