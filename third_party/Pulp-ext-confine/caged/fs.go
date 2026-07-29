package caged

import (
	"io"
	"os"
	"os/exec"
)

// execLookPath is a thin wrapper around exec.LookPath so caged.go can resolve
// jail binaries without importing os/exec directly (keeps the platform-neutral
// file dependency-light and easy to stub in tests).
func execLookPath(name string) (string, error) { return exec.LookPath(name) }

// linkOrCopy materialises dst as a reference to src: symlink, else hard link,
// else a plain copy (Windows without Developer Mode / cross-volume). Idempotent.
func linkOrCopy(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return nil
	}
	if err := os.Symlink(src, dst); err == nil {
		return nil
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
