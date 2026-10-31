package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A setup pack the tests' reader serves: one minted source with a read
// call and an update call, and two presets over them.
const setupDecl = `name: setupmail
base: https://setupmail.example.com/v1
credential:
  env: SETUP_TOKEN
  header: "Authorization: Bearer {credential}"
  mint: [SETUP_ACCOUNT]
  mint_command: [setup-mint]
calls:
  search: { kind: read, request: { method: GET, path: /messages } }
  draft: { kind: update, request: { method: POST, path: /drafts } }
`

const setupMint = "#!/bin/sh\n# mint a token from the account\n"

// The same credential under another source, for a pack whose sources
// share one account choice.
const setupCalDecl = `name: setupcal
base: https://setupcal.example.com/v1
credential:
  env: SETUP_TOKEN
  header: "Authorization: Bearer {credential}"
  mint: [SETUP_ACCOUNT]
  mint_command: [setup-mint]
calls:
  events: { kind: read, request: { method: GET, path: /events } }
`

// A static credential, for a pack whose account is every credential name.
const setupStaticDecl = `name: setupstatic
base: https://setupstatic.example.com/v1
credential: { env: STATIC_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  ping: { kind: read, request: { method: GET, path: /ping } }
  poke: { kind: update, request: { method: POST, path: /poke } }
`

// No credential at all.
const setupOpenDecl = `name: setupopen
base: https://setupopen.example.com/v1
calls:
  ping: { kind: read, request: { method: GET, path: /ping } }
`

// A source that tells people, for what a setup card still owes after it.
const setupPushDecl = `name: setuppush
base: https://setuppush.example.com/1
credential: { env: SETUPPUSH_TOKEN }
calls:
  message: { kind: update, params: { user: { type: string }, text: { type: string } }, request: { method: POST, path: /messages.json } }
notify: { call: message, address: { param: user, env: SETUPPUSH_USER }, text: text }
`

type setupSource struct {
	name  string
	text  string
	files map[string]string
}

// setupPack is a fake embedded pack whose label, sources and presets the
// test moves between asking and approving.
type setupPack struct {
	label   string
	build   string
	kind    string
	sources []setupSource
	presets []AskPreset
}

func (f *setupPack) reading() *PackReading {
	kind := f.kind
	if kind == "" {
		kind = "source"
	}
	r := &PackReading{Name: "setupmail", Kind: kind, Label: f.label, Build: f.build, Presets: f.presets}
	for _, s := range f.sources {
		src := AskSource{Name: s.name, Text: s.text}
		var progs []string
		for prog := range s.files {
			progs = append(progs, prog)
		}
		sort.Strings(progs)
		for _, prog := range progs {
			sum := sha256.Sum256([]byte(s.files[prog]))
			src.Files = append(src.Files, AskFile{Program: prog, Text: s.files[prog], SHA256: hex.EncodeToString(sum[:])})
		}
		r.Sources = append(r.Sources, src)
	}
	return r
}

func setupMailPresets() []AskPreset {
	return []AskPreset{
		{Name: "read", Grants: []AskPresetGrant{{Source: "setupmail", Calls: []string{"read"}}}},
		{Name: "drafts", Grants: []AskPresetGrant{{Source: "setupmail", Calls: []string{"read", "draft"}}}},
	}
}

func setupMailPack() *setupPack {
	return &setupPack{label: "embedded in yardr test1", build: "test1",
		sources: []setupSource{{name: "setupmail", text: setupDecl, files: map[string]string{"setup-mint": setupMint}}},
		presets: setupMailPresets()}
}

func (y *askYard) setSetupPack(f *setupPack) {
	y.Store.SetPackReader(func(ctx context.Context, name string, depot string, mappings Mappings, yard API) (*PackReading, error) {
		if name != "setupmail" {
			return nil, errFakeNoPack(name)
		}
		return f.reading(), nil
	})
}

func setupAsk() Ask {
	return Ask{Question: "Set up setupmail for the yardmaster?", To: "mckean",
		Context:   []string{"the yardmaster reads mail to work beads"},
		Recommend: 1, Why: "reading is the job",
		Setup: &AskSetup{Pack: "setupmail", To: []Grantee{{Crew: "yardmaster"}}}}
}

func (y *askYard) approveSetup(t *testing.T, seq uint64, option int, values map[string]string, actor string) *AskSetupApproved {
	t.Helper()
	done, err := y.ApproveSetup(y.ctx, y.bead, seq, option, values, "", actor, true)
	if err != nil {
		t.Fatal(err)
	}
	return done
}

// A setup ask is stored with the server's own bytes, whatever the writer
// sent for them: the pack, the grantees in the yard's order, the account
// names, the presets with YouDecide last, and the recommendation.
func TestSetupAskStoresServerBytes(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setSetupPack(setupMailPack())
	if err := y.PutGroup(y.ctx, Group{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "mckean"); err != nil {
		t.Fatal(err)
	}
	ask := setupAsk()
	ask.Setup.To = []Grantee{{Crew: "yardmaster"}, {Group: "builders"}, {Crew: "yardmaster"}}
	ask.Setup.Carried = []AskSource{{Name: "setupmail", Text: "name: forged\n"}}
	ask.Setup.Presets = []AskPreset{{Name: "forged", Grants: []AskPresetGrant{{Source: "setupmail", Calls: []string{"read"}}}}}
	ask.Setup.Account = []string{"FORGED"}
	ask.Setup.Does = []string{"a small setup, nothing to read here"}
	ask.Setup.Seen = map[string]string{"pack setupmail": "forged"}
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq)
	setup := got.Ask.Setup
	if setup == nil || setup.Pack != "setupmail" || setup.Label != "embedded in yardr test1" {
		t.Fatalf("the stored ask's setup = %+v", got.Ask.Setup)
	}
	if len(setup.To) != 2 || setup.To[0] != (Grantee{Group: "builders"}) || setup.To[1] != (Grantee{Crew: "yardmaster"}) {
		t.Errorf("the stored grantees = %+v, want groups before crew, once each", setup.To)
	}
	if len(setup.Carried) != 1 || setup.Carried[0].Text != setupDecl {
		t.Fatalf("the stored ask carries %+v, want the server's declaration", setup.Carried)
	}
	if len(setup.Presets) != 2 || setup.Presets[0].Name != "read" || setup.Presets[1].Name != "drafts" {
		t.Fatalf("the stored presets = %+v, want the server's", setup.Presets)
	}
	if len(setup.Account) != 1 || setup.Account[0] != "SETUP_ACCOUNT" {
		t.Errorf("the stored account = %v, want the mint's inputs", setup.Account)
	}
	if setup.AccountFile != filepath.Join(y.home, "sources", "setupmail.env") {
		t.Errorf("the stored account file = %q", setup.AccountFile)
	}
	if len(got.Ask.Options) != 3 || got.Ask.Options[0].Label != "read" ||
		got.Ask.Options[1].Label != "drafts" || got.Ask.Options[2].Label != YouDecide {
		t.Fatalf("the stored options = %+v, want a preset each with You decide last", got.Ask.Options)
	}
	if got.Ask.Recommend != 1 || got.Ask.Why != "reading is the job" {
		t.Errorf("the stored recommendation = %d %q", got.Ask.Recommend, got.Ask.Why)
	}
	joined := strings.Join(setup.Does, "\n")
	if strings.Contains(joined, "nothing to read here") || strings.Contains(joined, "forged") || strings.Contains(joined, "FORGED") {
		t.Errorf("the writer's words stood:\n%s", joined)
	}
	if setup.Seen["pack setupmail"] != "embedded in yardr test1" {
		t.Errorf("the stored build = %q", setup.Seen["pack setupmail"])
	}
	for _, key := range []string{"pack setupmail bytes", "pack setupmail presets", "source setupmail", "account setupmail", "group builders", "crew yardmaster"} {
		if _, ok := setup.Seen[key]; !ok {
			t.Errorf("the stored ask holds nothing of %q: %+v", key, setup.Seen)
		}
	}
}

