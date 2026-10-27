package preview

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// processGroupRSS sums the resident set size, in bytes, of every
// process in the group pgid. It reads ps's table: the one portable
// source on the yard's platforms, and the watcher samples it on its
// own tick (section 7), so its cost is the tick's, not a request's.
func processGroupRSS(pgid int) (int64, error) {
	out, err := exec.Command("ps", "-axo", "pgid=,rss=").Output()
	if err != nil {
		return 0, fmt.Errorf("preview: the process table is not read: %w", err)
	}
	var total int64
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		g, err1 := strconv.Atoi(f[0])
		rss, err2 := strconv.ParseInt(f[1], 10, 64)
		if err1 == nil && err2 == nil && g == pgid {
			total += rss << 10 // ps reports KiB
		}
	}
	return total, sc.Err()
}
