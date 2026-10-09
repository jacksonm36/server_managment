package ops

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidSchedule(t *testing.T) {
	for _, s := range []string{"* * * * *", "*/15 * * * *", "0 3 * * 1-5", "5,35 0-6/2 1 jan mon-fri", "@daily", "@reboot", "0 0 1,15 * *"} {
		if !validSchedule(s) {
			t.Errorf("%q rejected", s)
		}
	}
	for _, s := range []string{"", "* * * *", "* * * * * *", "@sometimes", "* * * * ; rm", "* * * * `id`", "*/x * * * *", "0 0 * * mon;reboot"} {
		if validSchedule(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestCronFileEscapesPercent(t *testing.T) {
	dir := t.TempDir()
	j := &CronJob{ID: "abcdef123456", Schedule: "@daily", User: "root", Command: "true", Enabled: true}
	content := renderCronLine(j, filepath.Join(dir, "x.log"))
	line := strings.Split(strings.TrimSpace(content), "\n")
	last := line[len(line)-1]
	// Every % in a cron command must be escaped, or cron cuts the line there.
	n := 0
	for i := range len(last) {
		if last[i] == '%' {
			n++
			if i == 0 || last[i-1] != '\\' {
				t.Fatalf("unescaped %% in %q", last)
			}
		}
	}
	if n == 0 {
		t.Fatalf("expected escaped date format in %q", last)
	}
}
