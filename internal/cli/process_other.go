//go:build !aix && !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !solaris

package cli

import (
	"io"
	"os/exec"
)

func prepareChildProcess(_ *exec.Cmd, _ io.Reader) (func(), bool) {
	return func() {}, false
}
