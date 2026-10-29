package cli

import (
	"context"

	"yardr.dev/yardr/internal/config"
)

// Starting the parts of up that are not running: through their login
// service where one is installed, else in the background with the login
// shell's PATH.

// startPart starts one of the kind for up: through its login service
// where one is installed and serviceFirst, else in the background. What
// the service cannot start is started as a child instead. serviceFirst is
// false where this run has just installed the service and waited on it,
// which is starting through it already.
func (a *app) startPart(ctx context.Context, cfg *config.Config, k *longRunning, args []string, addr string, serviceFirst bool) (int, error) {
	// A setting can change without rewriting a service file. Starting that
	// service would bring up its old address while up waits on the new one.
	if serviceFirst && serviceInstalled(cfg, k.Name) &&
		(addr == "" || serviceAddr(cfg, k.Name) == addr) {
		if pid, err := k.startThroughService(ctx, cfg, addr); err == nil {
			return pid, nil
		}
	}
	if !a.loginPATHMerged {
		a.loginPATHMerged = true
		addLoginPATH(ctx)
	}
	return k.start(a.environ(), cfg, args, addr)
}
