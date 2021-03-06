/*
   kpath - Compression of short-read sequence data
   Copyright (C) 2014  Carl Kingsford & Rob Patro

   This program is free software: you can redistribute it and/or modify
   it under the terms of the GNU General Public License as published by
   the Free Software Foundation, either version 3 of the License, or
   (at your option) any later version.

   This program is distributed in the hope that it will be useful,
   but WITHOUT ANY WARRANTY; without even the implied warranty of
   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
   GNU General Public License for more details.

   You should have received a copy of the GNU General Public License
   along with this program.  If not, see <http://www.gnu.org/licenses/>.

   Contact: carlk@cs.cmu.edu
*/

package main

/* Version January 10, 2015 */

import (
	"bufio"
	"compress/gzip"
	"crypto/md5"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"os"
	"runtime"
	"runtime/pprof"
    "runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"kingsford/kpath/arithc"
	"kingsford/kpath/bitio"
)

// A Kmer represents a kmer of size <= 16.
type Kmer uint32

// A KmerCount holds the counts for the # of times a transition is observed
type KmerCount uint16

// MAX_OBERSERVATION should be the largest value that can be stored in a
// KmerCount
const MAX_OBSERVATION = math.MaxUint16

// the interface for the model storage
type KmerModel interface {
    NextCount(k Kmer, c byte) KmerCount
    Distribution(k Kmer) (bool, [len(ALPHA)]KmerCount)
    SetCount(k Kmer, c, v byte)
    Increment(k Kmer, c, by byte)
}


/*
// A WeightXformFcn represents a function that can transform distribution
// counts.
type WeightXformFcn func(int, [len(ALPHA)]KmerCount) uint64
*/

//===================================================================
// Globals
//===================================================================

var (
	encodeFlags   *flag.FlagSet
	outFile       string
	refFile       string
	readFile      string
	globalK       int
	shiftKmerMask Kmer

	defaultInterval    [len(ALPHA)]uint32 = [...]uint32{2, 2, 2, 2}
	defaultIntervalSum uint64             = 4 * 2

	contextExists int
	flipped       int
)

const (
    SMALL_MODEL = 1
    ARRAY_MODEL = 2
)

var (
	flipReadsOption    bool = true
	dupsOption         bool = true
	writeNsOption      bool = true
	writeFlippedOption bool = true
	updateReference    bool = true
	maxThreads         int  = 10
	outputFastaOption  bool = true

    useArrayModel      bool = false

	cpuProfile      string = ""    // set to nonempty to write profile to this file
	writeQualOption bool   = false // NYI completely
	observationWeight int = 10
)

const (
	pseudoCount       uint64    = 1
	seenThreshold     KmerCount = 2 // before this threshold, increment 1 and treat as unseen
)

//===================================================================
// Int <-> String Kmer representations
//===================================================================

// acgt() takes a letter and returns the index in 0,1,2,3 to which it is
// mapped. 'N's become 'A's and any other letter induces a panic.
func acgt(a byte) byte {
	switch a {
	case 'A':
		return 0
	case 'N':
		return 0
	case 'C':
		return 1
	case 'G':
		return 2
	case 'T':
		return 3
	}
	panic(fmt.Errorf("Bad character: %s!", string(a)))
}

// baseFromBits() returns the ASCII letter for the given 2-bit encoding.
func baseFromBits(a byte) byte {
	return "ACGT"[a]
}

// stringToKmer() converts a string to a 2-bit kmer representation.
func stringToKmer(kmer string) Kmer {
	var x uint64
	for _, c := range kmer {
		x = (x << 2) | uint64(acgt(byte(c)))
	}
	return Kmer(x)
}

// isACGT() returns true iff the given character is one of A,C,G, or T.
func isACGT(c rune) bool {
	return c == 'A' || c == 'C' || c == 'G' || c == 'T'
}

// kmerToString() unpacks a 2-bit encoded kmer into a string.
func kmerToString(kmer Kmer, k int) string {
	s := make([]byte, k)
	for i := 0; i < k; i++ {
		s[k-i-1] = baseFromBits(byte(kmer & 0x3))
		kmer >>= 2
	}
	return string(s)
}

// setShiftKmerMask() initializes the kmer mask. This must be called anytime
// globalK changes.
func setShiftKmerMask() {
	for i := 0; i < globalK; i++ {
		shiftKmerMask = (shiftKmerMask << 2) | 3
	}
}

// shiftKmer() creates a new kmer by shifting the given one over one base to
// the left and adding the given next character at the right.
func shiftKmer(kmer Kmer, next byte) Kmer {
	return ((kmer << 2) | Kmer(next)) & shiftKmerMask
}

// RC computes the reverse complement of a single given nucleotide. Ns become
// Ts as if they were As. Any other character induces a panic.
func RC(c byte) byte {
	switch c {
	case 'A':
		return 'T'
	case 'N':
		return 'N'
	case 'C':
		return 'G'
	case 'G':
		return 'C'
	case 'T':
		return 'A'
	}
	panic(fmt.Errorf("Bad character: %s!", string(c)))
}

// reverseComplement() returns the reverse complement of the given string
func reverseComplement(r string) string {
	s := make([]byte, len(r))
	for i := 0; i < len(r); i++ {
		s[len(r)-i-1] = RC(r[i])
	}
	return string(s)
}

// AbsInt() computes the absolute value of an integer.
func AbsInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

//===================================================================


