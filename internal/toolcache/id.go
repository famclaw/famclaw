package toolcache

import (
	"crypto/rand"
	"sync"
	"time"
)

// Crockford base32: 32 chars, omitting I, L, O, U (commonly confused).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	idMu       sync.Mutex
	lastMillis uint64
	lastRand   [10]byte
)

// NewID returns a 26-char ULID-like identifier. 48 bits of millisecond
// timestamp (10 chars) + 80 bits of randomness (16 chars). Lexicographically
// time-orderable. Within the same millisecond, the random component is
// incremented monotonically (uint80++ with carry) so IDs minted in rapid
// succession still sort correctly.
func NewID() string {
	idMu.Lock()
	defer idMu.Unlock()

	now := uint64(time.Now().UnixMilli())
	var randBytes [10]byte

	if now == lastMillis {
		// Same ms — bump the prior randomness by 1 (treated as uint80,
		// big-endian) to preserve lex ordering of rapid mints.
		copy(randBytes[:], lastRand[:])
		for i := 9; i >= 0; i-- {
			randBytes[i]++
			if randBytes[i] != 0 {
				break
			}
		}
	} else {
		// New ms — fresh randomness.
		if _, err := rand.Read(randBytes[:]); err != nil {
			// crypto/rand failure is exceptional; preserve the err but
			// don't drag in fmt just for the panic message.
			panic(err)
		}
		lastMillis = now
	}
	copy(lastRand[:], randBytes[:])

	var out [26]byte
	// Encode 48-bit timestamp MSB-first as 10 base32 chars (5 bits each).
	for i := 0; i < 10; i++ {
		out[i] = crockford[(now>>(45-5*i))&0x1F]
	}
	// Encode 80-bit randomness as 16 base32 chars.
	encodeBase32(out[10:], randBytes[:])

	return string(out[:])
}

// encodeBase32 writes 16 base32 chars from 10 input bytes (80 bits → 16×5).
// Standard ULID layout.
func encodeBase32(out []byte, in []byte) {
	out[0] = crockford[(in[0]&0xF8)>>3]
	out[1] = crockford[((in[0]&0x07)<<2)|((in[1]&0xC0)>>6)]
	out[2] = crockford[(in[1]&0x3E)>>1]
	out[3] = crockford[((in[1]&0x01)<<4)|((in[2]&0xF0)>>4)]
	out[4] = crockford[((in[2]&0x0F)<<1)|((in[3]&0x80)>>7)]
	out[5] = crockford[(in[3]&0x7C)>>2]
	out[6] = crockford[((in[3]&0x03)<<3)|((in[4]&0xE0)>>5)]
	out[7] = crockford[in[4]&0x1F]
	out[8] = crockford[(in[5]&0xF8)>>3]
	out[9] = crockford[((in[5]&0x07)<<2)|((in[6]&0xC0)>>6)]
	out[10] = crockford[(in[6]&0x3E)>>1]
	out[11] = crockford[((in[6]&0x01)<<4)|((in[7]&0xF0)>>4)]
	out[12] = crockford[((in[7]&0x0F)<<1)|((in[8]&0x80)>>7)]
	out[13] = crockford[(in[8]&0x7C)>>2]
	out[14] = crockford[((in[8]&0x03)<<3)|((in[9]&0xE0)>>5)]
	out[15] = crockford[in[9]&0x1F]
}
