package main

import (
	"crypto/md5"
	"crypto/rand"
	"fmt"
)

// ByteDance request signing (the "bdms"/mssdk SDK the dola web page loads).
//
// Recovered by loading the live SDK as an oracle: the page exposes
// window.bdms.frontierSign({X-MS-STUB: <query>}) and every request the page
// makes also carries an a_bogus query parameter. The SDK's own source was read
// out of the chunk it ships in, and the algorithm below reproduces the
// deterministic bytes of its output exactly (see bdms_test.go for the vectors
// captured from the live oracle).
//
// Shape of one signature:
//
//	u[0]      = 63 & (++bogusIndex)   per-page counter, 6 bits
//	u[1],u[2] = envcode, big endian   automation-detection flags
//	u[3]      = 14                    constant the SDK calls ubcode
//	u[4],u[5] = md5(md5(""))[14:16]   constant
//	u[6],u[7] = md5(hexDecode(stub))[14:16]   the only input-dependent part
//	u[8]      = random
//	payload   = u[0:9] + xor(u[0:9])  10 bytes
//	out       = [ (1<<6)|(randbit<<4), rc4Key ] + rc4(payload)
//	signature = customBase64(out)     alphabet below, 16 characters
//
// Only two bytes depend on the request, and they are MD5 of a hex-decoded
// string, which is why this fits in the standard library with no dependency.
const (
	// bdmsAlphabet is the SDK's custom base64 alphabet: standard base64 with a
	// permuted character set, so the output is not decodable by encoding/base64.
	bdmsAlphabet = "Dkdpgh4ZKsQB80/Mfvw36XI1R25-WUAlEi7NLboqYTOPuzmFjJnryx9HVGcaStCe="
	// bdmsUBcode is the constant the SDK writes into u[3].
	bdmsUBcode = 14
	// bdmsKWebsocket is the constant forming the top bits of the first output
	// byte (1<<6 == 64, or 80 when the random bit is set).
	bdmsKWebsocket = 1
)

// BDMSSigner produces signatures and keeps the per-page counter the SDK keeps.
// It is not safe for concurrent use; give each goroutine its own signer, or
// guard it, because the counter is part of the signature.
type BDMSSigner struct {
	bogusIndex uint8
	envcode    uint16
}

// NewBDMSSigner returns a signer with the environment flags of a clean client.
//
// The SDK builds envcode from a set of automation probes (hook, incognito,
// webdriver, phantom, node, debugger, dom, switch, location, consistent) and
// ORs them together with a constant 1. A Go HTTP client that is honest about
// itself sets none of them, so envcode is exactly 1: the same value a plain
// browser produces, and better than a headless one.
func NewBDMSSigner() *BDMSSigner {
	return &BDMSSigner{envcode: 1}
}

// XBogus returns the signature for one request stub.
//
// The stub is the request's query string. The SDK also accepts a body-derived
// stub, and replaces an empty stub with 32 zeros, which this mirrors.
func (s *BDMSSigner) XBogus(stub string) (string, error) {
	var u [9]byte

	s.bogusIndex++
	u[0] = 63 & s.bogusIndex
	u[1] = byte(s.envcode >> 8)
	u[2] = byte(s.envcode & 0xFF)
	u[3] = bdmsUBcode

	// Note the asymmetry: the constant path hashes twice, the stub path once,
	// because the SDK wraps the stub in a hex-decode first.
	u[4], u[5] = bdmsConstantPair()

	effective := stub
	if effective == "" {
		effective = "00000000000000000000000000000000"
	}
	u[6], u[7] = bdmsStubPair(effective)

	randomTail, err := randomBytes(3)
	if err != nil {
		return "", fmt.Errorf("bdms: randomness unavailable: %w", err)
	}
	u[8] = randomTail[0]

	payload := make([]byte, 10)
	copy(payload, u[:])
	for _, b := range u {
		payload[9] ^= b
	}

	rc4Key := randomTail[1]
	encrypted := bdmsRC4([]byte{rc4Key}, payload)

	out := make([]byte, 0, 12)
	out = append(out, byte(bdmsKWebsocket<<6)|(randomTail[2]&1)<<4, rc4Key)
	out = append(out, encrypted...)

	return bdmsBase64(out), nil
}