// readReferenceFile() reads the sequences in the gzipped multifasta file with
// the given name and returns them as a slice of strings.
func readReferenceFile(fastaFile string) []string {
	// open the .gz fasta file that is the references
	log.Println("Reading Reference File...")
	inFasta, err := os.Open(fastaFile)
	DIE_ON_ERR(err, "Couldn't open fasta file %s", fastaFile)
	defer inFasta.Close()

	// wrap the gzip reader around it
	in, err := gzip.NewReader(inFasta)
	DIE_ON_ERR(err, "Couldn't open gzipped file %s", fastaFile)
	defer in.Close()

	out := make([]string, 0, 10000000)
	cur := make([]string, 0, 100)

	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.ToUpper(scanner.Text()))
		if len(line) == 0 {
			continue
		}

		if line[0] == byte('>') {
			if len(cur) > 0 {
				out = append(out, strings.Join(cur, ""))
				cur = make([]string, 0, 100)
			}
		} else {
			cur = append(cur, line)
		}
	}
	DIE_ON_ERR(scanner.Err(), "Couldn't finish reading reference")
	return out
}

// countKmersInReference() reads the given reference file (gzipped multifasta)
// and constructs a kmer hash for it that mapps kmers to distributions of next
// characters.
func countKmersInReference(k int, seqs []string) KmerModel {
    var km KmerModel
    if useArrayModel {
        km = NewArrayKmerModel(uint(k))
    } else {
        km = NewSmallKmerModel(uint(k))
    }

	log.Printf("Counting %v-mer transitions in reference file...\n", k)
	for _, s := range seqs {
		if len(s) <= k {
			continue
		}
		contextMer := stringToKmer(s[:k])
		for i := 0; i < len(s)-k; i++ {
			next := acgt(s[i+k])
			// seeing something in the reference gives us a count of seenThreshold
            km.SetCount(contextMer, next, byte(seenThreshold))

			contextMer = shiftKmer(contextMer, next)
		}
	}
	return km
}

func createKmerBitVectorFromReference(k int, seqs []string) *BitVec {

    bv := NewBitVec(1 << (2*uint(k)))

    for _, s := range seqs {
		if len(s) <= k {
			continue
		}
		contextMer := stringToKmer(s[:k])
		for i := 0; i < len(s)-k; i++ {
            bv.SetOn(uint64(contextMer))
            DIE_IF(bv.Get(uint64(contextMer)) != true, "Bad bit vector!")
			next := acgt(s[i+k])
			contextMer = shiftKmer(contextMer, next)
		}
	}
	return bv
}


//===================================================================
// Encoding
//===================================================================

// contextWeight() is a weight transformation function that will change the
// distribution weights according to the function for real contexts. If the
// count is too small, it returns the pseudocount; if the count is big enough
// it returns observationWeight * the distribution value.
func contextWeight(charIdx int, dist [len(ALPHA)]KmerCount) uint64 {
	if dist[charIdx] >= seenThreshold {
		return uint64(observationWeight) * uint64(dist[charIdx])
	} else {
		return pseudoCount
	}
}

// defaultWeight() is a weight transformation function for the default
// distribution. It returns the weight unchanged.
func defaultWeight(charIdx int, dist [len(ALPHA)]KmerCount) uint64 {
	return uint64(dist[charIdx])
}

// intervalFor() returns the interval for the given character (represented as a
// 2-bit encoded base) according to the given distribution (transformed by the
// given weight transformation function).
func intervalFor(
	letter byte,
	dist [len(ALPHA)]KmerCount,
) (a uint64, b uint64, total uint64) {

	letterIdx := int(letter)
	for i := 0; i < len(dist); i++ {
		w := contextWeight(i, dist)

		total += w
		if i <= letterIdx {
			b += w
			if i < letterIdx {
				a += w
			}
		}
	}
	return
}

// intervalForDefault() computes the interval for the given character using the
// default interval
func intervalForDefault(letter byte) (a uint64, b uint64, total uint64) {
	letterIdx := int(letter)
	for i := 0; i < len(defaultInterval); i++ {
		w := uint64(defaultInterval[i])
		total += w
		if i <= letterIdx {
			b += w
			if i < letterIdx {
				a += w
			}
		}
	}
	return
}

// nextInterval() computes the interval for the given context and updates the
// default distribution and context distributions as required.
func nextInterval(
	km KmerModel,
	contextMer Kmer,
	kidx byte,
	computeInterval bool,
) (a uint64, b uint64, total uint64) {
	// if the context exists, use that distribution
    if exists, dist := km.Distribution(contextMer); exists {
		contextExists++
		if computeInterval {
			a, b, total = intervalFor(kidx, dist)
		}
		if updateReference {
            km.Increment(contextMer, kidx, 1)
		}
	} else {
		// if the context doesnt exist, use a simple default interval
		if computeInterval {
			a, b, total = intervalForDefault(kidx)
		}
		defaultInterval[kidx]++
		defaultIntervalSum++

		if updateReference {
			// add this to the context now
            km.Increment(contextMer, kidx, 1)
		}
	}
	return
}

// countMatchingObservations() counts the number of observaions of kmers in the
// read.
func countMatchingObservations(bv *BitVec, r string) (n KmerCount) {
	contextMer := stringToKmer(r[:globalK])
	for i := globalK; i < len(r); i++ {
		symb := acgt(r[i])
        nextMer := shiftKmer(contextMer, symb)
        if bv.Get(uint64(contextMer)) && bv.Get(uint64(nextMer)) {
			n += seenThreshold
		}
        contextMer = nextMer
	}
	return
}

