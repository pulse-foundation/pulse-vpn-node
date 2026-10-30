//go:build linux

package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemMountClassification(t *testing.T) {
	mounts, reason := parseResourceMounts("1 0 8:0 / / rw - ext4 /dev/root rw\n2 1 0:2 / /data rw - nfs remote:/volume rw\n3 2 0:3 / /data/cache rw - tmpfs tmpfs rw\n4 1 0:4 / /escaped\\040mount rw - fuse.sshfs remote rw\n5 1 0:5 / /tab\\011mount rw - xfs /dev/local rw\n")
	if reason != "" {
		t.Fatal(reason)
	}
	for _, test := range []struct{ path, kind string }{{"/", "ext4"}, {"/data/file", "nfs"}, {"/database/file", "ext4"}, {"/data/cache/file", "tmpfs"}, {"/escaped mount/file", "fuse.sshfs"}, {"/tab\tmount/file", "xfs"}} {
		if got := resourceMountType(mounts, test.path); got != test.kind {
			t.Errorf("mount %q: got %q, want %q", test.path, got, test.kind)
		}
	}
}
func TestFilesystemRejectsUnsafeMountBeforeAccess(t *testing.T) {
	for _, kind := range []string{"nfs", "nfs4", "fuse", "fuse.sshfs", "autofs", "cifs", "unknown"} {
		mounts := []resourceMount{{path: "/", kind: "ext4"}, {path: "/must-not-be-accessed", kind: kind}}
		_, reason := localFilesystemValues(context.Background(), "/must-not-be-accessed/no-such-path", mounts)
		if reason != "unsupported_filesystem" {
			t.Errorf("%s: %s", kind, reason)
		}
	}
	// A local mount below a remote parent cannot justify traversing that parent.
	mounts := []resourceMount{{path: "/", kind: "ext4"}, {path: "/must-not-be-accessed", kind: "nfs"}, {path: "/must-not-be-accessed/local", kind: "tmpfs"}}
	if _, reason := localFilesystemValues(context.Background(), "/must-not-be-accessed/local/file", mounts); reason != "unsupported_filesystem" {
		t.Fatal(reason)
	}
}
func TestFilesystemRejectsSymlinkPaths(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	mounts := []resourceMount{{path: "/", kind: "ext4"}}
	for _, path := range []string{link, filepath.Join(link, "missing")} {
		if _, reason := localFilesystemValues(context.Background(), path, mounts); reason != "symlink_path" {
			t.Fatalf("symlink: %s", reason)
		}
	}
}
func TestFilesystemMountMetadataFailsClosed(t *testing.T) {
	for _, data := range []string{"", "malformed", "1 0 8:0 / /bad\\999 rw - ext4 root rw\n", "1 0 8:0 / relative rw - ext4 root rw\n"} {
		if _, reason := parseResourceMounts(data); reason != "malformed" {
			t.Fatalf("accepted malformed mounts: %q", data)
		}
	}
	for _, kind := range []string{"ext2", "ext3", "ext4", "xfs", "btrfs", "tmpfs", "overlay", "zfs"} {
		if !localResourceFilesystem(kind) {
			t.Errorf("rejected local %s", kind)
		}
	}
	if _, reason := localFilesystemValues(context.Background(), "/", nil); reason != "unsupported_filesystem" {
		t.Fatal(reason)
	}
}