// The setup card shows the declaration, the mint with its digest, the
// account line and each preset's yard-rendered calls with the grantee,
// each read against the declarations that made it.
func TestSetupCardShowsDeclarationMintAccountAndPresets(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	got := y.asked(t, seq)
	setup := got.Ask.Setup
	sum := sha256.Sum256([]byte(setupMint))
	digest := hex.EncodeToString(sum[:])
	joined := strings.Join(setup.Does, "\n")
	for _, want := range []string{
		"pack setupmail (embedded in yardr test1), of kind source, into the yard: it adds 1 source and copies 1 command file (1 command), and changes nothing else. It makes no hook and sets no credential's file.",
		"source setupmail: its calls go to https://setupmail.example.com/v1.",
		"Its credential is the variable SETUP_TOKEN (the name, never the value), minted from SETUP_ACCOUNT",
		"command file setup-mint, copied to <home>/sources/setupmail/setup-mint: sha256 " + digest + ", mode 0700",
		"one the operator sets wins over it",
		"call search (read): GET /messages",
		"call draft (update): POST /drafts",
		"the account SETUP_ACCOUNT: type it unseen. It is written to " + filepath.Join(y.home, "sources", "setupmail.env") + ", named in the answer, never the value.",
		"grants to crew yardmaster — pick one:",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%s", want, joined)
		}
	}
	if n := len(setup.Does) + len(setup.Presets); n > MaxRunSourceCardLines {
		t.Errorf("the card runs to %d lines, past the 40 it is held to", n)
	}
	// The card settles the account itself, so the credential is no part
	// of what stays owed after it.
	if strings.Contains(joined, "still owed after it") {
		t.Errorf("the card owes after it:\n%s", joined)
	}
	options := got.Ask.Options
	if len(options) != 3 {
		t.Fatalf("the options = %+v", options)
	}
	if want := "may make every read call of the source setupmail (https://setupmail.example.com): today that is search (GET /messages), and it covers any read call the operator later gives the source."; options[0].Consequence != want {
		t.Errorf("the read preset reads:\n%s\nwant:\n%s", options[0].Consequence, want)
	}
	if want := "may make every read call of the source setupmail (https://setupmail.example.com): today that is search (GET /messages), and it covers any read call the operator later gives the source. may make the call draft (POST /drafts) of the source setupmail (https://setupmail.example.com), which changes something there."; options[1].Consequence != want {
		t.Errorf("the drafts preset reads:\n%s\nwant:\n%s", options[1].Consequence, want)
	}
	if want := "the recommended preset, read, is granted"; options[2].Consequence != want {
		t.Errorf("You decide reads %q, want %q", options[2].Consequence, want)
	}
	if setup.TakenBack() != "the operator can take it back: yardr source rm setupmail; yardr source revoke setupmail <what> --crew yardmaster" {
		t.Errorf("the card takes it back with %q", setup.TakenBack())
	}
	lines := strings.Join(got.Lines(), "\n")
	for _, want := range []string{
		"yardmaster says: the yardmaster reads mail to work beads",
		"when you approve, the yard applies the pack, takes the account, and grants the preset you pick, and nothing else:",
		"yardr pack apply setupmail, the account SETUP_ACCOUNT, and the preset's grants to crew yardmaster",
		"the yard says: pack setupmail (embedded in yardr test1)",
		"the yard says: the account SETUP_ACCOUNT: type it unseen.",
		"the yard says: grants to crew yardmaster — pick one:",
		"1. read: may make every read call",
		"2. drafts: may make every read call",
		"3. You decide: the recommended preset, read, is granted",
		"(recommended: reading is the job)",
		"the declaration of source setupmail, whole as carried:",
		"answer with: yardr bead answer " + y.bead + " " + strconv.FormatUint(seq, 10) + " <n> --secret",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("the terminal card lacks %q:\n%s", want, lines)
		}
	}
	setupAskSourceBase(t)
}

// setupAskSourceBase checks a setup's selected region in its card and
// approval, and that names outside the pack's bases are refused.
func setupAskSourceBase(t *testing.T) {
	y, _ := runYard(t)
	decl := strings.Replace(setupDecl, "base: https://setupmail.example.com/v1\n",
		"base: https://setupmail.example.com/v1\nbases:\n  us: https://setupmail.example.com/v1\n  eu: https://eu.setupmail.example.com/v1\n", 1)
	if decl == setupDecl {
		t.Fatal("the test adds no bases")
	}
	f := setupMailPack()
	f.sources[0].text = decl
	y.setSetupPack(f)
	ask := setupAsk()
	ask.Setup.Base = "eu"
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq)
	if got.Ask.Setup.Base != "eu" {
		t.Fatalf("the stored base = %q", got.Ask.Setup.Base)
	}
	joined := strings.Join(got.Ask.Setup.Does, "\n")
	if want := "source setupmail: its calls go to https://eu.setupmail.example.com/v1 (base eu)."; !strings.Contains(joined, want) {
		t.Errorf("the card lacks %q:\n%s", want, joined)
	}
	if strings.Contains(joined, "https://setupmail.example.com/v1.") {
		t.Errorf("the card shows the default base too:\n%s", joined)
	}
	if got.Ask.Options[0].Consequence == "" || !strings.Contains(got.Ask.Options[0].Consequence, "https://eu.setupmail.example.com") ||
		strings.Contains(got.Ask.Options[0].Consequence, "https://setupmail.example.com") {
		t.Errorf("the preset grants do not name the selected base: %s", got.Ask.Options[0].Consequence)
	}
	done := y.approveSetup(t, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "mckean")
	if done.Pack != "setupmail" || done.Preset != "read" || done.Result != CarryPending {
		t.Fatalf("the approval recorded %+v", done)
	}

	ask = setupAsk()
	ask.Setup.Base = "xx"
	if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), "(it has: eu, us)") {
		t.Errorf("a setup with an unknown base was stored or refused otherwise: %v", err)
	}
	y.setSetupPack(setupMailPack())
	ask = setupAsk()
	ask.Setup.Base = "eu"
	if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), "--source-base eu: pack setupmail declares no bases") {
		t.Errorf("a setup with a base of a pack without bases was stored or refused otherwise: %v", err)
	}
}