// support sorting the fastq list lexicographically
type Lexicographically []*FastQ

func (a Lexicographically) Len() int { return len(a) }

func (a Lexicographically) Swap(i, j int) { a[i], a[j] = a[j], a[i] }

func (a Lexicographically) Less(i, j int) bool {
	for i, c := range a[i].Seq[:globalK] {
		d := a[j].Seq[i]
		if c < d {
			return true
		}
		if c > d {
			return false
		}
	}
	return false
}

// flipRange() flips the reads in the given slice if the reverse complement
// matches the reference better.
func flipRange(block []*FastQ, bv *BitVec) int {
	flip := 0
	for _, fq := range block {
		n1 := countMatchingObservations(bv, string(fq.Seq))
		rcr := reverseComplement(string(fq.Seq))
		n2 := countMatchingObservations(bv, rcr)

		// if they are tied, take the lexigographically smaller one
		if n2 > n1 || (n2 == n1 && string(rcr) < string(fq.Seq)) {
			fq.SetReverseComplement(rcr)
			flip++
		}
	}
	return flip
}

// readAndFlipReads() reads the reads and reverse complements them if the
// reverse complement matches the hash better (according to a countMatching*
// function above). It returns a slice of the reads. "N"s are treated as "A"s.
// No other characters are transformed and will eventually lead to a panic.
func readAndFlipReads(
	readFile string,
	bv *BitVec,
	flipReadsOption bool,
) []*FastQ {
	// read the reads from the file into memory
	log.Printf("Reading reads...")
	readStart := time.Now()
	fq := make(chan *FastQ, 10000000)
	go ReadFastQ(readFile, fq)
	reads := make([]*FastQ, 0, 10000000)
	for rec := range fq {
		reads = append(reads, rec)
	}
	readEnd := time.Now()
	log.Printf("Time: read %v reads; spent %v seconds.",
		len(reads), readEnd.Sub(readStart).Seconds())

	// if enabled, start several threads to flip the reads
	if flipReadsOption {
		// start maxThreads-1 workers to flip the read ranges
		wait := make([]chan int, maxThreads-1)
		for i := range wait {
			wait[i] = make(chan int)
		}
		blockSize := 1 + len(reads)/len(wait)
		log.Printf("Have %v read flippers, each working on %v reads",
			len(wait), blockSize)
		for i, c := range wait {
			go func(i int, c chan int) {
				end := (i + 1) * blockSize
				if end > len(reads) {
					end = len(reads)
				}
				log.Printf("Worker %v flipping [%d, %d)...", i, i*blockSize, end)
				count := flipRange(reads[i*blockSize:end], bv)
				c <- count
				close(c)
				runtime.Goexit()
				return
			}(i, c)
		}

		// wait for all the workers to finish and sum up their
		for _, c := range wait {
			for f := range c {
				flipped += f
			}
		}
	}
	flipEnd := time.Now()
	log.Printf("Time: flipping: %v seconds.", flipEnd.Sub(readEnd).Seconds())

	// sort the records by sequence
	sort.Sort(Lexicographically(reads))
	readSort := time.Now()
	log.Printf("Time: sorting reads: %v seconds.", readSort.Sub(flipEnd).Seconds())

	log.Printf("Read %v reads; flipped %v of them.", len(reads), flipped)
	return reads

}

// listBuckets() processes the reads and creates the bucket list and the list
// of the bucket sizes and returns them.
func listBuckets(reads []*FastQ) ([]string, []int) {
	curBucket := ""
	prevRead := ""
	allSame := false
	buckets := make([]string, 0, 1000000)
	counts := make([]int, 0, 1000000)

	for _, rec := range reads {
		r := string(rec.Seq)
		if r[:globalK] != curBucket {
			// if all the reads in a bucket are the same, record this
			// by negating the bucket count
			if dupsOption && allSame && counts[len(counts)-1] > 1 {
				counts[len(counts)-1] = -counts[len(counts)-1]
			}

			curBucket = r[:globalK]
			prevRead = r
			buckets = append(buckets, curBucket)
			counts = append(counts, 1)
			allSame = true
		} else {
			allSame = allSame && (r == prevRead)
			prevRead = r
			counts[len(counts)-1]++
		}
	}
	if dupsOption && allSame && counts[len(counts)-1] > 1 {
		counts[len(counts)-1] = -counts[len(counts)-1]
	}
	return buckets, counts
}

// writeCounts() writes the counts list out to the given writer.
func writeCounts(f io.Writer, readlen int, counts []int) {
	log.Printf("Writing counts...")
	fmt.Fprintf(f, "%d ", readlen)
	for _, c := range counts {
		fmt.Fprintf(f, "%d ", c)
	}
	log.Printf("Done; wrote %d counts.", len(counts))
}

// writeNLocations() writes out the locations of the translated Ns in the file.
func writeNLocations(f io.Writer, reads []*FastQ) {
	log.Printf("Writing location of Ns...")
	// every read's locations are written as a space separated list of ascii
	// integers
	c := 0
	for _, fq := range reads {
		for i, p := range fq.NLocations {
			fmt.Fprintf(f, "%d", p)
			c++
			if i != len(fq.NLocations)-1 {
				fmt.Fprintf(f, " ")
			}
		}
		fmt.Fprintf(f, "\n")
	}
	log.Printf("Done; wrote %d Ns.", c)
}

