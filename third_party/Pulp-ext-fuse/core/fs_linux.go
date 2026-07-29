//go:build linux

package core

import (
	"context"
	"os"
	"path/filepath"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// VDrive is the policy-enforcing virtual drive configuration.
type VDrive struct {
	Backing string
	Policy  Policy
	Hooks   Hooks
}

// Server wraps the FUSE server for lifecycle management.
type Server struct {
	server *fuse.Server
}

// Unmount tears down the FUSE mount.
func (s *Server) Unmount() error {
	return s.server.Unmount()
}

// Mount mounts vd.Backing at mountpoint with policy enforcement.
// Returns a *Server that must be Unmount()ed when done.
func Mount(mountpoint string, vd *VDrive) (*Server, error) {
	root := &policyNode{
		vd:       vd,
		realPath: vd.Backing,
		relPath:  "",
	}
	opts := &fs.Options{
		MountOptions: fuse.MountOptions{
			Debug:          false,
			DirectMount:    true,
			AllowOther:     false,
			FsName:         "vdrive",
			Name:           "vdrive",
			DisableXAttrs:  true,
			EnableLocks:    false,
		},
	}
	srv, err := fs.Mount(mountpoint, root, opts)
	if err != nil {
		return nil, err
	}
	return &Server{server: srv}, nil
}

// policyNode is a custom FUSE inode that enforces VDrive policy.
// Every operation checks policy before delegating to real OS calls.
type policyNode struct {
	fs.Inode
	vd       *VDrive
	realPath string // absolute path on the backing filesystem
	relPath  string // path relative to the mount root (empty for root)
}

// enforce checks whether op with want access is allowed on relpath.
// Calls OnMiss if policy is insufficient, then Audit.
// Returns true if allowed.
func (n *policyNode) enforce(op, relpath string, want Access) bool {
	acc := n.vd.Policy.Check(relpath)
	if acc < want {
		if n.vd.Hooks.OnMiss != nil {
			acc = n.vd.Hooks.OnMiss(relpath, want)
		}
	}
	allowed := acc >= want
	if n.vd.Hooks.Audit != nil {
		n.vd.Hooks.Audit(op, relpath, allowed)
	}
	return allowed
}

// toStat extracts *syscall.Stat_t from os.FileInfo.
func toStat(fi os.FileInfo) *syscall.Stat_t {
	return fi.Sys().(*syscall.Stat_t)
}

// stableAttr builds fs.StableAttr from a stat.
func stableAttr(st *syscall.Stat_t) fs.StableAttr {
	return fs.StableAttr{
		Mode: st.Mode,
		Ino:  st.Ino,
	}
}

// childRel computes the relpath for a named child of this node.
func (n *policyNode) childRel(name string) string {
	if n.relPath == "" {
		return name
	}
	return filepath.Join(n.relPath, name)
}

// --- fs.NodeGetattrer ---

func (n *policyNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	fi, err := os.Lstat(n.realPath)
	if err != nil {
		return fs.ToErrno(err)
	}
	out.Attr.FromStat(toStat(fi))
	return 0
}

// --- fs.NodeLookuper ---

func (n *policyNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	crel := n.childRel(name)
	creal := filepath.Join(n.realPath, name)

	if !n.enforce("Lookup", crel, Read) {
		return nil, syscall.EACCES
	}

	fi, err := os.Lstat(creal)
	if err != nil {
		return nil, fs.ToErrno(err)
	}
	st := toStat(fi)
	out.Attr.FromStat(st)

	child := &policyNode{vd: n.vd, realPath: creal, relPath: crel}
	inode := n.NewInode(ctx, child, stableAttr(st))
	return inode, 0
}

// --- fs.NodeReaddirer ---

func (n *policyNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	if !n.enforce("Readdir", n.relPath, Read) {
		return nil, syscall.EACCES
	}

	entries, err := os.ReadDir(n.realPath)
	if err != nil {
		return nil, fs.ToErrno(err)
	}

	dirs := make([]fuse.DirEntry, 0, len(entries))
	for _, e := range entries {
		crel := n.childRel(e.Name())
		// enforce runs Policy.Check + OnMiss + Audit so hidden-children are
		// recorded in the audit log (previously Policy.Check was called directly
		// here, silently dropping denied entries with no audit event).
		if !n.enforce("Readdir:child", crel, Read) {
			continue
		}
		info, err2 := e.Info()
		if err2 != nil {
			continue
		}
		st := toStat(info)
		dirs = append(dirs, fuse.DirEntry{
			Mode: st.Mode,
			Name: e.Name(),
			Ino:  st.Ino,
		})
	}
	return fs.NewListDirStream(dirs), 0
}

// --- fs.NodeOpener ---

func (n *policyNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	want := Read
	if flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
		want = ReadWrite
	}
	if !n.enforce("Open", n.relPath, want) {
		return nil, 0, syscall.EACCES
	}

	fd, err := syscall.Open(n.realPath, int(flags)&^syscall.O_CREAT, 0)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}
	return fs.NewLoopbackFile(fd), fuse.FOPEN_KEEP_CACHE, 0
}

