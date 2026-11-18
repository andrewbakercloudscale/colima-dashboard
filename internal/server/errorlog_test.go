package server

import "testing"

func TestLevelOf(t *testing.T) {
	for line, want := range map[string]string{
		`time="2026" level=error msg="boom"`:                 "error",
		`{"level":"warn","msg":"slow"}`:                      "warn",
		`2026/11/18 10:00:00 [error] 29#29: open() failed`:   "error",
		`E1118 10:00:00.123 1 controller.go:12] sync failed`: "error",
		`W1118 10:00:00.123 1 reflector.go:1] watch ended`:   "warn",
		`Traceback (most recent call last):`:                 "error",
		`panic: runtime error: index out of range`:           "error",
		`stress-ng: info:  [1] dispatching hogs`:             "",
		`GET /health 200`:                                    "",
		`Ran 12 tests: 0 errors, 0 failures`:                 "",
		`WARNING: no swap limit support`:                     "warn",
		`ERROR: relation "users" does not exist`:             "error",
	} {
		if got := levelOf(line); got != want {
			t.Errorf("%q: got %q, want %q", line, got, want)
		}
	}
}
