package progress

import (
	"os"
	"strings"
)

// PeakRSS reports the high-water mark from /proc, or "" where that is unavailable.
//
// The build holds a whole-corpus term map in memory, so the honest number has to be
// printed rather than guessed at: a derived index nobody can afford to rebuild is not
// a derived index.
func PeakRSS() string {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "VmHWM:"))
		}
	}
	return ""
}
