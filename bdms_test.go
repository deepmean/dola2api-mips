package main

import (
	"strings"
	"testing"
)

// The vectors below were captured from the live SDK: the page exposes
// window.bdms.frontierSign({X-MS-STUB: <stub>}) and its output was decoded back
// into the internal 9-byte header (see spikes/RESULT.md). They are the whole
// reason this implementation can be trusted without shipping the SDK.
//
// Expected header for every signature:
//
//	u[1],u[2] = 0,1        (envcode 1: a clean client, no automation flags)
//	u[3]      = 14         (ubcode)
//	u[4],u[5] = 69,63      (constant pair)
//	u[6],u[7] = per-stub
var stubVectors = []struct {
	stub  string
	u6    byte
	u7    byte
	label string
}{
	{"", 24, 165, "empty stub is replaced by 32 zeros"},
	{"a", 66, 126, "odd length: the trailing char is dropped"},
	{"abc", 239, 247, ""},
	{"aid=495671&channel=g&device_platform=web&version_code=20800", 2, 36, "a real query string"},
}

func TestBDMSConstantPair(t *testing.T) {
	u4, u5 := bdmsConstantPair()
	if u4 != 69 || u5 != 63 {
		t.Errorf("constant pair = %d,%d, want 69,63 (from the live SDK)", u4, u5)
	}
}

func TestBDMSStubPairVectors(t *testing.T) {
	for _, v := range stubVectors {
		effective := v.stub
		if effective == "" {
			effective = "00000000000000000000000000000000"
		}
		got6, got7 := bdmsStubPair(effective)
		if got6 != v.u6 || got7 != v.u7 {
			t.Errorf("stub %q -> %d,%d, want %d,%d %s", v.stub, got6, got7, v.u6, v.u7, v.label)
		}
	}
}

