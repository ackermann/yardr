package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/otel"
	"yardr.dev/yardr/internal/store"
)

// otelJob is a bead's session, with what its trace context must be.
func otelJob(cfg map[string]any) (job dispatch.Job, traceparent, attrs string) {
	job = dispatch.Job{
		Bead:    store.Bead{ID: "yard-13", Stage: "review"},
		Group:   store.Group{Name: "yard-review", Config: cfg},
		Depot:   &store.Depot{Name: "yard", Path: os.TempDir(), Base: "main"},
		Session: store.Session{ID: "s1"},
	}
	return job, otel.Traceparent("yard-13", "s1"),
		"yardr.bead=yard-13,yardr.stage=review,yardr.group=yard-review,yardr.depot=yard"
}

// An agent's tab gets the session's trace context, so a harness that
// exports OpenTelemetry sends it under the bead. A group's env file may add
// resource attributes; it cannot replace the bead's or the TRACEPARENT.
func TestHerdrSessionGetsTraceContext(t *testing.T) {
	t.Setenv(otel.ResourceVar, "")
	wt := t.TempDir()
	gitT(t, wt, "init", "-q", "-b", "main")
	h, calls, _ := envHerdr(t, wt)
	h.YardHome = t.TempDir()
	file := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(file, []byte("CLAUDE_CODE_ENABLE_TELEMETRY=1\nTRACEPARENT=00-bad\nOTEL_RESOURCE_ATTRIBUTES=team=core\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	job, traceparent, attrs := otelJob(map[string]any{"env_file": file})
	if _, err := h.Start(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	env := envOf(apiCall(t, readCalls(t, calls), "tab.create"))
	if env["TRACEPARENT"] != traceparent {
		t.Errorf("TRACEPARENT = %q, want %q", env["TRACEPARENT"], traceparent)
	}
	if want := "team=core," + attrs; env[otel.ResourceVar] != want {
		t.Errorf("%s = %q, want %q", otel.ResourceVar, env[otel.ResourceVar], want)
	}
	if env["CLAUDE_CODE_ENABLE_TELEMETRY"] != "1" {
		t.Errorf("the env file's own entry is gone: %v", env)
	}
}

// An exec command gets the same, after the resource attributes the daemon
// itself was started with.
func TestExecCommandGetsTraceContext(t *testing.T) {
	t.Setenv(otel.ResourceVar, "deployment.environment=test")
	t.Setenv("TRACEPARENT", "00-the-daemons-own")
	dir := t.TempDir()
	out := filepath.Join(dir, "env")
	job, traceparent, attrs := otelJob(map[string]any{
		"cmd": `printf '%s\n%s\n' "$TRACEPARENT" "$OTEL_RESOURCE_ATTRIBUTES" > env.tmp && mv env.tmp env`,
		"dir": dir, "log": filepath.Join(dir, "log")})
	e := &Exec{}
	if _, err := e.Start(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var err error
		if got, err = os.ReadFile(out); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the command wrote nothing")
		}
	}
	if want := traceparent + "\ndeployment.environment=test," + attrs + "\n"; string(got) != want {
		t.Errorf("environment = %q, want %q", got, want)
	}
	if !strings.HasPrefix(traceparent, "00-"+otel.TraceID("yard-13")+"-") {
		t.Errorf("TRACEPARENT %q does not name the bead's trace", traceparent)
	}
}

// A bead with no depot has no yardr.depot attribute.
func TestTraceContextWithoutADepot(t *testing.T) {
	t.Setenv(otel.ResourceVar, "")
	job, _, _ := otelJob(nil)
	job.Depot = nil
	env := Env(job, "x")
	if want := otel.ResourceVar + "=yardr.bead=yard-13,yardr.stage=review,yardr.group=yard-review"; env[len(env)-1] != want {
		t.Errorf("env = %v, want it to end with %q", env, want)
	}
}