// --- fs.NodeCreater ---

func (n *policyNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	// Creation requires ReadWrite on the parent directory.
	if !n.enforce("Create", n.relPath, ReadWrite) {
		return nil, nil, 0, syscall.EACCES
	}

	crel := n.childRel(name)
	creal := filepath.Join(n.realPath, name)

	fd, err := syscall.Open(creal, int(flags)|syscall.O_CREAT, mode)
	if err != nil {
		return nil, nil, 0, fs.ToErrno(err)
	}

	fi, serr := os.Lstat(creal)
	if serr != nil {
		syscall.Close(fd)
		return nil, nil, 0, fs.ToErrno(serr)
	}
	st := toStat(fi)
	out.Attr.FromStat(st)

	child := &policyNode{vd: n.vd, realPath: creal, relPath: crel}
	inode := n.NewInode(ctx, child, stableAttr(st))
	fh := fs.NewLoopbackFile(fd)
	return inode, fh, 0, 0
}

// --- fs.NodeMkdirer ---

func (n *policyNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if !n.enforce("Mkdir", n.relPath, ReadWrite) {
		return nil, syscall.EACCES
	}

	crel := n.childRel(name)
	creal := filepath.Join(n.realPath, name)

	if err := syscall.Mkdir(creal, mode); err != nil {
		return nil, fs.ToErrno(err)
	}

	fi, serr := os.Lstat(creal)
	if serr != nil {
		return nil, fs.ToErrno(serr)
	}
	st := toStat(fi)
	out.Attr.FromStat(st)

	child := &policyNode{vd: n.vd, realPath: creal, relPath: crel}
	return n.NewInode(ctx, child, stableAttr(st)), 0
}

// --- fs.NodeUnlinker ---

func (n *policyNode) Unlink(ctx context.Context, name string) syscall.Errno {
	if !n.enforce("Unlink", n.relPath, ReadWrite) {
		return syscall.EACCES
	}
	creal := filepath.Join(n.realPath, name)
	return fs.ToErrno(syscall.Unlink(creal))
}

// --- fs.NodeRmdirer ---

func (n *policyNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	if !n.enforce("Rmdir", n.relPath, ReadWrite) {
		return syscall.EACCES
	}
	creal := filepath.Join(n.realPath, name)
	return fs.ToErrno(syscall.Rmdir(creal))
}

// --- fs.NodeReadlinker (symlink support) ---

func (n *policyNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	if !n.enforce("Readlink", n.relPath, Read) {
		return nil, syscall.EACCES
	}
	target, err := os.Readlink(n.realPath)
	if err != nil {
		return nil, fs.ToErrno(err)
	}
	return []byte(target), 0
}