// bdmsHexDecode must be tolerant exactly like the SDK's decoder: a non-hex
// character counts as 0 and an odd trailing character is dropped. Getting this
// wrong is invisible for hex stubs and wrong for every real query string.
func TestBDMSHexDecodeMatchesJSSemantics(t *testing.T) {
	cases := []struct {
		in   string
		want []byte
	}{
		{"", nil},
		{"a", nil},                   // odd length: dropped
		{"ab", []byte{0xAB}},         // lowercase hex
		{"AB", []byte{0xAB}},         // uppercase hex
		{"aZ", []byte{0xA0}},         // non-hex counts as 0
		{"&4", []byte{0x04}},         // '&' -> 0, '4' -> 4
		{"0011", []byte{0x00, 0x11}}, // pairs
	}
	for _, c := range cases {
		got := bdmsHexDecode(c.in)
		if len(got) != len(c.want) {
			t.Errorf("bdmsHexDecode(%q) length = %d, want %d", c.in, len(got), len(c.want))
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("bdmsHexDecode(%q)[%d] = %#x, want %#x", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// A signature must be 16 characters of the custom alphabet, and must decode
// back to a valid header. This is the end-to-end property: encode, decode,
// verify the checksum and every deterministic byte.
func TestBDMSXBogusRoundTrip(t *testing.T) {
	signer := NewBDMSSigner()

	for _, v := range stubVectors {
		sig, err := signer.XBogus(v.stub)
		if err != nil {
			t.Fatalf("XBogus(%q): %v", v.stub, err)
		}
		if len(sig) != 16 {
			t.Fatalf("signature %q has length %d, want 16", sig, len(sig))
		}
		for _, c := range sig {
			if !strings.ContainsRune(bdmsAlphabet, c) {
				t.Fatalf("signature %q contains %q, which is outside the SDK alphabet", sig, c)
			}
		}

		u, ok := dissectXBogus(sig)
		if !ok {
			t.Fatalf("signature %q did not decode", sig)
		}
		if u[1] != 0 || u[2] != 1 {
			t.Errorf("envcode bytes = %d,%d, want 0,1", u[1], u[2])
		}
		if u[3] != bdmsUBcode {
			t.Errorf("ubcode = %d, want %d", u[3], bdmsUBcode)
		}
		if u[4] != 69 || u[5] != 63 {
			t.Errorf("constant pair = %d,%d, want 69,63", u[4], u[5])
		}
		if u[6] != v.u6 || u[7] != v.u7 {
			t.Errorf("stub pair = %d,%d, want %d,%d for stub %q", u[6], u[7], v.u6, v.u7, v.stub)
		}
	}
}

// The first output byte encodes kWebsocket in its top two bits and one random
// bit at 0x10, so it is always 64 or 80. Anything else means the framing drifted.
func TestBDMSFirstByteShape(t *testing.T) {
	signer := NewBDMSSigner()
	seen := map[byte]bool{}
	for i := 0; i < 64; i++ {
		sig, err := signer.XBogus("probe")
		if err != nil {
			t.Fatalf("XBogus: %v", err)
		}
		raw := bdmsBase64Decode(sig)
		if len(raw) != 12 {
			t.Fatalf("raw length = %d, want 12", len(raw))
		}
		if raw[0] != 64 && raw[0] != 80 {
			t.Fatalf("first byte = %d, want 64 or 80", raw[0])
		}
		seen[raw[0]] = true
	}
	if len(seen) != 2 {
		t.Errorf("first byte took %d distinct values in 64 draws, want both 64 and 80 (the random bit)", len(seen))
	}
}

// The counter is part of the signature, so it must advance and wrap at 63.
func TestBDMSCounterAdvancesAndWraps(t *testing.T) {
	signer := NewBDMSSigner()
	var previous byte
	for i := 1; i <= 5; i++ {
		sig, err := signer.XBogus("x")
		if err != nil {
			t.Fatalf("XBogus: %v", err)
		}
		u, ok := dissectXBogus(sig)
		if !ok {
			t.Fatal("decode failed")
		}
		if u[0] != byte(i) {
			t.Errorf("call %d: counter byte = %d, want %d", i, u[0], i)
		}
		previous = u[0]
	}

	// Push past 63 and confirm it wraps within the 6-bit mask.
	for i := 0; i < 60; i++ {
		if _, err := signer.XBogus("x"); err != nil {
			t.Fatalf("XBogus: %v", err)
		}
	}
	sig, err := signer.XBogus("x")
	if err != nil {
		t.Fatalf("XBogus: %v", err)
	}
	u, _ := dissectXBogus(sig)
	if u[0] > 63 {
		t.Errorf("counter byte = %d, want it masked to 6 bits", u[0])
	}
	if u[0] == previous {
		t.Errorf("counter did not advance past %d", previous)
	}
}

// Two signatures for the same stub must differ: the SDK mixes in fresh
// randomness every call, and a fixed signature would be replayable.
func TestBDMSIsNotDeterministic(t *testing.T) {
	signer := NewBDMSSigner()
	first, err := signer.XBogus("same")
	if err != nil {
		t.Fatalf("XBogus: %v", err)
	}
	second, err := signer.XBogus("same")
	if err != nil {
		t.Fatalf("XBogus: %v", err)
	}
	if first == second {
		t.Error("two signatures for the same stub are identical; randomness is not being mixed in")
	}
}

func TestBDMSBase64TailHandling(t *testing.T) {
	cases := []struct {
		in   []byte
		want int
	}{
		{nil, 0},
		{[]byte{1}, 4},          // 2 chars + "=="
		{[]byte{1, 2}, 4},       // 3 chars + "="
		{[]byte{1, 2, 3}, 4},    // exact triple
		{[]byte{1, 2, 3, 4}, 8}, // triple + 1-byte remainder
	}
	for _, c := range cases {
		got := bdmsBase64(c.in)
		if len(got) != c.want {
			t.Errorf("bdmsBase64(%v) length = %d, want %d (%q)", c.in, len(got), c.want, got)
		}
		if decoded := bdmsBase64Decode(got); len(decoded) != len(c.in) {
			t.Errorf("round trip of %v gave %v", c.in, decoded)
		}
	}
}

// dissectXBogus reverses one signature: strip the two framing bytes, RC4 the
// rest with the key in byte 1, and check the XOR checksum. It is the same tool
// that pinned the algorithm down against the live SDK, kept here so the test
// asserts on the internal header rather than only on the string.
func dissectXBogus(sig string) ([9]byte, bool) {
	var u [9]byte
	raw := bdmsBase64Decode(sig)
	if len(raw) != 12 {
		return u, false
	}
	payload := bdmsRC4([]byte{raw[1]}, raw[2:])
	if len(payload) != 10 {
		return u, false
	}
	var sum byte
	for i := 0; i < 9; i++ {
		u[i] = payload[i]
		sum ^= payload[i]
	}
	if sum != payload[9] {
		return u, false
	}
	return u, true
}
