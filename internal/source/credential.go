package source

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ReadCredential reads the credential of d as a call of kind needs it: the
// value of the variable d names for that kind (Credential.EnvOf), from
// envFile, the file the operator set for the source, read now; or the token
// the mint's inputs in that file mint, where the file names a mint, with
// mint, the command a mint of the source runs (MintArgv), and no scope.
// state says in words where it is,
// and for a minted token how long it is good, for showing, and never holds
// the value; err says why there is none, with the command that sets it up.
// readEnv is SelectCredential's.
func (d *Declaration) ReadCredential(ctx context.Context, envFile string, mint []string, kind string, readEnv func(path string) (env []string, resolved string, err error)) (value, state string, err error) {
	if d.Credential == nil {
		return "", "none: the file names no credential", nil
	}
	sel, err := d.SelectCredential(envFile, kind, readEnv)
	if err != nil {
		return "", "", err
	}
	return d.resolveSelected(ctx, sel, mint, kind)
}

// ReadUserCredential reads the credential of d for a call made as the user:
// the value of the variable d names for that kind from envFile, the file
// their record names, read now, or the token its mint's inputs mint, with
// mint, the command a mint of the source runs, and no scope. The precedence, the mint and what
// state says are ReadCredential's; err says why there is none, with the
// card that gives it. readEnv is SelectCredential's.
func (d *Declaration) ReadUserCredential(ctx context.Context, user, envFile string, mint []string, kind string, readEnv func(path string) (env []string, resolved string, err error)) (value, state string, err error) {
	if d.Credential == nil {
		return "", "none: the file names no credential", nil
	}
	card := userCredentialCard(d.Name, user)
	if envFile == "" {
		return "", "", fmt.Errorf("%s is not set for %s: %s", strings.Join(d.Credential.MintOrStatic(), " and "), user, card)
	}
	sel, err := d.SelectCredential(envFile, kind, readEnv)
	if err != nil {
		return "", "", fmt.Errorf("%s: %s", err, card)
	}
	return d.resolveSelected(ctx, sel, mint, kind)
}

// resolveSelected is what a selected credential is for a call of kind: the
// static value, or the token the mint's inputs mint.
func (d *Declaration) resolveSelected(ctx context.Context, sel *Selected, mint []string, kind string) (value, state string, err error) {
	if sel.Inputs == nil {
		name := d.Credential.EnvOf(kind)
		return sel.Value, name + " is set in " + sel.File, nil
	}
	token, expires, err := Mint(ctx, d, mint, sel.Inputs, "")
	if err != nil {
		return "", "", err
	}
	return token, mintState(d.Credential.Mint, sel.File) + ", minted, good until " + expires.Local().Format("15:04"), nil
}

// CredentialState says in words where every variable d names is set, the
// one of its updates included, as ReadCredential says it of one; for a
// minted credential, the input names and the command that mints them (mint,
// as MintArgv has it), which is not run to tell. err is the first that is
// not, or a *NoMintCommand where a mint is set up but no command is.
// readEnv is SelectCredential's.
func (d *Declaration) CredentialState(envFile string, mint []string, readEnv func(path string) (env []string, resolved string, err error)) (state string, err error) {
	if d.Credential == nil {
		return "none: the file names no credential", nil
	}
	sel, err := d.SelectCredential(envFile, Read, readEnv)
	if err != nil {
		return "", err
	}
	if sel.Inputs != nil {
		state = mintState(d.Credential.Mint, sel.File)
		if len(mint) == 0 {
			return state, &NoMintCommand{Source: d.Name}
		}
		return state + ", minted with " + strings.Join(mint, " "), nil
	}
	state = d.Credential.Env + " is set in " + sel.File
	if d.Credential.UpdateEnv == "" {
		return state, nil
	}
	update, err := d.SelectCredential(envFile, Update, readEnv)
	if err != nil {
		return "", err
	}
	return state + "; for an update, " + d.Credential.UpdateEnv + " is set in " + update.File, nil
}

// UserCredentialState says in words where every variable d names is set
// in the user's file, as CredentialState says it of the source's file: for
// a minted credential the input names, without the mint command, which is
// the source's and is said on its line. err is the first that is not, with
// the card that gives it. readEnv is SelectCredential's.
// userCredentialCard is the card that gives the user's credential in the
// source: asked of them, or of the operator for them.
func userCredentialCard(src, user string) string {
	return fmt.Sprintf("yardr bead ask <bead> \"<question>\" --secret %s --as %s --context \"<where to get it>\" asks %s for it", src, user, user)
}

