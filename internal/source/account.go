// Two sources share one account answer when they name the same credential
// and the same mint (design yardr-7d4e 3.5): both minted, the same mint
// input names, and the same effective mint argv. A hand-set mint_command is
// argv including arguments, so the program and its arguments are compared
// literally there; a pack-brought mint is a per-source copy under
// <home>/sources/<name>/, so the two sides' program paths always differ
// and the program is compared by the sha256 of its file's bytes, the
// arguments literally. A program file the yard cannot read breaks sharing,
// as a static credential does: one variable name across two services is
// coincidence, not sameness.
package source

import (
	"slices"
	"sort"
	"strings"
)

// SharesAccount reports whether the two sources take one account answer:
// the same credential names, both minted with the same input names, and
// the same effective mint. Setting is each source's hand-set mint_command,
// split on spaces where set, since the operator's own wins over the
// declaration's; progSHA resolves a relative mint program to the sha256 of
// its bytes, false where the file cannot be read. An absolute program is
// compared literally, as a hand-set one is. A missing mint command on both
// sides matches, as two sources with no mint to run take the same inputs.
func SharesAccount(a, b *Declaration, aSetting, bSetting string, aSHA, bSHA func(prog string) (string, bool)) bool {
	if a == nil || b == nil || a.Credential == nil || b.Credential == nil {
		return false
	}
	if !a.Credential.Minted() || !b.Credential.Minted() {
		return false
	}
	if a.Credential.Env != b.Credential.Env || a.Credential.UpdateEnv != b.Credential.UpdateEnv {
		return false
	}
	aInputs := append([]string{}, a.Credential.Mint...)
	bInputs := append([]string{}, b.Credential.Mint...)
	sort.Strings(aInputs)
	sort.Strings(bInputs)
	if !slices.Equal(aInputs, bInputs) {
		return false
	}
	aProg, aArgs, ok := mintIdentity(a.Credential, aSetting, aSHA)
	if !ok {
		return false
	}
	bProg, bArgs, ok := mintIdentity(b.Credential, bSetting, bSHA)
	if !ok {
		return false
	}
	return aProg == bProg && slices.Equal(aArgs, bArgs)
}

// mintIdentity is the effective mint to compare: the program key with its
// arguments. A hand-set command is argv literally, marked so it never
// matches a declaration's copy of the same path; a relative program is the
// sha256 its resolver reads, and an unreadable one matches nothing; an
// absolute program is the path literally. No command on either side is the
// empty identity.
func mintIdentity(c *Credential, setting string, progSHA func(prog string) (string, bool)) (string, []string, bool) {
	if argv := strings.Fields(setting); len(argv) > 0 {
		return "setting:" + argv[0], append([]string{}, argv[1:]...), true
	}
	if len(c.MintCommand) == 0 {
		return "", []string{}, true
	}
	prog, args := c.MintCommand[0], append([]string{}, c.MintCommand[1:]...)
	if !strings.HasPrefix(prog, "/") {
		if progSHA == nil {
			return "", nil, false
		}
		sha, ok := progSHA(prog)
		if !ok {
			return "", nil, false
		}
		return "sha256:" + sha, args, true
	}
	return prog, args, true
}
