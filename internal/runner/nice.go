package runner

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
)

// A group's nice setting (store.Group.Nice) is the niceness its sessions run
// at: the higher it is, the more a session gives way to what has a lower one,
// such as the daemon, when every core is wanted. A process inherits the
// niceness of the one that starts it, so the session's own process is all
// that is set, before it starts anything: an exec group's command is run
// under nice (underNice), and the shell of a herdr group's pane is reniced
// before the agent is started in it (niceShell). A process without privilege
// can only raise its niceness, so a session never runs at a lower one than
// the process that starts it: asked for less than that, it is left as it is.

// niceOf is the niceness of the process pid. The Linux system call answers 20
// less than the niceness, so that none of its answers is negative; the
// others answer the niceness.
func niceOf(pid int) (int, error) {
	n, err := syscall.Getpriority(syscall.PRIO_PROCESS, pid)
	if err != nil {
		return 0, err
	}
	if runtime.GOOS == "linux" {
		n = 20 - n
	}
	return n, nil
}

// underNice has cmd run under nice(1) when the niceness want is above this
// process's, which cmd would start with, by the difference: nice adds to the
// niceness it starts with. It is a wrapper and not a renice after the start
// because a shell forks its first commands at once, and those would keep the
// niceness they began with. nice execs the command, so the pid is the same.
// It is given the program by the path os/exec found for it: it would look
// the program up by the PATH of cmd.Env, which an env_file sets.
func underNice(cmd *exec.Cmd, want int) error {
	own, _ := niceOf(os.Getpid()) // a process is always allowed to ask about itself
	if want <= own {
		return nil
	}
	nice, err := exec.LookPath("nice")
	if err != nil {
		return err
	}
	cmd.Args = append([]string{"nice", "-n", strconv.Itoa(want - own), cmd.Path}, cmd.Args[1:]...)
	cmd.Path = nice
	return nil
}

// niceShell sets the niceness of the shell in a pane to want, unless it has
// that or more already, so that the agent started in the pane and all the
// agent starts have it (handle names the shell, as shellOn put it there).
// herdr starts the pane and takes no priority for it, so the shell is
// reniced once it exists; what it ran before then, its own startup, kept the
// niceness it had. A renice that lands in the few milliseconds after a
// process starts is undone by the exec it is still finishing (seen on
// macOS), but yardr is past that: it has spawned herdr, written the store
// and run git since herdr started the shell. A group with no niceness asks
// nothing.
func niceShell(handle map[string]any, want int) error {
	if want <= 0 {
		return nil
	}
	pid, _ := handle["shell_pid"].(int)
	if pid <= 0 {
		return fmt.Errorf("herdr did not report the pane shell, so its niceness cannot be set to %d", want)
	}
	own, err := niceOf(pid)
	if err != nil {
		return fmt.Errorf("niceness of the pane shell %d: %w", pid, err)
	}
	if want <= own {
		return nil
	}
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, pid, want); err != nil {
		return fmt.Errorf("nice %d for the pane shell %d: %w", want, pid, err)
	}
	return nil
}