// writeFlipped() writes out a stream of bits that says whether or not the
// reads were flipped.
func writeFlipped(out *bitio.Writer, reads []*FastQ) {
	for _, fq := range reads {
		if fq.IsFlipped {
			out.WriteBit(1)
		} else {
			out.WriteBit(0)
		}
	}
}


// encodeWithBuckets() reads the reads, creates the buckets, saves the buckets
// and their counts, and then encodes each read.
func preprocessWithBuckets(
	readFile string,
	outBaseName string,
	bv *BitVec,
) (*os.File, []string, []int) {
	// read the reads and flip as needed
	reads := readAndFlipReads(readFile, bv, flipReadsOption)

	readLength := len(reads[0].Seq)

	log.Printf("Estimated 2-bit encoding size: %d",
		uint64(math.Ceil(float64(2*len(reads)*readLength)/8.0)))

	// if the user wants the qualities written out
	waitForFlipped := make(chan struct{})
	if writeFlippedOption {
		outFlipped, err := os.Create(outBaseName + ".flipped")
		DIE_ON_ERR(err, "Couldn't create flipped file: %s", outBaseName+".flipped")
		defer outFlipped.Close()

		outFlippedZ, err := gzip.NewWriterLevel(outFlipped, gzip.BestCompression)
		DIE_ON_ERR(err, "Couldn't create gzipper for flipped file.")
		defer outFlippedZ.Close()

		flippedBits := bitio.NewWriter(outFlippedZ)
		defer flippedBits.Close()

		go func() {
			writeFlipped(flippedBits, reads)
			close(waitForFlipped)
			runtime.Goexit()
			return
		}()
	} else {
		close(waitForFlipped)
	}

	// if the user wants to write out the N positions, write them out
	waitForNs := make(chan struct{})
	if writeNsOption {
		outNs, err := os.Create(outBaseName + ".ns")
		DIE_ON_ERR(err, "Couldn't create N location file: %s", outBaseName+".ns")
		defer outNs.Close()

		outNsZ, err := gzip.NewWriterLevel(outNs, gzip.BestCompression)
		DIE_ON_ERR(err, "Couldn't create gzipper for N location file.")
		defer outNsZ.Close()

		go func() {
			writeNLocations(outNsZ, reads)
			close(waitForNs)
			runtime.Goexit()
			return
		}()
	} else {
		close(waitForNs)
	}

	// create the buckets and counts
	buckets, counts := listBuckets(reads)

	// write the bittree for the bucket out to a file
	outBT, err := os.Create(outBaseName + ".bittree")
	DIE_ON_ERR(err, "Couldn't create bucket file: %s", outBaseName+".bittree")
	defer outBT.Close()

	// compress the file with gzip as we are writing it
	outBZ, err := gzip.NewWriterLevel(outBT, gzip.BestCompression)
	DIE_ON_ERR(err, "Couldn't create gzipper for bucket file")
	defer outBZ.Close()

	// create a writer that lets us write bits
	writer := bitio.NewWriter(outBZ)
	defer writer.Close()

	/*** The main work to encode the bucket names ***/
	waitForBuckets := make(chan struct{})
	go func() {
		encodeKmersToFile(buckets, writer)
		close(waitForBuckets)
		runtime.Goexit()
		return
	}()

	// write out the counts
	countF, err := os.Create(outBaseName + ".counts")
	DIE_ON_ERR(err, "Couldn't create counts file: %s", outBaseName+".counts")
	defer countF.Close()

	// compress it as we are writing it
	countZ, err := gzip.NewWriterLevel(countF, gzip.BestCompression)
	DIE_ON_ERR(err, "Couldn't create gzipper for count file")
	defer countZ.Close()

	/*** The main work to encode the bucket counts ***/
	waitForCounts := make(chan struct{})
	go func() {
		writeCounts(countZ, readLength, counts)
		close(waitForCounts)
		runtime.Goexit()
		return
	}()

	// create a temp file containing the processed reads
	processedFile, err := ioutil.TempFile("", "kpath-encode-")
	DIE_ON_ERR(err, "Couldn't create temporary file in %s", os.TempDir())
	md5Hash := md5.New()
	waitForTemp := make(chan struct{})
	go func() {
		for i := range reads {
			md5Hash.Write(reads[i].Seq)
			processedFile.Write(reads[i].Seq)
			processedFile.Write([]byte{'\n'})
		}
		processedFile.Seek(0, 0)
		close(waitForTemp)
	}()


	// Wait for each of the coders to finish
	<-waitForBuckets
	<-waitForCounts
	<-waitForNs
	<-waitForFlipped
	<-waitForTemp
	log.Printf("MD5 hash of reads = %x", md5Hash.Sum(nil))

	log.Printf("Done processing; reads are of length %d ...", readLength)
	return processedFile, buckets, counts
}

// encodeSingleReadWithBucket() encodes a single read: uses a bucketing scheme
// for initial part, and arithmetic encoding for the rest.
func encodeSingleReadWithBucket(contextMer Kmer, r string, km KmerModel, coder *arithc.Encoder) {
	// encode rest using the reference probs
	for i := globalK; i < len(r); i++ {
		char := acgt(r[i])
		a, b, total := nextInterval(km, contextMer, char, true)
		coder.Encode(a, b, total)
		contextMer = shiftKmer(contextMer, char)
	}
}

