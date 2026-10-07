package lexdoc

import (
	"strconv"
	"strings"
	"time"
)

// ParseDate parses the date formats the register uses: ISO first, then the Danish
// "24. oktober 2005" form that appears in prose.
func ParseDate(s string) (time.Time, bool) {
	s = Squeeze(strings.TrimRight(strings.TrimSpace(s), "."))
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, true
	}
	fields := strings.Fields(s)
	if len(fields) != 3 {
		return time.Time{}, false
	}
	atoi := func(s string) int {
		n, _ := strconv.Atoi(strings.TrimSpace(s))
		return n
	}
	d := atoi(strings.TrimRight(fields[0], "."))
	mo := MonthNumber(fields[1])
	y := atoi(fields[2])
	if d == 0 || mo == 0 || y == 0 {
		return time.Time{}, false
	}
	return time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC), true
}
