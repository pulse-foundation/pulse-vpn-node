//go:build integration

package integration_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise real subprocess termination without starting Docker resources.
func TestCommandFailureDistinguishesTimeout(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf 'expected startup failure\\n' >&2\nif [ \"$1\" = hang ]; then exec /bin/sleep 60; fi\nif [ \"$1\" = signal ]; then kill -KILL $$; fi\nif [ \"$1\" = success ]; then exit 0; fi\nexit 23\n"
	need(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700) == nil, "cannot create subprocess fixture")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := newSuite(t)
	t.Run("nonzero exit", func(t *testing.T) {
		r := s.run(5*time.Second, "exit")
		var exit *exec.ExitError
		need(t, errors.As(r.err, &exit) && exit.ExitCode() == 23, "lost process exit status")
		need(t, r.expectedFailure("expected startup failure"), "ordinary startup failure was not accepted")
		need(t, !r.expectedFailure("different error"), "wrong diagnostic was accepted")
	})
	t.Run("error followed by hang", func(t *testing.T) {
		r := s.run(time.Second, "hang")
		need(t, strings.Contains(r.stderr, "expected startup failure"), "fixture did not emit startup error")
		need(t, errors.Is(r.err, context.DeadlineExceeded), "timeout must remain distinguishable from process failure")
		need(t, !r.expectedFailure("expected startup failure"), "hung startup was accepted as an expected failure")
	})
	for _, mode := range []string{"signal", "success"} {
		t.Run(mode, func(t *testing.T) {
			r := s.run(5*time.Second, mode)
			need(t, strings.Contains(r.stderr, "expected startup failure"), "fixture did not emit startup error")
			need(t, !r.expectedFailure("expected startup failure"), "signal or successful exit was accepted as an expected failure")
		})
	}
}
