//go:build !linux

package sessionhub

import "os"

func quietControlEcho(*os.File) func() { return func() {} }