// bdmsConstantPair is the two constant bytes the SDK derives from the empty
// string: md5(md5(""))[14:16]. It is 69,63 on the live SDK.
func bdmsConstantPair() (byte, byte) {
	inner := md5.Sum(nil)
	outer := md5.Sum(inner[:])
	return outer[14], outer[15]
}

// bdmsStubPair is the only input-dependent part: md5(hexDecode(stub))[14:16].
func bdmsStubPair(stub string) (byte, byte) {
	sum := md5.Sum(bdmsHexDecode(stub))
	return sum[14], sum[15]
}

// bdmsHexDecode mirrors the SDK's decoder, including its tolerance: pairs of
// characters, a non-hex character counts as 0 (JavaScript's `undefined << 4`),
// and an odd trailing character is dropped.
func bdmsHexDecode(s string) []byte {
	out := make([]byte, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		out = append(out, bdmsHexVal(s[i])<<4|bdmsHexVal(s[i+1]))
	}
	return out
}

func bdmsHexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 0
	}
}

// bdmsRC4 is RC4 with the SDK's key schedule.
//
// Hand-rolled rather than taken from crypto/rc4: the point is byte-for-byte
// compatibility with the SDK's cipher, and RC4 is deprecated in the standard
// library for being weak, not for being wrong. Nothing here is a security
// decision; the upstream computes exactly this.
func bdmsRC4(key, data []byte) []byte {
	if len(key) == 0 {
		return append([]byte(nil), data...)
	}
	var state [256]byte
	for i := range state {
		state[i] = byte(i)
	}
	var j byte
	for i := 0; i < 256; i++ {
		j += state[i] + key[i%len(key)]
		state[i], state[j] = state[j], state[i]
	}
	out := make([]byte, len(data))
	var a, b byte
	for k, v := range data {
		a++
		b += state[a]
		state[a], state[b] = state[b], state[a]
		out[k] = v ^ state[byte(int(state[a])+int(state[b]))]
	}
	return out
}

// bdmsBase64 encodes with the SDK's alphabet. The tail handling matches the
// SDK: a one-byte remainder emits two characters and then "==", a two-byte
// remainder emits three and then "=".
func bdmsBase64(data []byte) string {
	out := make([]byte, 0, (len(data)+2)/3*4)
	i := 0
	for ; i+3 <= len(data); i += 3 {
		v := uint32(data[i])<<16 | uint32(data[i+1])<<8 | uint32(data[i+2])
		out = append(out,
			bdmsAlphabet[(v>>18)&0x3F],
			bdmsAlphabet[(v>>12)&0x3F],
			bdmsAlphabet[(v>>6)&0x3F],
			bdmsAlphabet[v&0x3F])
	}
	switch len(data) - i {
	case 1:
		v := uint32(data[i]) << 16
		out = append(out,
			bdmsAlphabet[(v>>18)&0x3F],
			bdmsAlphabet[(v>>12)&0x3F],
			'=', '=')
	case 2:
		v := uint32(data[i])<<16 | uint32(data[i+1])<<8
		out = append(out,
			bdmsAlphabet[(v>>18)&0x3F],
			bdmsAlphabet[(v>>12)&0x3F],
			bdmsAlphabet[(v>>6)&0x3F],
			'=')
	}
	return string(out)
}

// bdmsBase64Decode reverses bdmsBase64. It exists for tests and for reading a
// signature back, which is how the algorithm above was pinned down.
func bdmsBase64Decode(s string) []byte {
	var reverse [256]byte
	for i := range reverse {
		reverse[i] = 0xFF
	}
	for i := 0; i < len(bdmsAlphabet); i++ {
		reverse[bdmsAlphabet[i]] = byte(i)
	}
	out := make([]byte, 0, len(s)/4*3)
	var bits uint32
	var nbits uint
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			break
		}
		v := reverse[s[i]]
		if v == 0xFF {
			continue
		}
		bits = bits<<6 | uint32(v)
		nbits += 6
		if nbits >= 8 {
			nbits -= 8
			out = append(out, byte(bits>>nbits))
		}
	}
	return out
}

func randomBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return buf, nil
}