// A pack past 40 lines stays on the existing per-step flow, with pack
// check named.
func TestSetupAskRefusesLongCard(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	var calls []string
	for i := 0; i < 40; i++ {
		calls = append(calls, "call"+strconv.Itoa(i)+": { kind: read, request: { method: GET, path: /m"+strconv.Itoa(i)+" } }")
	}
	decl := "name: bigmail\nbase: https://bigmail.example.com\ncredential: { env: BIG_TOKEN }\ncalls:\n  " + strings.Join(calls, "\n  ") + "\n"
	f := setupMailPack()
	f.sources = []setupSource{{name: "bigmail", text: decl}}
	f.presets = []AskPreset{{Name: "read", Grants: []AskPresetGrant{{Source: "bigmail", Calls: []string{"read"}}}}}
	y.setSetupPack(f)
	ask := setupAsk()
	ask.Recommend, ask.Why = 1, "reading is the job"
	_, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster")
	if err == nil {
		t.Fatal("a setup past 40 lines was stored")
	}
	for _, want := range []string{"lines", "read whole", "yardr pack check setupmail", "step by step", "a pack apply card", "an account card per source", "a grant card per source and kind"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
}

// A recommendation that names no preset is refused with the terminal
// that prints the presets numbered.
func TestSetupAskRefusesUnknownRecommend(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setSetupPack(setupMailPack())
	ask := setupAsk()
	ask.Recommend, ask.Why = 3, "everything, at once"
	_, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster")
	if err == nil {
		t.Fatal("a setup with --recommend 3 of 2 presets was stored")
	}
	for _, want := range []string{"presets 1..2", "yardr pack show setupmail", "--recommend 3 names none"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
	ask.Recommend, ask.Why = 1, ""
	if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), "--recommend 1 needs --why") {
		t.Errorf("a recommendation without why was stored or refused otherwise: %v", err)
	}
}

