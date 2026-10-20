package yardkey

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

// seeded is the key whose seed is the 32 bytes first, first+1, ...: the
// keys the edge's vectors were made with.
func seeded(first byte) ed25519.PrivateKey {
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = first + byte(i)
	}
	return ed25519.NewKeyFromSeed(s)
}

// The link's wire format, pinned with the vectors of the edge's own copy
// (yardr-hosted, router/internal/yardkey, TestLinkWireFormat). Ed25519 signs
// deterministically, so a change here to a label, to the order of what is
// signed or to its encoding no longer makes them, and the yard could check
// nothing the edge sends. The nonces are 0..31 and 200..231; the digest is
// the sha256 of "content-type:application/json\nx-hub-signature-256:sha256=abc".
func TestLinkWireFormatIsTheEdges(t *testing.T) {
	const (
		edgeLink = "6ktBtXlaFU54LEdxG9B_Ewi35JLxluFwz7ArlsMn6i-8APEm4tSLxSZzB0LtnvU4ZsuOrig_FPhxJX-Q_twjBA"
		yardLink = "p0uCf0fIyXagHAHzJn4KgoIdEB4ydRWdGZa8RrBt6Oqfdg2vZ6VTBopQ1e1O1C2GqITI6DMVCfdxYCx8VqGHDg"
		session  = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8.yMnKy8zNzs_Q0dLT1NXW19jZ2tvc3d7f4OHi4-Tl5uc"
		digest   = "b84de7906197ab1c607505f34d9c586c96becb4c745a5bb7b708ef9b41541554"
		frameSig = "fnfyQAgn23ykETGPIDELUJPS5j_blquK5lE7Bec8VFvAzGi06JpJV_d920BYW88eAwXK0nXPqE_fHOVWMPC0CA"
	)
	yard, edge := seeded(1), seeded(101)
	yn, en := make([]byte, NonceSize), make([]byte, NonceSize)
	for i := range yn {
		yn[i], en[i] = byte(i), byte(200+i)
	}
	if got := SignLink(edge, yn, en); got != edgeLink {
		t.Errorf("the challenge signs as %s", got)
	}
	if got := SignLink(yard, yn, en); got != yardLink {
		t.Errorf("the hello signs as %s", got)
	}
	if err := VerifyLink(Public(edge), yn, en, edgeLink); err != nil {
		t.Errorf("the edge's pinned challenge: %v", err)
	}
	if got := Session(yn, en); got != session {
		t.Errorf("the session reads %s", got)
	}
	f := Frame{
		Session: session, Seq: 214, ID: "r-81", Method: "POST", Path: "/hooks/github",
		Header:   map[string][]string{"X-Hub-Signature-256": {"sha256=abc"}, "Content-Type": {"application/json"}},
		Body:     []byte(`{"zen":"ok"}`),
		Received: "2026-10-20T09:14:03Z",
	}
	if got := HeaderDigest(f.Header); got != digest {
		t.Errorf("the headers digest as %s", got)
	}
	if got := SignFrame(edge, f); got != frameSig {
		t.Errorf("the frame signs as %s", got)
	}
	if err := VerifyFrame(Public(edge), f, frameSig); err != nil {
		t.Errorf("the edge's pinned frame: %v", err)
	}
}

// A challenge is checked against the key, both nonces and their order: one
// signed by another key, or over another nonce, is refused.
func TestVerifyLinkRefusesAnotherKeyOrNonce(t *testing.T) {
	edge, other := seeded(101), seeded(201)
	yn, en := make([]byte, NonceSize), make([]byte, NonceSize)
	for i := range yn {
		yn[i], en[i] = byte(i), byte(100+i)
	}
	sig := SignLink(edge, yn, en)
	if err := VerifyLink(Public(edge), yn, en, sig); err != nil {
		t.Fatalf("the challenge as signed: %v", err)
	}
	if VerifyLink(Public(other), yn, en, sig) == nil {
		t.Error("verifies for another key")
	}
	if VerifyLink(Public(edge), yn, en, SignLink(other, yn, en)) == nil {
		t.Error("another key's signature verifies as the edge's")
	}
	if VerifyLink(Public(edge), en, yn, sig) == nil {
		t.Error("verifies with the nonces swapped")
	}
	yn2 := append([]byte(nil), yn...)
	yn2[0] ^= 1
	if VerifyLink(Public(edge), yn2, en, sig) == nil {
		t.Error("verifies over another yard nonce")
	}
	if VerifyLink(Public(edge), yn[:16], en, SignLink(edge, yn[:16], en)) == nil {
		t.Error("a short nonce verifies")
	}
}