// encodeReadsFromTempFile() reads the newline seperated reads from tempFile
// and encodes them using the information in buckets, counts, hash. It writes
// to the given arithmetic coder.  buckets, counts and tempFile are obtained
// with preprocessWithBuckets().
func encodeReadsFromTempFile(
	tempFile *os.File,
	buckets []string,
	counts []int,
	km KmerModel,
	coder *arithc.Encoder,
) (n int) {
	/*** The main work to encode the read tails ***/
	log.Printf("Currently have %v Go routines...", runtime.NumGoroutine())
	runtime.GC()
	runtime.LockOSThread()

	buf := bufio.NewReader(tempFile)

	encodeStart := time.Now()
	log.Printf("Encoding reads...")

	for i, c := range counts {
		bucketMer := stringToKmer(buckets[i])
		if c > 0 {
			// write out the given number of reads
			for j := 0; j < c; j++ {
				r, err := buf.ReadString('\n')
				DIE_ON_ERR(err, "Couldn't read from temp file %s", tempFile.Name())
				encodeSingleReadWithBucket(bucketMer, r[:len(r)-1], km, coder)
				n++
			}
		} else {
			// all the reads in this bucket are the same, so just write one
			// and skip past the rest.
			r, err := buf.ReadString('\n')
			DIE_ON_ERR(err, "Couldn't read from temp file %s", tempFile.Name())
			encodeSingleReadWithBucket(bucketMer, r[:len(r)-1], km, coder)

			// skip past c-1 reads that should be identical
			for j := 1; j < AbsInt(c); j++ {
				buf.ReadString('\n')
				DIE_ON_ERR(err, "Couldn't read from temp file %s", tempFile.Name())
			}
			n++
		}
	}

	log.Printf("done. Took %v seconds to encode the tails.",
		time.Now().Sub(encodeStart).Seconds())
	runtime.UnlockOSThread()

	tempFile.Close()
	err := os.Remove(tempFile.Name())
	DIE_ON_ERR(err, "Couldn't delete temp file %s", tempFile.Name())

	return
}

//===============================================================================
// DECODING
//===============================================================================

// readBucketCounts() opens the file with the given name and parses it to
// extract a list of bucket sizes that were written by the encoding. The given
// file must have been written by the coder --- it is assumed to be a gzipped
// list of space-separated ASCII numbers.
func readBucketCounts(countsFN string) ([]int, int) {
	log.Printf("Reading bucket counts from %v", countsFN)

	// open the count file
	c1, err := os.Open(countsFN)
	DIE_ON_ERR(err, "Couldn't open count file: %s", countsFN)
	defer c1.Close()

	// the count file is compressed with gzip; uncompress it as we read it
	c, err := gzip.NewReader(c1)
	DIE_ON_ERR(err, "Couldn't create gzip reader: %v")
	defer c.Close()

	var n, readlen int
	_, err = fmt.Fscanf(c, "%d", &readlen)
	DIE_ON_ERR(err, "Couldn't read read length from counts file")

	counts := make([]int, 0)
	err = nil
	sum := 0
	dupBucketCount := 0
	for x := 1; err == nil && x > 0; {
		x, err = fmt.Fscanf(c, "%d", &n)
		if x > 0 && err == nil {
			sum += AbsInt(n)
			if n < 0 {
				dupBucketCount++
			}
			counts = append(counts, n)
		}
	}
	log.Printf("Number of uniform buckets = %d\n", dupBucketCount)
	log.Printf("Total counts = %d\n", sum)
	log.Printf("done; read %d counts", len(counts))
	return counts, readlen
}

// readFlipped() reads the compressed bitstream that indicates whether a read
// was flipped or not. If the file does not exist, returns nil.
func readFlipped(flippedFN string) []bool {
	// open the file; return empty if nothing there
	flippedIn, err := os.Open(flippedFN)
	if err == nil {
		log.Printf("Reading flipped bits from %s", flippedFN)
		defer flippedIn.Close()

		flippedZ, err := gzip.NewReader(flippedIn)
		DIE_ON_ERR(err, "Couldn't create unzipper for flipped file")
		defer flippedZ.Close()

		flippedBits := bitio.NewReader(bufio.NewReader(flippedZ))
		defer flippedBits.Close()

		flipped := make([]bool, 0, 1000000)
		for {
			b, err := flippedBits.ReadBit()
			if err != nil {
				break
			}
			if b > 0 {
				flipped = append(flipped, true)
			} else {
				flipped = append(flipped, false)
			}
		}
		log.Printf("Read %d bits indicating whether reads were flipped.", len(flipped))
		return flipped
	} else {
		log.Printf("No flipped bit file (%s) found; ignoring.", flippedFN)
		return nil
	}
}

