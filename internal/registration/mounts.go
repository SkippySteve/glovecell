package registration

import (
	"bufio"
	"os"
	"strings"
)

// MountedPath reports whether path appears as a mount point in /proc/mounts.
func MountedPath(path string) bool {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[1] == path {
			return true
		}
	}
	return false
}
