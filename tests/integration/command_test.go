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

func TestBuildFailureStopsSuite(t *testing.T) {
	realDocker, err := exec.LookPath("docker")
	need(t, err == nil, "Docker is required to resolve Compose")
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	// Only configuration resolution reaches Docker; builds and runtime commands
	// are intercepted so this regression cannot create containers or images.
	script := `#!/bin/sh
case " $* " in
  *" config --format json "*) exec "$TEST_REAL_DOCKER" "$@" ;;
  *" build "*) printf 'build\n' >> "$TEST_DOCKER_CALLS"; echo 'synthetic build failure' >&2; exit 1 ;;
  *" ps -aq "*|*" network ls -q "*|*" volume ls -q "*) exit 0 ;;
  *) printf 'unexpected command\n' >> "$TEST_DOCKER_CALLS"; exit 2 ;;
esac
`
	need(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700) == nil, "cannot create Docker boundary fixture")
	binary, err := os.Executable()
	need(t, err == nil, "cannot locate test binary")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestNode$", "-test.v", "-test.timeout=25s")
	cmd.Dir = newSuite(t).root
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "TEST_REAL_DOCKER="+realDocker, "TEST_DOCKER_CALLS="+calls)
	output, err := cmd.CombinedOutput()
	need(t, err != nil && ctx.Err() == nil, "build failure must fail promptly")
	need(t, strings.Contains(string(output), "synthetic build failure"), "build diagnostics were lost")
	data, err := os.ReadFile(calls)
	need(t, err == nil && string(data) == "build\n", "failed build was retried or runtime commands were attempted")
	need(t, !strings.Contains(string(output), "=== RUN   TestNode/Lifecycle"), "suite continued after failed preparation")
}