// readNLocations() reads the compressed N location file and returns a slice of
// slices that contain the positions of the Ns. An optimization is made that if
// there are no Ns in a read, then out[r] will be nil rather than an empty
// list.  If the file is not found, will return nil
func readNLocations(nLocFN string) [][]byte {
	// open the file; return empty if nothing there
	inNs, err := os.Open(nLocFN)
	if err == nil {
		log.Printf("Reading locations of Ns from %s", nLocFN)
		defer inNs.Close()
		inZ, err := gzip.NewReader(inNs)
		DIE_ON_ERR(err, "Couldn't create gzipper for N locations")
		defer inZ.Close()

		locs := make([][]byte, 0, 10000000)
		ncount := 0

		// for every line in the input file
		scanner := bufio.NewScanner(inZ)
		for scanner.Scan() {
			// split into the list of integers (as strings)
			posns := strings.Split(strings.TrimSpace(scanner.Text()), " ")

			// if there are any Ns in this read
			if len(posns) > 0 && posns[0] != "" {
				// create a new slice to hold them, and convert them to integers
				locs = append(locs, make([]byte, 0))
				for _, v := range posns {
					p, err := strconv.Atoi(v)
					DIE_ON_ERR(err, "Badly formatted N location file!")
					locs[len(locs)-1] = append(locs[len(locs)-1], byte(p))
				}
				ncount += len(posns)
			} else {
				// otherwise, for reads with no Ns, the slice is just nil
				locs = append(locs, nil)
			}
		}
		DIE_ON_ERR(scanner.Err(), "Couldn't finish reading N locations")
		log.Printf("Read locations for %d Ns.", ncount)
		return locs
	} else {
		log.Printf("No file with N locations (%s) was found; ignoring.", nLocFN)
		return nil
	}
}

// dart() finds the interval in the given distribution that contains the given
// target, after transformming the distribution using the given weightOf
// function. This is called by lookup() during decode.
func dart(
	dist [len(ALPHA)]KmerCount,
	target uint32,
) (uint64, uint64, uint64) {
	sum := uint32(0)
	for i := range dist {
		w := uint32(contextWeight(i, dist))
		sum += w
		if target < sum {
			return uint64(sum - w), uint64(sum), uint64(i)
		}
	}
	panic(fmt.Errorf("Couldn't find range for target %d", target))
}

// dartDefault() finds the range in the default distribution that contains
// target
func dartDefault(target uint32) (uint64, uint64, uint64) {
	sum := uint32(0)
	for i, w := range defaultInterval {
		sum += uint32(w)
		if target < sum {
			return uint64(sum - w), uint64(sum), uint64(i)
		}
	}
	panic(fmt.Errorf("Couldn't find range for target %d", target))
}


// lookup() is called by arithc.Decoder to find an interval that contains the
// given value t.
func lookup(km KmerModel, context Kmer, t uint64) (uint64, uint64, uint64) {
    if exists, dist := km.Distribution(context); exists {
		return dart(dist, uint32(t))
	} else {
		return dartDefault(uint32(t))
	}
}

/*
// sumDist() computes the sum of the items in the given distribution after
// first transforming them via the given weightOf function.
func sumDist(d [len(ALPHA)]KmerCount, weightOf WeightXformFcn) (total uint64) {
	for i := range d {
		total += uint64(weightOf(i, d))
	}
	return
}
*/

// contextTotal() returns the total sum of the appropriate distribution: the
// distribution of the given context (if found) or the default distribution
// (otherwise).
func contextTotal(km KmerModel, context Kmer) (total uint64) {
    if exists, dist := km.Distribution(context); exists {
        for i := range dist {
            total += uint64(contextWeight(i, dist))
        }
		return total
	} else {
		return defaultIntervalSum
	}
}

// decodeSingleRead() does the work of decoding a single read.
func decodeSingleRead(
	contextMer Kmer,
	km KmerModel,
	tailLen int,
	decoder *arithc.Decoder,
	out []byte,
) {
	// function called by Decode
	lu := func(t uint64) (uint64, uint64, uint64) {
		a, b, c := lookup(km, contextMer, t)
		return a, b, c
	}

	for i := 0; i < tailLen; i++ {
		// decode next symbol
		symb, err := decoder.Decode(contextTotal(km, contextMer), lu)
		DIE_ON_ERR(err, "Fatal error decoding!")
		b := byte(symb)

		// write it out
		out[i] = baseFromBits(b)

		// update hash counts (throws away the computed interval; just
		// called for side effects.)
		nextInterval(km, contextMer, b, false)

		// update the new context
		contextMer = shiftKmer(contextMer, b)
	}
}

// putbackNs() replaces the letters at the given position by Ns.
func putbackNs(s string, p []byte) string {
	b := []byte(s)
	for _, v := range p {
		b[v] = 'N'
	}
	return string(b)
}