// A link's signature is no request's, and a request's no link's, though
// both are made with the one key over what could be the same socket.
func TestLinkAndRequestSignaturesDoNotCross(t *testing.T) {
	yard, edge := seeded(1), seeded(101)
	yn, en := make([]byte, NonceSize), make([]byte, NonceSize)
	for i := range yn {
		yn[i], en[i] = byte(i), byte(100+i)
	}
	at := time.Unix(1792486800, 0)
	ts, reqSig := SignRequest(yard, Public(edge), "GET", "/edge/link", at, nil)
	if VerifyLink(Public(yard), yn, en, reqSig) == nil {
		t.Error("a request's signature verifies as a link's")
	}
	linkSig := SignLink(yard, yn, en)
	if VerifyRequest(Public(yard), Public(edge), "GET", "/edge/link", ts, linkSig, nil, at) == nil {
		t.Error("a link's signature verifies as a request's")
	}
	// Nor a frame's as either: its label is its own.
	f := Frame{Session: Session(yn, en), Seq: 1, ID: "r-1", Method: "GET", Path: "/edge/link", Header: map[string][]string{}, Received: "2026-10-20T09:14:03Z"}
	frameSig := SignFrame(yard, f)
	if VerifyLink(Public(yard), yn, en, frameSig) == nil || VerifyRequest(Public(yard), Public(edge), "GET", "/edge/link", ts, frameSig, nil, at) == nil {
		t.Error("a frame's signature verifies as a link's or a request's")
	}
}

// Every field a frame's signature covers is checked: a frame changed in any
// one of them after signing is refused.
func TestVerifyFrameCoversEveryField(t *testing.T) {
	edge := seeded(101)
	base := Frame{Session: "a.b", Seq: 3, ID: "r-1", Method: "POST", Path: "/hooks/x",
		Header: map[string][]string{"X-Hub-Signature-256": {"sha256=abc"}, "X-Github-Delivery": {"d-1"}},
		Body:   []byte("{}"), Received: "2026-10-20T09:14:03Z"}
	sig := SignFrame(edge, base)
	if err := VerifyFrame(Public(edge), base, sig); err != nil {
		t.Fatalf("the frame as signed: %v", err)
	}
	for name, change := range map[string]func(*Frame){
		"session": func(f *Frame) { f.Session = "a.c" },
		"seq":     func(f *Frame) { f.Seq = 4 },
		"id":      func(f *Frame) { f.ID = "r-2" },
		"method":  func(f *Frame) { f.Method = "PUT" },
		"path":    func(f *Frame) { f.Path = "/hooks/y" },
		"signature": func(f *Frame) {
			f.Header = map[string][]string{"X-Hub-Signature-256": {"sha256=abd"}, "X-Github-Delivery": {"d-1"}}
		},
		"delivery": func(f *Frame) {
			f.Header = map[string][]string{"X-Hub-Signature-256": {"sha256=abc"}, "X-Github-Delivery": {"d-2"}}
		},
		"body":     func(f *Frame) { f.Body = []byte("{ }") },
		"received": func(f *Frame) { f.Received = "2026-10-20T09:14:04Z" },
		"queued":   func(f *Frame) { f.Queued = true },
	} {
		f := base
		change(&f)
		if VerifyFrame(Public(edge), f, sig) == nil {
			t.Errorf("a frame whose %s changed after signing verifies", name)
		}
	}
}

// The header digest is the canonical form: names lowercased and sorted, a
// name's values in order, one line each.
func TestHeaderDigestIsCanonical(t *testing.T) {
	sum := func(s string) string {
		b := sha256.Sum256([]byte(s))
		return hex.EncodeToString(b[:])
	}
	h := map[string][]string{
		"X-B":     {"2", "1"},
		"x-a":     {"lower"},
		"X-A":     {"upper"},
		"Content": {"c"},
	}
	want := sum("content:c\nx-a:upper\nx-a:lower\nx-b:2\nx-b:1")
	for i := 0; i < 20; i++ {
		if got := HeaderDigest(h); got != want {
			t.Fatalf("digest %s, want %s", got, want)
		}
	}
	if HeaderDigest(nil) != sum("") {
		t.Error("no headers do not digest as the empty text")
	}
}

// A frame with a line break in a field, or a colon in a header name, would
// sign as some other frame's text, and is refused.
func TestVerifyFrameRefusesWhatCouldSignAsAnother(t *testing.T) {
	edge := seeded(101)
	base := Frame{Session: "s", Seq: 1, ID: "r-1", Method: "POST", Path: "/hooks/x", Header: map[string][]string{}, Received: "2026-10-20T09:14:03Z"}
	for name, f := range map[string]Frame{
		"path":         func() Frame { f := base; f.Path = "/hooks/x\n1"; return f }(),
		"header value": func() Frame { f := base; f.Header = map[string][]string{"A": {"b\nc:d"}}; return f }(),
		"header name":  func() Frame { f := base; f.Header = map[string][]string{"A:b": {"c"}}; return f }(),
	} {
		if VerifyFrame(Public(edge), f, SignFrame(edge, f)) == nil {
			t.Errorf("a %s with a break verifies", name)
		}
	}
}
