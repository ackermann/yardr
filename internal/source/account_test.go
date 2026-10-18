package source

import (
	"testing"
)

const shareDecl = `name: sharemail
base: https://sharemail.example.com
credential:
  env: SHARE_TOKEN
  mint: [SHARE_ACCOUNT]
  mint_command: [share-mint]
calls:
  ping: { kind: read, request: { method: GET, path: /ping } }
`

func shareParsed(t *testing.T, text string) *Declaration {
	t.Helper()
	d, err := Parse("share.yaml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func shaOf(sha string) func(string) (string, bool) {
	return func(string) (string, bool) { return sha, true }
}

// The same mint inputs, the same program bytes and the same arguments
// share one account.
func TestSharesAccountSameMint(t *testing.T) {
	a := shareParsed(t, shareDecl)
	b := shareParsed(t, shareDecl)
	if !SharesAccount(a, b, "", "", shaOf("abc"), shaOf("abc")) {
		t.Fatal("the same mint does not share")
	}
}

// A static credential never shares: one variable name across two services
// is coincidence, not sameness.
func TestSharesAccountStaticNever(t *testing.T) {
	static := `name: staticone
base: https://static.example.com
credential: { env: SHARE_TOKEN }
calls:
  ping: { kind: read, request: { method: GET, path: /ping } }
`
	a := shareParsed(t, shareDecl)
	b := shareParsed(t, static)
	if SharesAccount(a, b, "", "", shaOf("abc"), shaOf("abc")) {
		t.Fatal("a minted and a static credential share")
	}
	if SharesAccount(b, b, "", "", shaOf("abc"), shaOf("abc")) {
		t.Fatal("two static credentials with one name share")
	}
}

// The same mint names with different bytes are different mints.
func TestSharesAccountDifferentBytesBreak(t *testing.T) {
	a := shareParsed(t, shareDecl)
	b := shareParsed(t, shareDecl)
	if SharesAccount(a, b, "", "", shaOf("abc"), shaOf("def")) {
		t.Fatal("two mints under one name with different bytes share")
	}
}

// A program file the yard cannot read breaks sharing.
func TestSharesAccountUnreadableBreaks(t *testing.T) {
	a := shareParsed(t, shareDecl)
	b := shareParsed(t, shareDecl)
	unreadable := func(string) (string, bool) { return "", false }
	if SharesAccount(a, b, "", "", shaOf("abc"), unreadable) {
		t.Fatal("an unreadable mint program shares")
	}
}

// Different mint inputs are different accounts.
func TestSharesAccountDifferentInputsBreak(t *testing.T) {
	other := `name: sharemail
base: https://sharemail.example.com
credential:
  env: SHARE_TOKEN
  mint: [OTHER_ACCOUNT]
  mint_command: [share-mint]
calls:
  ping: { kind: read, request: { method: GET, path: /ping } }
`
	a := shareParsed(t, shareDecl)
	b := shareParsed(t, other)
	if SharesAccount(a, b, "", "", shaOf("abc"), shaOf("abc")) {
		t.Fatal("different mint inputs share")
	}
}

// A hand-set mint shares only where the full effective argv matches.
func TestSharesAccountHandSetMatches(t *testing.T) {
	a := shareParsed(t, shareDecl)
	b := shareParsed(t, shareDecl)
	if !SharesAccount(a, b, "/usr/bin/my-mint --fast", "/usr/bin/my-mint --fast", shaOf("abc"), shaOf("def")) {
		t.Fatal("the same hand-set mint argv does not share")
	}
}

// Differing hand-set arguments break sharing.
func TestSharesAccountHandSetArgsBreak(t *testing.T) {
	a := shareParsed(t, shareDecl)
	b := shareParsed(t, shareDecl)
	if SharesAccount(a, b, "/usr/bin/my-mint --fast", "/usr/bin/my-mint --slow", shaOf("abc"), shaOf("abc")) {
		t.Fatal("hand-set mints with differing arguments share")
	}
	if SharesAccount(a, b, "/usr/bin/my-mint", "/usr/bin/other-mint", shaOf("abc"), shaOf("abc")) {
		t.Fatal("hand-set mints with differing programs share")
	}
}
