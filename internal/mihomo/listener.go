package mihomo

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ownsListener checks Linux procfs inside the node container, without host mounts
// or extra capabilities. TCP reachability alone can succeed against a foreign process.
func ownsListener(ctx context.Context, pid, port int) error {
	root := filepath.Join("/proc", strconv.Itoa(pid))
	entries, err := os.ReadDir(filepath.Join(root, "fd"))
	if err != nil {
		return errors.New("cannot inspect core listener ownership (Linux procfs required)")
	}
	sockets := make(map[string]bool)
	for _, entry := range entries {
		link, err := os.Readlink(filepath.Join(root, "fd", entry.Name()))
		if err == nil && strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
			sockets[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	wantPort := fmt.Sprintf(":%04X", port)
	for _, family := range []string{"tcp", "tcp6"} {
		f, err := os.Open(filepath.Join(root, "net", family))
		if err != nil {
			continue // IPv6 may be disabled; absence never counts as ownership.
		}
		scanner := bufio.NewScanner(f)
		found := false
		for scanner.Scan() {
			if ctx.Err() != nil {
				_ = f.Close()
				return ctx.Err()
			}
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 10 && fields[3] == "0A" && strings.HasSuffix(fields[1], wantPort) && sockets[fields[9]] {
				found = true
				break
			}
		}
		_ = f.Close()
		if found {
			return nil
		}
	}
	return errors.New("VLESS listener is not owned by core")
}
