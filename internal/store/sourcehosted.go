package store

import (
	"context"
	"time"

	"yardr.dev/yardr/internal/source"
)

// HostedMint mints one hosted-Google token in-process: the token for
// account at the edge addr, for the source the hosted mint names, and
// when it expires. The server is one (internal/server, through
// internal/edgelink, with the scope check before the token is held);
// the store knows only the shape, since it cannot call the edge package
// (which reads the store's edge setting) and the yard's key is the
// owning process's to hold. It is source.HostedFetch by another name.
type HostedMint = func(ctx context.Context, addr, source, account string) (token string, expires time.Time, err error)

// SetHostedMint tells the store how its owning process mints a
// hosted-Google token in-process, as SetPackReader tells it how that
// process reads its packs. A store no server owns has none, and a hosted
// call on it fails naming the in-process mint rather than running the
// mint command the source's setting names: that command refuses every
// caller now, and spawning it was the door this closes.
func (s *Store) SetHostedMint(f HostedMint) { s.hostedMint = f }

// isHostedMint reports whether argv names the hosted mint with the edge
// and source it mints through (source.HostedGoogleMint): the shape a
// call mints in-process for. The agent refusal (hostedGoogleMint) is
// wider on purpose: it keeps an agent out of a mint spelled any way,
// while only a mint that names its edge and source can be made at all.
func isHostedMint(argv []string) bool {
	_, _, ok := source.HostedGoogleMint(argv)
	return ok
}

// hostedCredential is the credential of a hosted-Google call: the static
// value where the caller's file sets one, as every credential's
// precedence has it, else the token the hosted mint mints in-process
// through the registered HostedMint, held in the mint cache and never
// printed. argv is the source's effective mint, which names the hosted
// mint; asFile is the file of the user the call names, for one made as
// them, and src's own file serves a plain call. With no minter
// registered, MintHosted refuses naming the in-process mint: the mint
// command is never run.
func (s *Store) hostedCredential(ctx context.Context, d *source.Declaration, src *Source, asFile string, req SourceCall, call *source.Call, argv []string) (string, error) {
	if d.Credential == nil {
		return "", nil
	}
	envFile := src.EnvFile
	if req.As != "" {
		envFile = asFile
	}
	sel, err := d.SelectCredential(envFile, call.Kind, ReadEnvFileEntries)
	if err != nil {
		return "", err
	}
	if sel.Inputs == nil {
		return sel.Value, nil
	}
	token, _, err := source.MintHosted(ctx, d, argv, sel.Inputs, "", s.hostedMint)
	return token, err
}

// hostedGoogleMint reports whether the effective mint argv of a source
// runs the hosted mint: yardr edge mint-google <url> <source>, as the
// operator's mint_command setting names it (MintArgv). It is told by its
// "edge mint-google" words wherever they stand, not by the whole line, so
// a mint the operator spells another way (yardr by another path or name,
// through env, naming another google source) still keeps an agent caller
// out: the refusal fails closed.
func hostedGoogleMint(mint []string) bool {
	for i := 0; i+1 < len(mint); i++ {
		if mint[i] == "edge" && mint[i+1] == "mint-google" {
			return true
		}
	}
	return false
}