// A pack carrying sources under different credentials is refused with
// the per-step flow named: one card carries one account choice.
func TestSetupAskRefusesDifferentCredentials(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	f := setupMailPack()
	f.sources = append(f.sources, setupSource{name: "setupstatic", text: setupStaticDecl})
	f.presets = []AskPreset{{Name: "read", Grants: []AskPresetGrant{
		{Source: "setupmail", Calls: []string{"read"}},
		{Source: "setupstatic", Calls: []string{"read"}},
	}}}
	y.setSetupPack(f)
	_, err := y.AddAsk(y.ctx, y.bead, setupAsk(), "yardmaster")
	if err == nil {
		t.Fatal("a setup of sources under different credentials was stored")
	}
	for _, want := range []string{"different credentials", "setupmail", "setupstatic", "one card carries one account choice", "step by step", "a pack apply card", "an account card per source", "a grant card per source and kind"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
}

// Matching static variable names do not make credentials for two services
// the same account.
func TestSetupAskRefusesSharedStaticCredentialName(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	f := setupMailPack()
	second := strings.ReplaceAll(setupStaticDecl, "setupstatic", "setupstatic2")
	f.sources = []setupSource{
		{name: "setupstatic", text: setupStaticDecl},
		{name: "setupstatic2", text: second},
	}
	f.presets = []AskPreset{{Name: "read", Grants: []AskPresetGrant{
		{Source: "setupstatic", Calls: []string{"read"}},
		{Source: "setupstatic2", Calls: []string{"read"}},
	}}}
	y.setSetupPack(f)
	if _, err := y.AddAsk(y.ctx, y.bead, setupAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "different credentials") {
		t.Errorf("two static credentials with the same name were shared or refused otherwise: %v", err)
	}
}

// A source's configured mint_command wins over the pack's command and can
// make two otherwise identical declarations different credentials.
func TestSetupAskRefusesDifferentEffectiveMints(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	f := setupMailPack()
	f.sources = append(f.sources, setupSource{name: "setupcal", text: setupCalDecl, files: map[string]string{"setup-mint": setupMint}})
	f.presets = []AskPreset{{Name: "read", Grants: []AskPresetGrant{
		{Source: "setupmail", Calls: []string{"read"}},
		{Source: "setupcal", Calls: []string{"read"}},
	}}}
	y.setSetupPack(f)
	if _, err := y.PutSource(y.ctx, "setupcal.yaml", setupCalDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetSource(y.ctx, "setupcal", SourceMintCommand, "/custom/google-mint", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, setupAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "different credentials") {
		t.Errorf("a source's different effective mint was treated as the pack's mint: %v", err)
	}
}

// Sources naming the same credential and the same mint take one card,
// and the account is the mint's inputs, never the minted token.
func TestSetupAskSharesOneCredentialAcrossSources(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	f := setupMailPack()
	f.sources = append(f.sources, setupSource{name: "setupcal", text: setupCalDecl, files: map[string]string{"setup-mint": setupMint}})
	f.presets = []AskPreset{{Name: "read", Grants: []AskPresetGrant{
		{Source: "setupmail", Calls: []string{"read"}},
		{Source: "setupcal", Calls: []string{"read"}},
	}}}
	y.setSetupPack(f)
	seq := y.ask(t, setupAsk(), "yardmaster")
	got := y.asked(t, seq)
	setup := got.Ask.Setup
	if len(setup.Account) != 1 || setup.Account[0] != "SETUP_ACCOUNT" {
		t.Errorf("the account = %v, want the shared mint's inputs", setup.Account)
	}
	if setup.AccountFile != filepath.Join(y.home, "sources", "setupmail.env") {
		t.Errorf("the account file = %q, want the first match's default", setup.AccountFile)
	}
	if want := "may make every read call of the source setupmail (https://setupmail.example.com): today that is search (GET /messages), and it covers any read call the operator later gives the source. may make every read call of the source setupcal (https://setupcal.example.com): today that is events (GET /events), and it covers any read call the operator later gives the source."; got.Ask.Options[0].Consequence != want {
		t.Errorf("the preset reads:\n%s\nwant:\n%s", got.Ask.Options[0].Consequence, want)
	}
}

// The same mint names with different bytes are different credentials:
// the program is compared by the sha256 of its file.
func TestSetupAskRefusesSameMintNamesWithDifferentBytes(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	f := setupMailPack()
	f.sources = append(f.sources, setupSource{name: "setupcal", text: setupCalDecl, files: map[string]string{"setup-mint": "#!/bin/sh\n# another mint\n"}})
	f.presets = []AskPreset{{Name: "read", Grants: []AskPresetGrant{
		{Source: "setupmail", Calls: []string{"read"}},
		{Source: "setupcal", Calls: []string{"read"}},
	}}}
	y.setSetupPack(f)
	_, err := y.AddAsk(y.ctx, y.bead, setupAsk(), "yardmaster")
	if err == nil || !strings.Contains(err.Error(), "different credentials") {
		t.Errorf("a setup of two mints under one name was stored or refused otherwise: %v", err)
	}
}

// Which user a notifying source tells is still owed after a setup card:
// the card settles the account, not the telling.
func TestSetupCardKeepsNotifyOwed(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	f := setupMailPack()
	f.sources = []setupSource{{name: "setuppush", text: setupPushDecl}}
	f.presets = []AskPreset{{Name: "all", Grants: []AskPresetGrant{{Source: "setuppush", Calls: []string{"message"}}}}}
	y.setSetupPack(f)
	ask := setupAsk()
	ask.Recommend, ask.Why = 0, ""
	seq := y.ask(t, ask, "yardmaster")
	joined := strings.Join(y.asked(t, seq).Ask.Setup.Does, "\n")
	if !strings.Contains(joined, "still owed after it: which user it tells, by yardr user set <user> notify=setuppush.") {
		t.Errorf("the card owes nothing after it:\n%s", joined)
	}
	if strings.Contains(joined, "the credential, by a secret ask") {
		t.Errorf("the card owes the credential it settles:\n%s", joined)
	}
}

// A static source's account is every credential name.
func TestSetupAskStaticAccountIsEveryName(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	f := setupMailPack()
	f.sources = []setupSource{{name: "setupstatic", text: setupStaticDecl}}
	f.presets = []AskPreset{{Name: "all", Grants: []AskPresetGrant{{Source: "setupstatic", Calls: []string{"read", "poke"}}}}}
	y.setSetupPack(f)
	ask := setupAsk()
	ask.Recommend, ask.Why = 0, ""
	seq := y.ask(t, ask, "yardmaster")
	got := y.asked(t, seq)
	if len(got.Ask.Setup.Account) != 1 || got.Ask.Setup.Account[0] != "STATIC_TOKEN" {
		t.Errorf("the account = %v, want every credential name", got.Ask.Setup.Account)
	}
	if want := "no preset is recommended, so pick one of the presets above"; got.Ask.Options[1].Consequence != want {
		t.Errorf("You decide reads %q, want %q", got.Ask.Options[1].Consequence, want)
	}
}

// What cannot be carded is refused: a pack of another kind, one with no
// presets, one naming no credential, one the binary does not embed, no
// server reading, no grantee, and options from the writer.
func TestSetupAskRefusesWhatCannotBeCarded(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	depot := setupMailPack()
	depot.kind = "crew"
	y.setSetupPack(depot)
	if _, err := y.AddAsk(y.ctx, y.bead, setupAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "pack of kind source") {
		t.Errorf("a setup of a crew pack was stored or refused otherwise: %v", err)
	}
	none := setupMailPack()
	none.presets = nil
	y.setSetupPack(none)
	if _, err := y.AddAsk(y.ctx, y.bead, setupAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "carries no presets") {
		t.Errorf("a setup with no presets was stored or refused otherwise: %v", err)
	}
	open := setupMailPack()
	open.sources = []setupSource{{name: "setupopen", text: setupOpenDecl}}
	open.presets = []AskPreset{{Name: "read", Grants: []AskPresetGrant{{Source: "setupopen", Calls: []string{"read"}}}}}
	y.setSetupPack(open)
	if _, err := y.AddAsk(y.ctx, y.bead, setupAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "names no credential") {
		t.Errorf("a setup naming no credential was stored or refused otherwise: %v", err)
	}
	y.setSetupPack(setupMailPack())
	ask := setupAsk()
	ask.Setup.Pack = "nosuchpack"
	if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), "embeds no pack") {
		t.Errorf("a setup of an unembedded pack was stored or refused otherwise: %v", err)
	}
	ask = setupAsk()
	ask.Setup.To = nil
	if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), "--crew <name> or --group <name>") {
		t.Errorf("a setup with no grantee was stored or refused otherwise: %v", err)
	}
	ask = setupAsk()
	ask.Options = []AskOption{{Label: "mine", Consequence: "mine"}}
	if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), "takes no --option") {
		t.Errorf("a setup with a writer option was stored or refused otherwise: %v", err)
	}
	if _, err := y.AddAsk(y.ctx, y.bead, setupAsk(), "mckean"); err == nil || !strings.Contains(err.Error(), "needs no approval") {
		t.Errorf("a setup asked by the operator was stored or refused otherwise: %v", err)
	}
	ask = setupAsk()
	ask.To = "ada"
	if _, err := y.AddAsk(y.ctx, y.bead, ask, "yardmaster"); err == nil || !strings.Contains(err.Error(), "the operator's to approve") {
		t.Errorf("a setup to ada was stored or refused otherwise: %v", err)
	}
	plain, _ := runYard(t)
	if _, err := plain.AddAsk(plain.ctx, plain.bead, setupAsk(), "yardmaster"); err == nil || !strings.Contains(err.Error(), "no server has registered") {
		t.Errorf("a setup with no server reading was stored or refused otherwise: %v", err)
	}
}

