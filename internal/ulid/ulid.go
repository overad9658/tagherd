// Package ulid makes ULIDs: 48 bits of millisecond time and 80 random bits,
// in Crockford base32, so plan IDs sort by creation time.
package ulid

import (
	"crypto/rand"
	"io"
	"time"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// New returns a ULID for t with randomness from crypto/rand.
func New(t time.Time) string {
	id, err := FromReader(t, rand.Reader)
	if err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return id
}

// FromReader returns a ULID for t with randomness from r.
func FromReader(t time.Time, r io.Reader) (string, error) {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	if _, err := io.ReadFull(r, b[6:]); err != nil {
		return "", err
	}
	// 128 bits as 26 base32 digits: the first digit holds the top 3 bits.
	var out [26]byte
	var acc uint64
	bits := 2 // 26*5 = 130, so two zero bits lead
	j := 0
	for _, x := range b {
		acc = acc<<8 | uint64(x)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[j] = alphabet[(acc>>uint(bits))&31]
			j++
		}
	}
	return string(out[:]), nil
}
