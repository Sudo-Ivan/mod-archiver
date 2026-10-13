// SPDX-License-Identifier: 0BSD

package core

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
)

// xxHash64, implemented from the public domain specification.
// Fast non-cryptographic hash used for content identity and resume
// checks. Integrity verification against the site uses MD5.

const (
	xxP1 uint64 = 11400714785074694791
	xxP2 uint64 = 14029467366897019727
	xxP3 uint64 = 1609587929392839161
	xxP4 uint64 = 9650029242287828579
	xxP5 uint64 = 2870177450012600261
)

func xxRotl(x uint64, r uint) uint64 { return x<<r | x>>(64-r) }

func xxRound(acc, input uint64) uint64 {
	acc += input * xxP2
	acc = xxRotl(acc, 31)
	acc *= xxP1
	return acc
}

func xxMerge(h, v uint64) uint64 {
	v = xxRound(0, v)
	h ^= v
	h = h*xxP1 + xxP4
	return h
}

func xxh64(data []byte, seed uint64) uint64 {
	n := len(data)
	i := 0
	var h uint64
	if n >= 32 {
		v1 := seed + xxP1 + xxP2
		v2 := seed + xxP2
		v3 := seed
		v4 := seed - xxP1
		for ; i+32 <= n; i += 32 {
			v1 = xxRound(v1, binary.LittleEndian.Uint64(data[i:]))
			v2 = xxRound(v2, binary.LittleEndian.Uint64(data[i+8:]))
			v3 = xxRound(v3, binary.LittleEndian.Uint64(data[i+16:]))
			v4 = xxRound(v4, binary.LittleEndian.Uint64(data[i+24:]))
		}
		h = xxRotl(v1, 1) + xxRotl(v2, 7) + xxRotl(v3, 12) + xxRotl(v4, 18)
		h = xxMerge(h, v1)
		h = xxMerge(h, v2)
		h = xxMerge(h, v3)
		h = xxMerge(h, v4)
	} else {
		h = seed + xxP5
	}
	h += uint64(n)
	for ; i+8 <= n; i += 8 {
		k := xxRound(0, binary.LittleEndian.Uint64(data[i:]))
		h ^= k
		h = xxRotl(h, 27)*xxP1 + xxP4
	}
	if i+4 <= n {
		h ^= uint64(binary.LittleEndian.Uint32(data[i:])) * xxP1
		h = xxRotl(h, 23)*xxP2 + xxP3
		i += 4
	}
	for ; i < n; i++ {
		h ^= uint64(data[i]) * xxP5
		h = xxRotl(h, 11) * xxP1
	}
	h ^= h >> 33
	h *= xxP2
	h ^= h >> 29
	h *= xxP3
	h ^= h >> 32
	return h
}

// sampleSpan is how much of the head, middle and tail of a file the
// fast hash reads. At most 3 * sampleSpan bytes are touched, so the
// check is O(1) in file size.
const sampleSpan = 4 << 20

// FastHash returns a content identifier built from the file size and
// xxHash64 of sampled head, middle and tail blocks. Cheap enough to
// verify gigabyte files in a single read of a few megabytes.
func FastHash(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	size := st.Size()

	read := func(off int64, n int) ([]byte, error) {
		buf := make([]byte, n)
		rn, err := f.ReadAt(buf, off)
		if err != nil && err != io.EOF {
			return nil, err
		}
		return buf[:rn], nil
	}

	var head, mid, tail []byte
	if size <= 3*sampleSpan {
		if head, err = read(0, int(size)); err != nil {
			return "", 0, err
		}
	} else {
		if head, err = read(0, sampleSpan); err != nil {
			return "", 0, err
		}
		midOff := size/2 - sampleSpan/2
		if mid, err = read(midOff, sampleSpan); err != nil {
			return "", 0, err
		}
		if tail, err = read(size-sampleSpan, sampleSpan); err != nil {
			return "", 0, err
		}
	}

	h1 := xxh64(head, 0)
	h2 := xxh64(mid, h1)
	h3 := xxh64(tail, h2)
	var final [8 + 3*8]byte
	binary.LittleEndian.PutUint64(final[0:], uint64(size))
	binary.LittleEndian.PutUint64(final[8:], h1)
	binary.LittleEndian.PutUint64(final[16:], h2)
	binary.LittleEndian.PutUint64(final[24:], h3)
	return fmt.Sprintf("xxh64s:%016x", xxh64(final[:], 0)), size, nil
}

// SumFile streams path through h and returns the hex digest.
func SumFile(path string, h hash.Hash) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func MD5File(path string) (string, error)    { return SumFile(path, md5.New()) }
func SHA256File(path string) (string, error) { return SumFile(path, sha256.New()) }
