//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris
// +build darwin dragonfly freebsd linux netbsd openbsd solaris

package termenv

import (
	"errors"
	"testing"
)

func TestTermStatusReportSkipsWhenTmuxEnvPresent(t *testing.T) {
	o := Output{
		environ: staticEnv{
			"TERM": "xterm-256color",
			"TMUX": "/tmp/tmux-501/default,1,0",
		},
	}
	if _, err := o.termStatusReport(11); !errors.Is(err, ErrStatusReport) {
		t.Fatalf("termStatusReport with TMUX = %v, want ErrStatusReport", err)
	}
}

type staticEnv map[string]string

func (e staticEnv) Environ() []string {
	out := make([]string, 0, len(e))
	for k, v := range e {
		out = append(out, k+"="+v)
	}
	return out
}

func (e staticEnv) Getenv(key string) string { return e[key] }
