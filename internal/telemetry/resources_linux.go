//go:build linux

package telemetry

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type resourceMount struct{ path, kind string }

func filesystemValues(ctx context.Context, path string) ([6]uint64, string) {
	data, reason := resourceRead(ctx, "/proc/self/mountinfo")
	if reason != "" {
		return [6]uint64{}, reason
	}
	mounts, reason := parseResourceMounts(data)
	if reason != "" {
		return [6]uint64{}, reason
	}
	return localFilesystemValues(ctx, path, mounts)
}

// Classify namespace mount metadata before touching the selected path. A local
// child mount does not make traversal through a remote parent safe.
func localFilesystemValues(ctx context.Context, path string, mounts []resourceMount) ([6]uint64, string) {
	var result [6]uint64
	if !filepath.IsAbs(path) || len(path) > 4096 {
		return result, "invalid_path"
	}
	parts := strings.Split(path, "/")
	if len(parts) > 256 {
		return result, "invalid_path"
	}
	for _, part := range parts {
		if part == ".." {
			return result, "invalid_path"
		}
	}
	path = filepath.Clean(path)
	prefixes := []string{"/"}
	current := ""
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part != "" {
			current += "/" + part
			prefixes = append(prefixes, current)
		}
	}
	// Validate every prefix's filesystem before issuing any Lstat: nested remote
	// mountpoints must not even be accessed to discover that they are remote.
	for _, prefix := range prefixes {
		if !localResourceFilesystem(resourceMountType(mounts, prefix)) {
			return result, "unsupported_filesystem"
		}
	}
	for _, prefix := range prefixes {
		if ctx.Err() != nil {
			return result, "cancelled"
		}
		info, err := os.Lstat(prefix)
		if err != nil {
			return result, sourceReason(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return result, "symlink_path"
		}
	}
	if ctx.Err() != nil {
		return result, "cancelled"
	}
	// Pin each directory without following symlinks. Lstat supplies a clear
	// diagnostic; descriptor traversal closes the symlink replacement race.
	flags := syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
	fd, err := syscall.Open("/", flags, 0)
	if err != nil {
		return result, sourceReason(err)
	}
	defer func() { _ = syscall.Close(fd) }()
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		if ctx.Err() != nil {
			return result, "cancelled"
		}
		next, err := syscall.Openat(fd, part, flags, 0)
		if err != nil {
			return result, sourceReason(err)
		}
		_ = syscall.Close(fd)
		fd = next
	}
	var st syscall.Statfs_t
	if err := syscall.Fstatfs(fd, &st); err != nil {
		return result, sourceReason(err)
	}
	if st.Bsize <= 0 || st.Bfree > st.Blocks || st.Bavail > st.Blocks || st.Ffree > st.Files {
		return result, "malformed"
	}
	size := uint64(st.Bsize)
	if st.Blocks > math.MaxUint64/size {
		return result, "malformed"
	}
	return [6]uint64{st.Blocks * size, st.Bfree * size, st.Bavail * size, st.Files, st.Ffree, st.Files - st.Ffree}, ""
}

func localResourceFilesystem(kind string) bool {
	switch kind {
	case "ext2", "ext3", "ext4", "xfs", "btrfs", "tmpfs", "overlay", "zfs":
		return true
	default:
		return false
	}
}

func parseResourceMounts(data string) ([]resourceMount, string) {
	var mounts []resourceMount
	for _, line := range strings.Split(data, "\n") {
		if line == "" {
			continue
		}
		before, after, ok := strings.Cut(line, " - ")
		fields, details := strings.Fields(before), strings.Fields(after)
		if !ok || len(fields) < 6 || len(details) < 3 {
			return nil, "malformed"
		}
		path, ok := unescapeResourceMountPath(fields[4])
		if !ok || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, "malformed"
		}
		mounts = append(mounts, resourceMount{path: path, kind: details[0]})
	}
	if len(mounts) == 0 {
		return nil, "malformed"
	}
	return mounts, ""
}

func unescapeResourceMountPath(path string) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] != '\\' {
			out.WriteByte(path[i])
			continue
		}
		if i+3 >= len(path) {
			return "", false
		}
		switch path[i+1 : i+4] {
		case "040":
			out.WriteByte(' ')
		case "011":
			out.WriteByte('\t')
		case "012":
			out.WriteByte('\n')
		case "134":
			out.WriteByte('\\')
		default:
			return "", false
		}
		i += 3
	}
	return out.String(), true
}

func resourceMountType(mounts []resourceMount, path string) string {
	longest := -1
	kind := ""
	for _, mount := range mounts {
		if mount.path != "/" && path != mount.path && !strings.HasPrefix(path, mount.path+"/") {
			continue
		}
		if len(mount.path) > longest {
			longest, kind = len(mount.path), mount.kind
		} else if len(mount.path) == longest && kind != mount.kind {
			kind = ""
		}
	}
	return kind
}