// decodeReads() decodes the file wrapped by the given Decoder, using the
// kmers, counts, and hash table provided. It writes its output to the given
// io.Writer.
func decodeReads(
	kmers []string,
	counts []int,
	isFlipped []bool,
	nLocations [][]byte,
	km KmerModel,
	readLen int,
	out io.Writer,
	decoder *arithc.Decoder,
) {
	log.Printf("Decoding reads...")

	n := 0
	ncount := 0
	buf := bufio.NewWriter(out)

	md5Hash := md5.New()

	patchAndWriteRead := func(head, tail string) {
		// put the head & tail together
		s := fmt.Sprintf("%s%s", head, tail)
		md5Hash.Write([]byte(s))

		// put back the ns if we have them
		if nLocations != nil {
			s = putbackNs(s, nLocations[n])
			ncount += len(nLocations[n])
		}
		// unflip the reads if we have them
		if isFlipped != nil && isFlipped[n] {
			s = reverseComplement(s)
			flipped++
		}
		// write it out
		if outputFastaOption {
			fmt.Fprintf(buf, ">R%d\n", n)
		}
		buf.Write([]byte(s))
		buf.WriteByte('\n')
		return
	}

	// tailBuf is a buffer for read tails returned by decodeSingleRead
	tailLen := readLen - len(kmers[0])
	tailBuf := make([]byte, tailLen)

	log.Printf("Currently have %v Go routines...", runtime.NumGoroutine())

	// for every bucket
	for curBucket, c := range counts {
		contextMer := stringToKmer(kmers[curBucket])

		// if bucket is a uniform bucket, write out |c| copies of the decoded
		// string
		if c < 0 {
			decodeSingleRead(contextMer, km, tailLen, decoder, tailBuf)
			for j := 0; j < AbsInt(c); j++ {
				patchAndWriteRead(kmers[curBucket], string(tailBuf))
				n++
			}
		} else {
			// otherwise, decode a read for each string in the bucket
			for j := 0; j < c; j++ {
				decodeSingleRead(contextMer, km, tailLen, decoder, tailBuf)
				patchAndWriteRead(kmers[curBucket], string(tailBuf))
				n++
			}
		}
	}
	buf.Flush()
	log.Printf("Added back %d Ns to the reads.", ncount)
	log.Printf("MD5 hash of reads = %x", md5Hash.Sum(nil))
	log.Printf("done. Wrote %v reads; %d were flipped", n, flipped)
}

//===================================================================
// Command line and main driver
//===================================================================

// init() is called automatically on program start up. Here, it creates the
// command line parser.
func init() {
	encodeFlags = flag.NewFlagSet("encode", flag.ContinueOnError)
	encodeFlags.StringVar(&refFile, "ref", "", "reference fasta filename")
	encodeFlags.StringVar(&outFile, "out", "", "output filename")
	encodeFlags.StringVar(&readFile, "reads", "", "reads filename")
	encodeFlags.IntVar(&globalK, "k", 16, "length of k")
	encodeFlags.BoolVar(&flipReadsOption, "flip", true, "if true, reverse complement reads as needed")
	encodeFlags.BoolVar(&dupsOption, "dups", true, "if true, record dups specially")
	encodeFlags.BoolVar(&updateReference, "update", true, "if true, update the reference dynamically")
	encodeFlags.IntVar(&maxThreads, "p", 10, "The maximum number of threads to use")

	encodeFlags.BoolVar(&outputFastaOption, "fasta", true, "If false, output seqs, one per line")

	encodeFlags.StringVar(&cpuProfile, "cpuProfile", "", "if nonempty, write pprof profile to given file.")
    encodeFlags.IntVar(&observationWeight, "mul", observationWeight, "debugging: change weight of an observation")
    encodeFlags.BoolVar(&useArrayModel, "bigmem", false, "if true, use more memory for faster speed")
}

// writeGlobalOptions() writes out the global variables that can affect the
// encoding / decoding. Files encoded with one set of options can only be
// decoded using the same set of options.
func writeGlobalOptions() {
	log.Printf("Option: psudeoCount = %d", pseudoCount)
	log.Printf("Option: observationWeight = %d", observationWeight)
	log.Printf("Option: seenThreshold = %d", seenThreshold)
	//log.Printf("Option: MAX_OBSERVATION = %d", MAX_OBSERVATION)
	log.Printf("Option: flipReadsOption = %v", flipReadsOption)
	log.Printf("Option: dupsOption = %v", dupsOption)
	log.Printf("Option: updateReference = %v", updateReference)
}