// An approval records approved-and-not-carried-out with the carrying
// record beside it, and hands the account values to the server's memory,
// never stored: the ask reads as approved and waiting to be carried out.
func TestSetupApprovalRecordsCarryPending(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	// A setup takes the account, so its card is always weighty, and without
	// the client's word that its person saw the weight (weighed) the
	// approval is refused in the words of design yardr-ekmx 3.3: nothing is
	// recorded or kept in memory, and the ask stays open for the approval
	// below.
	words := WeightWords(y.asked(t, seq).Ask.Weight)
	if !strings.HasPrefix(words, "weighty (credential") {
		t.Fatalf("a setup ask weighs %s", words)
	}
	want := "ask " + strconv.FormatUint(seq, 10) + " is " + words + `: approve it where its weight is shown; a client sends "weighed": true once its person has passed its gate`
	if _, err := y.ApproveSetup(y.ctx, y.bead, seq, 2, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "mckean", false); err == nil || err.Error() != want {
		t.Fatalf("approving a setup without the word = %v, want %q", err, want)
	}
	if rec, err := y.CarryOf(y.ctx, y.bead, seq); err != nil || rec != nil {
		t.Fatalf("a refused approval left a carrying record: %+v, %v", rec, err)
	}
	if _, ok := y.takeSetupSecret(carryKey(y.bead, seq)); ok {
		t.Fatal("a refused approval kept the values in memory")
	}
	if got := y.asked(t, seq); got.State != AskOpen {
		t.Fatalf("the ask is %s after the refusal, want it open", got.State)
	}
	done := y.approveSetup(t, seq, 2, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "mckean")
	if done.Pack != "setupmail" || done.Preset != "drafts" || done.Result != CarryPending {
		t.Fatalf("the approval recorded %+v", done)
	}
	if len(done.Given) != 1 || done.Given[0] != "SETUP_ACCOUNT" {
		t.Errorf("the approval gave %v", done.Given)
	}
	rec, err := y.CarryOf(y.ctx, y.bead, seq)
	if err != nil || rec == nil || rec.State != CarryApproved || rec.Actor != "mckean" {
		t.Fatalf("the carrying record = %+v, %v", rec, err)
	}
	if len(rec.Argv) != 2 || rec.Argv[0] != "setup" || rec.Argv[1] != "setupmail" {
		t.Errorf("the carrying record names %v", rec.Argv)
	}
	values, ok := y.takeSetupSecret(carryKey(y.bead, seq))
	if !ok || values["SETUP_ACCOUNT"] != "ops@example.com" {
		t.Errorf("the handed values = %v, %v", values, ok)
	}
	if _, ok := y.takeSetupSecret(carryKey(y.bead, seq)); ok {
		t.Error("the values were taken twice")
	}
	got := y.asked(t, seq)
	lines := strings.Join(got.Lines(), "\n")
	if !strings.Contains(lines, "mckean approved, to be carried out: setup of pack setupmail, preset drafts to crew yardmaster; gave SETUP_ACCOUNT") {
		t.Errorf("the approved card reads:\n%s", lines)
	}
	if pending, err := y.PendingCarry(y.ctx, y.now); err != nil || !pending {
		t.Errorf("the supported setup is not pending in the watcher: %v, %v", pending, err)
	}
	claimed, err := y.ClaimCarry(y.ctx, "boot-one", y.now)
	if err != nil || claimed == nil || claimed.Ask != seq {
		t.Errorf("the watcher did not claim the setup: %+v, %v", claimed, err)
	}
	if ev := lastAnswer(t, y); !strings.Contains(ev.Data["answer"].(string), "approved ask "+strconv.FormatUint(seq, 10)+" on "+y.bead+": setup of pack setupmail, preset drafts to crew yardmaster; gave SETUP_ACCOUNT") {
		t.Errorf("the answer event reads %q", ev.Data["answer"])
	}
	// The card is strong and the approval came through no device, so the
	// answer's event is witnessed terminal.
	if ev := lastAnswer(t, y); Witness(ev) != WitnessTerminal || Via(ev) != "" {
		t.Errorf("the setup approval's event is witnessed %q via %q, want terminal with no device", Witness(ev), Via(ev))
	}

	// A paired device approves the same strong card, on a plain yard and on
	// a hosted one: it takes the word that its person saw the weight as the
	// socket does, and the approval records the carrying out, hands the
	// values over, and names the device in the answer's event without
	// claiming a witness.
	phone := WithVia(y.ctx, "mckean", "phone")
	typed := map[string]string{"SETUP_ACCOUNT": "ops@example.com"}
	for _, c := range []struct{ yard, hosted string }{{"a plain yard", "false"}, {"a hosted yard", "true"}} {
		if err := y.SetSetting(y.ctx, HostedSetting, c.hosted, "mckean"); err != nil {
			t.Fatal(err)
		}
		seq := y.ask(t, setupAsk(), "yardmaster")
		if _, err := y.ApproveSetup(phone, y.bead, seq, 2, typed, "", "mckean", false); err == nil || !strings.Contains(err.Error(), "approve it where its weight is shown") {
			t.Fatalf("%s: approving a setup through a device without the word = %v", c.yard, err)
		}
		if got := y.asked(t, seq); got.State != AskOpen {
			t.Fatalf("%s: the ask is %s after the refusal, want it open", c.yard, got.State)
		}
		done, err := y.ApproveSetup(phone, y.bead, seq, 2, typed, "", "mckean", true)
		if err != nil {
			t.Fatalf("%s: approving a strong setup through a device = %v", c.yard, err)
		}
		if done.Result != CarryPending {
			t.Fatalf("%s: the device's approval recorded %q", c.yard, done.Result)
		}
		if rec, err := y.CarryOf(y.ctx, y.bead, seq); err != nil || rec == nil || rec.State != CarryApproved || rec.Actor != "mckean" {
			t.Fatalf("%s: the carrying record = %+v, %v", c.yard, rec, err)
		}
		if got, ok := y.takeSetupSecret(carryKey(y.bead, seq)); !ok || got["SETUP_ACCOUNT"] != "ops@example.com" {
			t.Fatalf("%s: the handed values = %v, %v", c.yard, got, ok)
		}
		if ev := lastAnswer(t, y); Via(ev) != "phone" || Witness(ev) != "" {
			t.Fatalf("%s: the setup approval's event is via %q witnessed %q, want phone with no witness", c.yard, Via(ev), Witness(ev))
		}
	}
}

// A strong setup is approved under a fresh sign-in as a run card is: the
// approval carries the card out and witnesses the answer's event, and a
// stale sign-in refuses with no carrying record and the ask open.
func TestSetupApprovalUnderAFreshSignIn(t *testing.T) {
	// Sequential: the package's fake clock.
	pinClockFixed(t, askNow)
	y, _ := runYard(t)
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	if !StrongWeight(y.asked(t, seq).Ask.Weight) {
		t.Fatalf("the setup weighs %s, want it strong", WeightWords(y.asked(t, seq).Ask.Weight))
	}
	if err := y.SetSetting(y.ctx, WebJWTPrivilegedSessionMaxAgeSetting, "15m", "mckean"); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"SETUP_ACCOUNT": "ops@example.com"}
	stale := WithFreshWitness(y.ctx, "mckean", y.now.Add(-time.Hour))
	want := "ask " + strconv.FormatUint(seq, 10) + " is strong (code): this approval's fresh sign-in is outside the gateway's window, and a strong card is approved in the hosted web only under one inside it. Nothing ran, and the ask stays open"
	if _, err := y.ApproveSetup(stale, y.bead, seq, 2, values, "", "mckean", true); err == nil || err.Error() != want {
		t.Fatalf("approving a strong setup under a stale sign-in = %v, want %q", err, want)
	}
	if rec, err := y.CarryOf(y.ctx, y.bead, seq); err != nil || rec != nil {
		t.Fatalf("a refused approval left a carrying record: %+v, %v", rec, err)
	}
	if got := y.asked(t, seq); got.State != AskOpen {
		t.Fatalf("the ask is %s after the refusal, want it open", got.State)
	}
	fresh := WithFreshWitness(y.ctx, "mckean", y.now)
	done, err := y.ApproveSetup(fresh, y.bead, seq, 2, values, "", "mckean", true)
	if err != nil {
		t.Fatalf("approving a strong setup under a fresh sign-in = %v", err)
	}
	if done.Result != CarryPending {
		t.Fatalf("the fresh approval recorded %q", done.Result)
	}
	if ev := lastAnswer(t, y); Witness(ev) != WitnessFreshSignIn || Via(ev) != "" {
		t.Fatalf("the setup approval's event is witnessed %q via %q, want a fresh sign-in with no device", Witness(ev), Via(ev))
	}
}

