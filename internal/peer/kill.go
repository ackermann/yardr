package peer

import "syscall"

func unixKill(pid, sig int) error { return syscall.Kill(pid, syscall.Signal(sig)) }

var errPerm = syscall.EPERM
