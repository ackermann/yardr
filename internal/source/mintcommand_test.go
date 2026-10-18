package source

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// mintBrought is minted with the command it mints with beside it: a file
// next to the declaration's, as a build's is.
var mintBrought = strings.Replace(minted, "  mint: [CRM_APP_ID, CRM_APP_KEY]\n",
	"  mint: [CRM_APP_ID, CRM_APP_KEY]\n  mint_command: [crm/mint, --quiet]\n", 1)

// A declaration may bring the command its credential is minted with: a
// relative program, which is one of its commands and so is copied,
// hashed and shown as each is. An absolute one, or one without a mint
// to run for, is refused (yardr-66qd).
func TestParseCredentialMintCommand(t *testing.T) {
	d, err := Parse("crm.yaml", []byte(mintBrought))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Credential.MintCommand; !reflect.DeepEqual(got, []string{"crm/mint", "--quiet"}) {
		t.Fatalf("mint_command = %q", got)
	}
	cmds := d.Commands()
	if len(cmds) == 0 || cmds[0].Key != MintCommandKey || cmds[0].Argv[0] != "crm/mint" {
		t.Fatalf("commands = %+v: the mint command is one of them", cmds)
	}
	if !d.HasCommandFiles() || d.programs()[0] != "crm/mint" {
		t.Fatalf("programs = %q: the mint's file is copied as every command's", d.programs())
	}
	for name, tc := range map[string]struct{ text, want string }{
		"absolute": {strings.Replace(mintBrought, "[crm/mint, --quiet]", "[/usr/local/bin/crm-mint]", 1),
			`credential.mint_command: the program "/usr/local/bin/crm-mint" is absolute`},
		"no mint": {strings.Replace(crm, "  env: CRM_TOKEN                                  # the name, never the value\n",
			"  env: CRM_TOKEN\n  mint_command: [crm/mint]\n", 1),
			"credential.mint_command: names a command to mint with, and the credential names no mint"},
		"leaves the dir": {strings.Replace(mintBrought, "[crm/mint, --quiet]", "[../mint]", 1),
			"has a '.' or '..' segment"},
		"no list": {strings.Replace(mintBrought, "[crm/mint, --quiet]", "crm/mint", 1),
			"credential.mint_command: want a list of strings"},
	} {
		_, err := Parse("f", []byte(tc.text))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// The command a mint runs is the operator's setting where it is set,
// whatever the declaration brings; else the copy of the one the
// declaration brings, with its arguments; else none.
func TestMintArgv(t *testing.T) {
	home := t.TempDir()
	brings, err := Parse("crm.yaml", []byte(mintBrought))
	if err != nil {
		t.Fatal(err)
	}
	none, err := Parse("crm.yaml", []byte(minted))
	if err != nil {
		t.Fatal(err)
	}
	copyOf := filepath.Join(home, "sources", "crm", "crm", "mint")
	for _, tc := range []struct {
		name    string
		d       *Declaration
		setting string
		want    []string
	}{
		{"the declaration's", brings, "", []string{copyOf, "--quiet"}},
		{"the operator's wins", brings, "/opt/bin/my-mint --as jo", []string{"/opt/bin/my-mint", "--as", "jo"}},
		{"the operator's alone", none, "/opt/bin/my-mint", []string{"/opt/bin/my-mint"}},
		{"neither", none, "", nil},
		{"spaces are no setting", brings, "   ", []string{copyOf, "--quiet"}},
	} {
		if got := tc.d.MintArgv(home, tc.setting); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: MintArgv = %q, want %q", tc.name, got, tc.want)
		}
	}
	var bare Declaration
	if got := bare.MintArgv(home, ""); got != nil {
		t.Errorf("no credential: MintArgv = %q", got)
	}
}
