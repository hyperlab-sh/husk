package host

import "testing"

func TestSurfaceLoggerWarnfLogsAtWarnLevel(t *testing.T) {
	var level, msg string
	r := &Root{opts: options{logger: func(l, m string) { level, msg = l, m }}}
	printfLog{r}.Warnf("surfaces: %s refused", "main-stage")
	if level != "warn" || msg != "surfaces: main-stage refused" {
		t.Fatalf("got %q %q", level, msg)
	}
	printfLog{r}.Printf("plain")
	if level != "info" {
		t.Fatalf("Printf level = %q, want info", level)
	}
}