func (d *Declaration) UserCredentialState(user, file string, readEnv func(path string) (env []string, resolved string, err error)) (string, error) {
	if d.Credential == nil {
		return "none: the file names no credential", nil
	}
	card := userCredentialCard(d.Name, user)
	if file == "" {
		return "", fmt.Errorf("%s is not set for %s: %s", strings.Join(d.Credential.MintOrStatic(), " and "), user, card)
	}
	sel, err := d.SelectCredential(file, Read, readEnv)
	if err != nil {
		return "", fmt.Errorf("%s: %s", err, card)
	}
	if sel.Inputs != nil {
		return mintState(d.Credential.Mint, sel.File), nil
	}
	state := d.Credential.Env + " is set in " + sel.File
	if d.Credential.UpdateEnv == "" {
		return state, nil
	}
	update, err := d.SelectCredential(file, Update, readEnv)
	if err != nil {
		return "", fmt.Errorf("%s: %s", err, card)
	}
	return state + "; for an update, " + d.Credential.UpdateEnv + " is set in " + update.File, nil
}

// mintState says in words where the mint's inputs are set.
func mintState(names []string, file string) string {
	verb := "are"
	if len(names) == 1 {
		verb = "is"
	}
	return strings.Join(names, " and ") + " " + verb + " set in " + file
}

// Selected is the credential envFile selects for a call of kind, by the
// precedence Select keeps.
type Selected struct {
	// Value is the static value; Inputs the mint's inputs, for Mint to
	// resolve. One of the two is set.
	Value  string
	Inputs map[string]string
	// File is the file as read.
	File string
}

// SelectCredential reads envFile and applies the credential precedence for
// a call of kind. It runs nothing: a mint is for the call. An error says
// why neither way is set, with the file or the command that sets it up. A
// file that names no credential selects nothing, without an error. readEnv
// reads the file: the caller passes the store's reader, since source
// cannot import the store for it (the store imports source).
func (d *Declaration) SelectCredential(envFile, kind string, readEnv func(path string) (env []string, resolved string, err error)) (*Selected, error) {
	if d.Credential == nil {
		return &Selected{}, nil
	}
	if envFile == "" {
		need, sets := d.Credential.EnvOf(kind), strings.Join(d.Credential.Envs(), " and ")
		if d.Credential.Minted() {
			need = strings.Join(d.Credential.Mint, " and ") + ", or " + need
			sets = d.Credential.Names()
		}
		// The command that writes the file first, so that nobody does by
		// hand; the file the operator keeps themselves is named instead.
		// An agent reads this from yardr call, where source key is refused
		// from a pane, so its way on is named too: this names no caller.
		return nil, fmt.Errorf("%s is not set up: yardr source key %s gives %s, or yardr source set %s env_file=<file> names a file the operator keeps themselves; an agent asks from its pane: yardr bead ask <bead> \"<question>\" --secret %s --context \"<where to get it>\"", need, d.Name, sets, d.Name, d.Name)
	}
	env, file, err := readEnv(envFile)
	if err != nil {
		return nil, err
	}
	value, inputs, err := d.Credential.Select(d.Credential.EnvOf(kind), func(name string) string { return Lookup(env, name) })
	if err != nil {
		var missing *Missing
		if errors.As(err, &missing) {
			return nil, fmt.Errorf("env_file %s does not set %s", file, missing.Names)
		}
		return nil, fmt.Errorf("env_file %s: %w", file, err)
	}
	return &Selected{Value: value, Inputs: inputs, File: file}, nil
}

// Lookup is the value of name in env, the KEY=VALUE lines of an env file:
// of a name set twice the last, as in a pane's environment.
func Lookup(env []string, name string) string {
	value := ""
	for _, e := range env {
		if k, v, _ := strings.Cut(e, "="); k == name {
			value = v
		}
	}
	return value
}

// EnvFile is where the yard keeps the credential of the source name when
// the operator named no file for it: <home>/sources/<name>.env, beside the
// store and not in it.
func EnvFile(home, name string) string {
	return filepath.Join(home, "sources", name+".env")
}
