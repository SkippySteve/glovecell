package sessionhub

import (
	"os"

	"golang.org/x/sys/unix"
)

// quietControlEcho stops the terminal from echoing control characters as
// visible ^[ text, so bracketed-paste markers don't show up around a paste.
// It returns the function that restores the terminal.
func quietControlEcho(f *os.File) func() {
	fd := int(f.Fd())
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil || t.Lflag&unix.ECHOCTL == 0 {
		return func() {}
	}
	saved := *t
	t.Lflag &^= unix.ECHOCTL
	if unix.IoctlSetTermios(fd, unix.TCSETS, t) != nil {
		return func() {}
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, &saved) }
}
