package cli

import "testing"

func TestShowNumbers(t *testing.T) {
	for in, want := range map[any]string{
		float64(2366465): "2366465",
		float64(-3):      "-3",
		1.5:              "1.5",
		"x":              "x",
		true:             "true",
	} {
		if got := show(in); got != want {
			t.Errorf("show(%v) = %q, want %q", in, got, want)
		}
	}
	if got := fmtConfig(map[string]any{"handle": map[string]any{"pid": float64(2366465), "log": "/tmp/x"}}); got != "handle={log=/tmp/x pid=2366465}" {
		t.Errorf("nested = %q", got)
	}
}