// main() encodes or decodes a set of reads based on the first command line
// argument (which is either encode or decode).
func main() {
	fmt.Println("kpath  Copyright (C) 2014  Carl Kingsford & Rob Patro\n")

	fmt.Println("This program comes with ABSOLUTELY NO WARRANTY; This is free software, and")
	fmt.Println("you are welcome to redistribute it under certain conditions; see")
	fmt.Println("accompanying LICENSE.txt file.\n")

	log.Println("Starting kpath version 0.6.3 (1-6-15)")
	startTime := time.Now()

	log.Printf("Maximum threads = %v", maxThreads)
	runtime.GOMAXPROCS(maxThreads)

    // "GOMAXPROCS" sets the actual OS threads; our minimum number of "threads" is 2
    // even though both these Go threads may run in the same OS thread.
    if maxThreads < 2 {
        maxThreads = 2
    }

	// parse the command line
	const (
		ENCODE int = 1
		DECODE int = 2
	)
	if len(os.Args) < 2 {
		encodeFlags.PrintDefaults()
		os.Exit(1)
	}
	var mode int
	if os.Args[1][0] == 'e' {
		mode = ENCODE
		log.SetPrefix("kpath (encode): ")
	} else {
		mode = DECODE
		log.SetPrefix("kpath (decode): ")
	}
	encodeFlags.Parse(os.Args[2:])
	if globalK <= 0 || globalK > 16 {
		log.Fatalf("K must be specified as a small positive integer with -k")
	}
	log.Printf("Using kmer size = %d", globalK)
	setShiftKmerMask()

	if refFile == "" {
		log.Fatalf("Must specify gzipped fasta as reference with -ref")
	}

	if readFile == "" {
		log.Println("Must specify input file with -reads")
		log.Fatalln("If decoding, just give basename of encoded files.")
	}

	if outFile == "" {
		log.Println("Must specify output location with -out")
		log.Println("If encoding, omit extension.")
	}

	if cpuProfile != "" {
		log.Printf("Writing CPU profile to %s", cpuProfile)
		cpuF, err := os.Create(cpuProfile)
		DIE_ON_ERR(err, "Couldn't create CPU profile file %s", cpuProfile)
		pprof.StartCPUProfile(cpuF)
		defer pprof.StopCPUProfile()
	}

	writeGlobalOptions()

	if mode == ENCODE {
		/* encode -k -ref -reads=FOO.seq -out=OUT
		   will encode into OUT.{enc,bittree,counts} */
		log.Printf("Reading from %s", readFile)
		log.Printf("Writing to %s, %s, %s",
			outFile+".enc", outFile+".bittree", outFile+".counts")

		// create the output file
		outF, err := os.Create(outFile + ".enc")
		DIE_ON_ERR(err, "Couldn't create output file %s", outFile)
		defer outF.Close()

		//outBuf := bufio.NewWriterSize(outF, 200000000)
		//defer outBuf.Flush()

		writer := bitio.NewWriter(outF)
		defer writer.Close()

		// create encoder
		encoder := arithc.NewEncoder(writer)
		defer encoder.Finish()

		// pre-Process reads
        refSeqs := readReferenceFile(refFile)
        bv := createKmerBitVectorFromReference(globalK, refSeqs)
        tempReadFile, buckets, counts := preprocessWithBuckets(readFile, outFile, bv)
        bv = nil
        runtime.GC()
        debug.FreeOSMemory()

        // build the full model
        km := countKmersInReference(globalK, refSeqs)
        debug.FreeOSMemory()

        // encode the reads
		n := encodeReadsFromTempFile(tempReadFile, buckets, counts, km, encoder)
		log.Printf("Reads Flipped: %v", flipped)
		log.Printf("Encoded %v reads (may be < # of input reads due to duplicates).", n)

	} else {
		/* decode -k -ref -reads=FOO -out=OUT.seq
		   will look for FOO.enc, FOO.bittree, FOO.counts and decode into OUT.seq */

        // count the kmers in the reference
        var km KmerModel
        waitForReference := make(chan struct{})
        go func() {
            refStart := time.Now()
            km = countKmersInReference(globalK, readReferenceFile(refFile))
            log.Printf("Time: Took %v seconds to read reference.",
                time.Now().Sub(refStart).Seconds())
            close(waitForReference)
            return
        }()

		tailsFN := readFile + ".enc"
		headsFN := readFile + ".bittree"
		countsFN := readFile + ".counts"

		log.Printf("Reading from %s, %s, and %s", tailsFN, headsFN, countsFN)

		// read the bucket names
		var kmers []string
		waitForBuckets := make(chan struct{})
		go func() {
			kmers = decodeKmersFromFile(headsFN, globalK)
			sort.Strings(kmers)
			close(waitForBuckets)
			runtime.Goexit()
			return
		}()

		// read the bucket counts
		var counts []int
		var readlen int
		waitForCounts := make(chan struct{})
		go func() {
			counts, readlen = readBucketCounts(countsFN)
			close(waitForCounts)
			runtime.Goexit()
			return
		}()

		// read the flipped bits --- flipped by be 0-length if no file could be
		// found; this indicates that either nothing was flipped or we don't
		// care about orientation
		var flipped []bool
		waitForFlipped := make(chan struct{})
		go func() {
			flipped = readFlipped(readFile + ".flipped")
			close(waitForFlipped)
			runtime.Goexit()
			return
		}()

		// read the NLocations, which might be 0-length if no file could be
		// found; this indicates that the Ns were recorded some other way.
		var NLocations [][]byte
		waitForNLocations := make(chan struct{})
		go func() {
			NLocations = readNLocations(readFile + ".ns")
			close(waitForNLocations)
			runtime.Goexit()
			return
		}()

		// open encoded read file
		encIn, err := os.Open(tailsFN)
		DIE_ON_ERR(err, "Can't open encoded read file %s", tailsFN)
		defer encIn.Close()

		readerBuf := bufio.NewReader(encIn)

		// create a bit reader wrapper around it
		reader := bitio.NewReader(readerBuf)
		defer reader.Close()

		// create a decoder around it
		decoder, err := arithc.NewDecoder(reader)
		DIE_ON_ERR(err, "Couldn't create decoder!")

		// create the output file
		log.Printf("Writing to %s", outFile)
		outF, err := os.Create(outFile)
		DIE_ON_ERR(err, "Couldn't create output file %s", outFile)
		defer outF.Close()

		<-waitForReference
		<-waitForBuckets
		<-waitForCounts
		<-waitForFlipped
		<-waitForNLocations
        <-waitForReference
		log.Printf("Read length = %d", readlen)
		decodeReads(kmers, counts, flipped, NLocations, km, readlen, outF, decoder)
	}
	log.Printf("Default interval used %v times and context used %v times",
		defaultIntervalSum, contextExists)

	endTime := time.Now()
	log.Printf("kpath took %v to run.", endTime.Sub(startTime).Seconds())

	/* UNCOMMENT TO DEBUG GARBAGE COLLECTION WITH GO 1.2
	   var stats debug.GCStats
	   stats.PauseQuantiles = make([]time.Duration, 5)
	   debug.ReadGCStats(&stats)
	   log.Printf("Last GC=%v\nNum GC=%v\nPause for GC=%v\nPauseHistory=%v",
	       stats.LastGC, stats.NumGC, stats.PauseTotal.Seconds(), stats.Pause)
	*/
}