// An approval refuses values that the account file writer cannot save,
// before it records the approval or keeps the values in memory.
func TestSetupApprovalRejectsInvalidAccountValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, value, want string
	}{
		{"empty", "", "the value for SETUP_ACCOUNT is empty"},
		{"line break", "token\nFORGED=value", "the value for SETUP_ACCOUNT has a line break in it"},
		{"space at an end", " token ", "the value for SETUP_ACCOUNT begins or ends with white space"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			y, _ := runYard(t)
			y.setSetupPack(setupMailPack())
			seq := y.ask(t, setupAsk(), "yardmaster")
			_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1,
				map[string]string{"SETUP_ACCOUNT": tc.value}, "", "mckean", true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid account value was approved or refused otherwise: %v", err)
			}
			if tc.value != "" && strings.Contains(err.Error(), tc.value) {
				t.Errorf("the refusal repeats the account value: %v", err)
			}
			if rec, err := y.CarryOf(y.ctx, y.bead, seq); err != nil || rec != nil {
				t.Errorf("an invalid value left a carrying record: %+v, %v", rec, err)
			}
			if got := y.asked(t, seq); got.State != AskOpen {
				t.Errorf("an invalid value left the ask %s, want it open", got.State)
			}
			if _, ok := y.takeSetupSecret(carryKey(y.bead, seq)); ok {
				t.Error("an invalid value was kept in memory")
			}
		})
	}
}

// A static credential with a check call, for what a setup card tries
// before it stores: the base is the test's stand-in.
func setupCheckedDecl(base string) string {
	return "name: setupchecked\nbase: " + base + `
credential: { env: CHECKED_TOKEN, header: "Authorization: Bearer {credential}" }
check: ping
calls:
  ping: { kind: read, request: { method: GET, path: /ping } }
`
}

func setupCheckedPack(base string) *setupPack {
	return &setupPack{label: "embedded in yardr test1", build: "test1",
		sources: []setupSource{{name: "setupchecked", text: setupCheckedDecl(base)}},
		presets: []AskPreset{
			{Name: "read", Grants: []AskPresetGrant{{Source: "setupchecked", Calls: []string{"read"}}}},
		}}
}

// A minted credential with a check call, for what a setup card does
// where the typed account is the mint's inputs, not a token: the base
// is the test's stand-in.
func setupCheckedMintDecl(base string) string {
	return "name: setupchecked\nbase: " + base + `
credential:
  env: CHECKED_TOKEN
  header: "Authorization: Bearer {credential}"
  mint: [CHECKED_ACCOUNT]
  mint_command: [setup-mint]
check: ping
calls:
  ping: { kind: read, request: { method: GET, path: /ping } }
`
}

func setupCheckedMintPack(base string) *setupPack {
	return &setupPack{label: "embedded in yardr test1", build: "test1",
		sources: []setupSource{{name: "setupchecked", text: setupCheckedMintDecl(base), files: map[string]string{"setup-mint": setupMint}}},
		presets: []AskPreset{
			{Name: "read", Grants: []AskPresetGrant{{Source: "setupchecked", Calls: []string{"read"}}}},
		}}
}

