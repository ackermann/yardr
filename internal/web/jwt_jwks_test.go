package web

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"testing"
)

// leastX is the point of curve with the least x that has one: an x of a byte
// or so where the field is 32 to 66 wide, which is what a coordinate looks
// like when its producer dropped the leading zeros.
func leastX(curve elliptic.Curve) (x, y *big.Int) {
	params := curve.Params()
	three := big.NewInt(3)
	for x = big.NewInt(1); ; x = new(big.Int).Add(x, big.NewInt(1)) {
		// y² = x³ - 3x + b, the equation of every NIST curve.
		rhs := new(big.Int).Exp(x, three, params.P)
		rhs.Sub(rhs, new(big.Int).Mul(three, x))
		rhs.Add(rhs, params.B).Mod(rhs, params.P)
		if y = new(big.Int).ModSqrt(rhs, params.P); y != nil {
			return x, y
		}
	}
}

// An EC key of a JWKS is taken or skipped by what its coordinates say, and
// a key skipped is a gateway whose tokens all fail: so which coordinates
// are taken is pinned here, apart from how the key is parsed. A coordinate
// without its leading zero bytes, or with more of them than the field is
// wide, names the same integer and is taken, as it always was; a point off
// the curve, the point at infinity and an integer the field has no room for
// are not.
func TestParseJWKSECCoordinates(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		crv   string
		curve elliptic.Curve
	}{
		{"P-256", elliptic.P256()},
		{"P-384", elliptic.P384()},
		{"P-521", elliptic.P521()},
	} {
		params := c.curve.Params()
		size := (params.BitSize + 7) / 8
		x, y := leastX(c.curve)
		// The key those coordinates name, as the uncompressed point.
		point := make([]byte, 1+2*size)
		point[0] = 4
		x.FillBytes(point[1 : 1+size])
		y.FillBytes(point[1+size:])
		full := func(n *big.Int) []byte { return n.FillBytes(make([]byte, size)) }
		zeros := func(n int, b []byte) []byte { return append(make([]byte, n), b...) }

		offCurve := new(big.Int).Xor(y, big.NewInt(1))
		// 2^BitSize is wider than the field; p itself is not a field element.
		tooWide := new(big.Int).Lsh(big.NewInt(1), uint(params.BitSize))

		for _, tc := range []struct {
			name string
			x, y []byte
			ok   bool
		}{
			{"full width", full(x), full(y), true},
			{"leading zeros dropped", x.Bytes(), y.Bytes(), true},
			{"more zeros than the field is wide", zeros(3, full(x)), zeros(2, full(y)), true},
			{"a point off the curve", full(x), full(offCurve), false},
			{"the point at infinity", make([]byte, size), make([]byte, size), false},
			{"no x", nil, full(y), false},
			{"no y", full(x), nil, false},
			{"an x wider than the field", tooWide.Bytes(), full(y), false},
			{"a y wider than the field", full(x), tooWide.Bytes(), false},
			{"an x that is p", params.P.Bytes(), full(y), false},
		} {
			raw, err := json.Marshal(map[string]any{"keys": []map[string]any{{
				"kty": "EC", "kid": "k", "crv": c.crv,
				"x": base64.RawURLEncoding.EncodeToString(tc.x),
				"y": base64.RawURLEncoding.EncodeToString(tc.y),
			}}})
			if err != nil {
				t.Fatal(err)
			}
			keys, err := parseJWKS(raw)
			if tc.ok != (err == nil) {
				t.Errorf("%s, %s: parseJWKS error = %v, want taken %v", c.crv, tc.name, err, tc.ok)
				continue
			}
			if !tc.ok {
				continue
			}
			got, ok := keys["k"].(*ecdsa.PublicKey)
			if !ok {
				t.Errorf("%s, %s: key = %T, want *ecdsa.PublicKey", c.crv, tc.name, keys["k"])
				continue
			}
			if gotPoint, err := got.Bytes(); err != nil || !bytes.Equal(gotPoint, point) || got.Curve != c.curve {
				t.Errorf("%s, %s: key = %x on %v (%v), want %x on %v", c.crv, tc.name, gotPoint, got.Curve, err, point, c.curve)
			}
		}
	}
}
