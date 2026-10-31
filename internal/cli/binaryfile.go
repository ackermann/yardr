package cli

import (
	"fmt"
	"io"
	"log/slog"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/version"
)

// The yard records which yardr it runs in <home>/binary (config.RecordBinary)
// for the programs that cannot find yardr by PATH. Init, the server and the
// daemon each record the one they run; a record that cannot be written is
// said and never stops them.

// recordBinary records this yardr as the one the yard runs, for init, which
// says on w, its error stream, what went wrong.
func recordBinary(w io.Writer, cfg *config.Config) {
	self, err := executable()
	if err == nil {
		err = cfg.RecordBinary(self)
	}
	if err != nil {
		fmt.Fprintf(w, "warning: cannot record the yardr binary in %s: %v\n", cfg.BinaryFile(), err)
	}
}

// daemonBinary is the daemon's own binary: the one it watches for a build
// installed over it, which it also records as the one the yard runs. It is
// nil when the binary cannot be found, and the daemon then replaces nothing.
func daemonBinary(cfg *config.Config, log *slog.Logger) *version.Binary {
	bin, err := version.Self()
	if err != nil {
		log.Warn("cannot watch the yardr binary; the daemon will not replace itself", "err", err)
		return nil
	}
	if err := cfg.RecordBinary(bin.Path); err != nil {
		log.Warn("cannot record the yardr binary", "binary", bin.Path, "file", cfg.BinaryFile(), "err", err)
	}
	return bin
}