// setupCheckedStandin is the service a checked setup card tries its
// typed credential against: it records each request's bearer and answers
// status until the test changes it.
func setupCheckedStandin(t *testing.T, bearers *[]string, status *int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*bearers = append(*bearers, r.Header.Get("Authorization"))
		if *status != http.StatusOK {
			w.WriteHeader(*status)
			_, _ = w.Write([]byte(`{"ok":false}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A setup card whose source names a check tries the typed credential
// with it before anything is stored: a refused check refuses the answer
// with the check's words, stores nothing, and leaves the card open to be
// answered again.
func TestSetupCheckRefusesBeforeStoring(t *testing.T) {
	t.Parallel()
	var bearers []string
	status := http.StatusForbidden
	y, _ := runYard(t)
	y.setSetupPack(setupCheckedPack(setupCheckedStandin(t, &bearers, &status)))
	seq := y.ask(t, setupAsk(), "yardmaster")
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"CHECKED_TOKEN": "wrong-token"}, "", "mckean", true)
	want := "ask " + strconv.FormatUint(seq, 10) + ": the check call ping (GET /ping) was answered 403: the service does not take what CHECKED_TOKEN is set to. Nothing was stored"
	if err == nil || err.Error() != want {
		t.Fatalf("a refused check was approved or refused otherwise: %v, want %q", err, want)
	}
	if strings.Contains(err.Error(), "wrong-token") {
		t.Errorf("the refusal repeats the typed value: %v", err)
	}
	if len(bearers) != 1 || bearers[0] != "Bearer wrong-token" {
		t.Errorf("the check ran with %v, want one call with the typed credential", bearers)
	}
	if rec, err := y.CarryOf(y.ctx, y.bead, seq); err != nil || rec != nil {
		t.Errorf("a refused check left a carrying record: %+v, %v", rec, err)
	}
	if got := y.asked(t, seq); got.State != AskOpen {
		t.Errorf("a refused check left the ask %s, want it open", got.State)
	}
	if _, ok := y.takeSetupSecret(carryKey(y.bead, seq)); ok {
		t.Error("a refused check kept the typed value in memory")
	}
	// The card stays open to be answered again: the same card with a
	// passing check stores.
	status = http.StatusOK
	done := y.approveSetup(t, seq, 1, map[string]string{"CHECKED_TOKEN": "good-token"}, "mckean")
	if len(done.Given) != 1 || done.Given[0] != "CHECKED_TOKEN" {
		t.Errorf("the second answer gave %v", done.Given)
	}
}

// A check the service never answers refuses the same way: with the
// check's words, nothing stored, the card open.
func TestSetupCheckUnansweredRefuses(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing answers now: the closed port refuses the check
	y, _ := runYard(t)
	y.setSetupPack(setupCheckedPack(url))
	seq := y.ask(t, setupAsk(), "yardmaster")
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"CHECKED_TOKEN": "typed-token"}, "", "mckean", true)
	head := "ask " + strconv.FormatUint(seq, 10) + ": the check call ping (GET /ping) failed: source setupchecked, call ping: "
	if err == nil || !strings.HasPrefix(err.Error(), head) || !strings.HasSuffix(err.Error(), ". Nothing was stored") {
		t.Fatalf("an unanswered check was approved or refused otherwise: %v, want it headed %q and ending %q", err, head, ". Nothing was stored")
	}
	if strings.Contains(err.Error(), "typed-token") {
		t.Errorf("the refusal repeats the typed value: %v", err)
	}
	if rec, err := y.CarryOf(y.ctx, y.bead, seq); err != nil || rec != nil {
		t.Errorf("an unanswered check left a carrying record: %+v, %v", rec, err)
	}
	if got := y.asked(t, seq); got.State != AskOpen {
		t.Errorf("an unanswered check left the ask %s, want it open", got.State)
	}
	if _, ok := y.takeSetupSecret(carryKey(y.bead, seq)); ok {
		t.Error("an unanswered check kept the typed value in memory")
	}
}

// The same card with a check that passes stores the secret as today:
// the preset is approved, the carry record stands, and the typed value
// is handed to the server's memory for the account's write.
func TestSetupCheckPassStoresAsToday(t *testing.T) {
	t.Parallel()
	var bearers []string
	status := http.StatusOK
	y, _ := runYard(t)
	y.setSetupPack(setupCheckedPack(setupCheckedStandin(t, &bearers, &status)))
	seq := y.ask(t, setupAsk(), "yardmaster")
	done := y.approveSetup(t, seq, 1, map[string]string{"CHECKED_TOKEN": "good-token"}, "mckean")
	if done.Pack != "setupmail" || done.Preset != "read" || done.Result != CarryPending {
		t.Fatalf("the approval recorded %+v", done)
	}
	if len(done.Given) != 1 || done.Given[0] != "CHECKED_TOKEN" {
		t.Errorf("the approval gave %v", done.Given)
	}
	if len(bearers) != 1 || bearers[0] != "Bearer good-token" {
		t.Errorf("the check ran with %v, want one call with the typed credential", bearers)
	}
	rec, err := y.CarryOf(y.ctx, y.bead, seq)
	if err != nil || rec == nil || rec.State != CarryApproved {
		t.Fatalf("the carrying record = %+v, %v", rec, err)
	}
	values, ok := y.takeSetupSecret(carryKey(y.bead, seq))
	if !ok || values["CHECKED_TOKEN"] != "good-token" {
		t.Errorf("the handed values = %v, %v", values, ok)
	}
}

// A card whose typed account is a mint's inputs is stored as today,
// with no check call: the token is minted only once the pack is in the
// yard. The stand-in refuses every check, so a call would refuse this.
func TestSetupCheckMintedStoredAsToday(t *testing.T) {
	t.Parallel()
	var bearers []string
	status := http.StatusForbidden
	y, _ := runYard(t)
	y.setSetupPack(setupCheckedMintPack(setupCheckedStandin(t, &bearers, &status)))
	seq := y.ask(t, setupAsk(), "yardmaster")
	done := y.approveSetup(t, seq, 1, map[string]string{"CHECKED_ACCOUNT": "ops@example.com"}, "mckean")
	if len(done.Given) != 1 || done.Given[0] != "CHECKED_ACCOUNT" {
		t.Errorf("the approval gave %v", done.Given)
	}
	if len(bearers) != 0 {
		t.Errorf("the card checked a minted account with %v, want no check call", bearers)
	}
	if rec, err := y.CarryOf(y.ctx, y.bead, seq); err != nil || rec == nil || rec.State != CarryApproved {
		t.Fatalf("the carrying record = %+v, %v", rec, err)
	}
}

// A second approval is refused: a setup runs once for one ask.
func TestSetupSecondApprovalRefused(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	y.approveSetup(t, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "mckean")
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "was approved already, by mckean: a setup runs once for one ask") {
		t.Errorf("a second approval was recorded or refused otherwise: %v", err)
	}
}

// Picking You decide grants the asker's recommended preset; with none
// recommended there is nothing to grant.
func TestSetupYouDecideGrantsRecommended(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setSetupPack(setupMailPack())
	ask := setupAsk()
	ask.Recommend, ask.Why = 2, "drafts are the job"
	seq := y.ask(t, ask, "yardmaster")
	done := y.approveSetup(t, seq, 3, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "mckean")
	if done.Preset != "drafts" {
		t.Errorf("You decide granted %q, want the recommended drafts", done.Preset)
	}
	ask = setupAsk()
	ask.Recommend, ask.Why = 0, ""
	seq = y.ask(t, ask, "yardmaster")
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 3, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "You decide grants the recommended preset, and none is recommended: pick a preset, or decline it") {
		t.Errorf("You decide with no recommendation was recorded or refused otherwise: %v", err)
	}
}

// An approval is recorded past a build that moved, the pack unchanged file
// for file: the build is provenance, which the card keeps. It is refused
// when the label without the build moved (here, a reading with no label).
func TestSetupStaleBuild(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	f := setupMailPack()
	y.setSetupPack(f)
	seq := y.ask(t, setupAsk(), "yardmaster")
	f.label, f.build = "embedded in yardr test2", "test2"
	done := y.approveSetup(t, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "mckean")
	if done.Result != CarryPending {
		t.Errorf("an approval past a rebuild recorded %q, want it carried out", done.Result)
	}
	// The approved card still names the build it was carded under: the
	// carrying out holds the reading to it.
	if setup := y.asked(t, seq).Ask.Setup; setup.Label != "embedded in yardr test1" || setup.Seen["pack setupmail build"] != "test1" {
		t.Errorf("the approved card names the label %q and the build %q, want the ones it was carded under", setup.Label, setup.Seen["pack setupmail build"])
	}

	seq = y.ask(t, setupAsk(), "yardmaster")
	f.label, f.build = "", ""
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "was carded under yardr test2") || !strings.Contains(err.Error(), "whose pack differs") {
		t.Errorf("an approval past a reading with no label was recorded or refused otherwise: %v", err)
	}
}

// An approval is refused stale when the pack's bytes moved.
func TestSetupStaleBytes(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	f := setupMailPack()
	y.setSetupPack(f)
	seq := y.ask(t, setupAsk(), "yardmaster")
	f.sources[0].text = strings.Replace(setupDecl, "search: { kind: read", "search: { kind: read", 1) + "# a comment line\n"
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "whose pack differs") {
		t.Errorf("an approval past changed bytes was recorded or refused otherwise: %v", err)
	}
}

// An approval is refused stale when the presets moved, the bytes standing.
func TestSetupStalePresets(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	f := setupMailPack()
	y.setSetupPack(f)
	seq := y.ask(t, setupAsk(), "yardmaster")
	f.presets = append(f.presets, AskPreset{Name: "everything", Grants: []AskPresetGrant{{Source: "setupmail", Calls: []string{"read", "draft"}}}})
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "whose presets differ") {
		t.Errorf("an approval past changed presets was recorded or refused otherwise: %v", err)
	}
}

// An approval is refused stale when a source it would add was added since.
func TestSetupStaleSourceAdded(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	f := setupMailPack()
	f.sources = []setupSource{{name: "setupstatic", text: setupStaticDecl}}
	f.presets = []AskPreset{{Name: "all", Grants: []AskPresetGrant{{Source: "setupstatic", Calls: []string{"read", "poke"}}}}}
	y.setSetupPack(f)
	seq := y.ask(t, setupAsk(), "yardmaster")
	if _, err := y.PutSource(y.ctx, "setupstatic.yaml", setupStaticDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"STATIC_TOKEN": "tok"}, "", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "was not in the yard: it was added since") {
		t.Errorf("an approval past an added source was recorded or refused otherwise: %v", err)
	}
}

// An approval is refused stale when a source's settings move under its
// name while the card is open.
func TestSetupStaleSourceChanged(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setSetupPack(setupMailPack())
	if _, err := y.PutSource(y.ctx, "setupmail.yaml", setupDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq := y.ask(t, setupAsk(), "yardmaster")
	if _, err := y.SetSource(y.ctx, "setupmail", SourceEnvFile, filepath.Join(y.home, "sources", "shared.env"), "mckean"); err != nil {
		t.Fatal(err)
	}
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "its env_file was set since") {
		t.Errorf("an approval past a source setting change was recorded or refused otherwise: %v", err)
	}
}

// An approval is refused stale when a command copy moved under it.
func TestSetupStaleInventory(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	dir := filepath.Join(y.home, "sources", "setupmail")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale-copy"), []byte("# left behind\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "copies stood as carded: the command copy stale-copy was added since") {
		t.Errorf("an approval past a moved copy was recorded or refused otherwise: %v", err)
	}
}

// An approval is refused stale when the account was given meanwhile, or
// ungiven: owed-ness moved.
func TestSetupStaleOwed(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	file := y.asked(t, seq).Ask.Setup.AccountFile
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("SETUP_ACCOUNT=ops@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "was still owed: it was given since") {
		t.Errorf("an approval past a given account was recorded or refused otherwise: %v", err)
	}
	seq = y.ask(t, setupAsk(), "yardmaster")
	if err := os.WriteFile(file, []byte("# nothing set\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "was given: its file no longer holds SETUP_ACCOUNT") {
		t.Errorf("an approval past an ungiven account was recorded or refused otherwise: %v", err)
	}
}

// An approval is refused stale when a grantee moved under its name.
func TestSetupStaleGrantee(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	if _, err := y.PutCrew(y.ctx, CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "mckean"); err != nil {
		t.Fatal(err)
	}
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "crew member yardmaster was another") {
		t.Errorf("an approval past a moved grantee was recorded or refused otherwise: %v", err)
	}
}

// Grants held since are run past, as the grant row runs them: only a
// preset held already refuses the approval.
func TestSetupRunsPastGrantsHeldSince(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	f := setupMailPack()
	f.sources = []setupSource{{name: "setupstatic", text: setupStaticDecl}}
	f.presets = []AskPreset{{Name: "all", Grants: []AskPresetGrant{{Source: "setupstatic", Calls: []string{"read", "poke"}}}}}
	y.setSetupPack(f)
	if _, err := y.PutSource(y.ctx, "setupstatic.yaml", setupStaticDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq := y.ask(t, setupAsk(), "yardmaster")
	if _, err := y.GrantSource(y.ctx, "setupstatic", "read", Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	done := y.approveSetup(t, seq, 1, map[string]string{"STATIC_TOKEN": "tok"}, "mckean")
	if done.Preset != "all" || done.Result != CarryPending {
		t.Errorf("an approval past a grant held since recorded %+v", done)
	}
}

func TestSetupHeldAlreadyRefusesApproval(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	f := setupMailPack()
	f.sources = []setupSource{{name: "setupstatic", text: setupStaticDecl}}
	f.presets = []AskPreset{{Name: "all", Grants: []AskPresetGrant{{Source: "setupstatic", Calls: []string{"read", "poke"}}}}}
	y.setSetupPack(f)
	if _, err := y.PutSource(y.ctx, "setupstatic.yaml", setupStaticDecl, "mckean"); err != nil {
		t.Fatal(err)
	}
	seq := y.ask(t, setupAsk(), "yardmaster")
	if _, err := y.GrantSource(y.ctx, "setupstatic", "read", Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.GrantSource(y.ctx, "setupstatic", "poke", Grantee{Crew: "yardmaster"}, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	_, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"STATIC_TOKEN": "tok"}, "", "mckean", true)
	if err == nil || !strings.Contains(err.Error(), "would grant the preset all of pack setupmail to crew yardmaster, and it is held already") {
		t.Errorf("an approval of a held preset was recorded or refused otherwise: %v", err)
	}
}

// A setup ask takes no pick and no words through the plain answer path:
// Later and Decline go there, with words for a decline, and from its
// addressee or an operator alone.
func TestSetupPlainAnswerShapes(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	if err := y.AddAnswer(y.ctx, y.bead, Answer{Ask: seq, Option: 1}, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "takes a preset pick with the account") {
		t.Errorf("a plain pick was recorded or refused otherwise: %v", err)
	}
	y.refusesStrangers(t, seq)
	y.answer(t, Answer{Ask: seq, Later: true}, "mckean")
	if got := y.asked(t, seq); got.State != AskAside {
		t.Errorf("a put-aside setup reads %s", got.State)
	}
	seq = y.ask(t, setupAsk(), "yardmaster")
	if err := y.AddAnswer(y.ctx, y.bead, Answer{Ask: seq, Decline: true}, "mckean"); err == nil ||
		!strings.Contains(err.Error(), "needs words") {
		t.Errorf("a wordless decline was recorded or refused otherwise: %v", err)
	}
	y.answer(t, Answer{Ask: seq, Decline: true, Words: "not now"}, "mckean")
	if _, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "was declined") {
		t.Errorf("an approval of a declined setup was recorded or refused otherwise: %v", err)
	}
}

// An approval is refused for anyone but the operator, for a pick that
// names no option, for values that are not the account asked for, and
// over a link.
func TestSetupApprovalRefusals(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.setSetupPack(setupMailPack())
	seq := y.ask(t, setupAsk(), "yardmaster")
	if _, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "ada", true); err == nil ||
		!strings.Contains(err.Error(), "approving a command is the operator's") {
		t.Errorf("an approval by ada was recorded or refused otherwise: %v", err)
	}
	if _, err := y.ApproveSetup(y.ctx, y.bead, seq, 9, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "has options 1..3") {
		t.Errorf("an approval past the options was recorded or refused otherwise: %v", err)
	}
	if _, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com", "EXTRA": "x"}, "", "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "given a name it did not ask for") {
		t.Errorf("an approval with a forged name was recorded or refused otherwise: %v", err)
	}
	if _, err := y.ApproveSetup(y.ctx, y.bead, seq, 1, map[string]string{}, "", "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "SETUP_ACCOUNT was not given") {
		t.Errorf("an approval with no value was recorded or refused otherwise: %v", err)
	}
	if _, err := y.ApproveSetup(y.ctx, y.bead+"@airy", seq, 1, map[string]string{"SETUP_ACCOUNT": "ops@example.com"}, "", "mckean", true); err == nil ||
		!strings.Contains(err.Error(), "never over a link") {
		t.Errorf("an approval over a link was recorded or refused otherwise: %v", err)
	}
}
