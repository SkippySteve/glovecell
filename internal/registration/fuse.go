package registration

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// remoteNode exposes one jailed remote path through FUSE.
type remoteNode struct {
	fs.Inode
	remote *Remote
	path   string
}

var (
	_ fs.NodeGetattrer  = (*remoteNode)(nil)
	_ fs.NodeLookuper   = (*remoteNode)(nil)
	_ fs.NodeReaddirer  = (*remoteNode)(nil)
	_ fs.NodeOpener     = (*remoteNode)(nil)
	_ fs.NodeCreater    = (*remoteNode)(nil)
	_ fs.NodeMkdirer    = (*remoteNode)(nil)
	_ fs.NodeUnlinker   = (*remoteNode)(nil)
	_ fs.NodeRmdirer    = (*remoteNode)(nil)
	_ fs.NodeRenamer    = (*remoteNode)(nil)
	_ fs.NodeSymlinker  = (*remoteNode)(nil)
	_ fs.NodeReadlinker = (*remoteNode)(nil)
	_ fs.NodeSetattrer  = (*remoteNode)(nil)
)

func errno(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	if e, ok := err.(syscall.Errno); ok {
		return e
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "No such file"), strings.Contains(msg, "not found"):
		return syscall.ENOENT
	case strings.Contains(msg, "File exists"):
		return syscall.EEXIST
	case strings.Contains(msg, "escapes jail"), strings.Contains(msg, "Permission"):
		return syscall.EACCES
	case strings.Contains(msg, "Not a directory"):
		return syscall.ENOTDIR
	case strings.Contains(msg, "Is a directory"):
		return syscall.EISDIR
	default:
		return syscall.EIO
	}
}

func (n *remoteNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	attrs, err := n.remote.Stat(n.path)
	if err != nil {
		return errno(err)
	}
	fillAttr(&out.Attr, attrs)
	out.SetTimeout(time.Second)
	return 0
}

func fillAttr(out *fuse.Attr, attrs StatAttrs) {
	out.Mode = attrs.Mode
	out.Size = uint64(attrs.Size)
	out.Nlink = attrs.Nlink
	out.Uid = attrs.UID
	out.Gid = attrs.GID
	out.Ino = attrs.Ino
	out.Mtime = uint64(attrs.Mtime)
	out.Atime = uint64(attrs.Atime)
	out.Ctime = uint64(attrs.Mtime)
}

func (n *remoteNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	childPath := path.Join(n.path, name)
	attrs, err := n.remote.Stat(childPath)
	if err != nil {
		return nil, errno(err)
	}
	child := &remoteNode{remote: n.remote, path: childPath}
	stable := fs.StableAttr{Mode: attrs.Mode & syscall.S_IFMT, Ino: attrs.Ino}
	inode := n.NewInode(ctx, child, stable)
	fillAttr(&out.Attr, attrs)
	out.SetEntryTimeout(time.Second)
	out.SetAttrTimeout(time.Second)
	return inode, 0
}

func (n *remoteNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	names, err := n.remote.ReadDir(n.path)
	if err != nil {
		return nil, errno(err)
	}
	entries := make([]fuse.DirEntry, 0, len(names))
	for _, name := range names {
		attrs, err := n.remote.Stat(path.Join(n.path, name))
		mode := uint32(syscall.S_IFREG)
		var ino uint64
		if err == nil {
			mode = attrs.Mode & syscall.S_IFMT
			ino = attrs.Ino
		}
		entries = append(entries, fuse.DirEntry{Name: name, Mode: mode, Ino: ino})
	}
	return fs.NewListDirStream(entries), 0
}

type remoteFile struct {
	remote *Remote
	path   string
}

var (
	_ fs.FileReader = (*remoteFile)(nil)
	_ fs.FileWriter = (*remoteFile)(nil)
)

func (f *remoteFile) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	data, err := f.remote.Read(f.path, off, int64(len(dest)))
	if err != nil {
		return nil, errno(err)
	}
	return fuse.ReadResultData(data), 0
}

func (f *remoteFile) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	n, err := f.remote.Write(f.path, off, data)
	if err != nil {
		return 0, errno(err)
	}
	return uint32(n), 0
}

func (n *remoteNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return &remoteFile{remote: n.remote, path: n.path}, fuse.FOPEN_DIRECT_IO, 0
}

func (n *remoteNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	childPath := path.Join(n.path, name)
	if err := n.remote.Create(childPath, mode); err != nil {
		return nil, nil, 0, errno(err)
	}
	attrs, err := n.remote.Stat(childPath)
	if err != nil {
		return nil, nil, 0, errno(err)
	}
	child := &remoteNode{remote: n.remote, path: childPath}
	inode := n.NewInode(ctx, child, fs.StableAttr{Mode: attrs.Mode & syscall.S_IFMT, Ino: attrs.Ino})
	fillAttr(&out.Attr, attrs)
	return inode, &remoteFile{remote: n.remote, path: childPath}, fuse.FOPEN_DIRECT_IO, 0
}

func (n *remoteNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	childPath := path.Join(n.path, name)
	if err := n.remote.Mkdir(childPath, mode); err != nil {
		return nil, errno(err)
	}
	attrs, err := n.remote.Stat(childPath)
	if err != nil {
		return nil, errno(err)
	}
	child := &remoteNode{remote: n.remote, path: childPath}
	inode := n.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR, Ino: attrs.Ino})
	fillAttr(&out.Attr, attrs)
	return inode, 0
}

func (n *remoteNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return errno(n.remote.Unlink(path.Join(n.path, name)))
}

func (n *remoteNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	return errno(n.remote.Rmdir(path.Join(n.path, name)))
}

func (n *remoteNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	np, ok := newParent.(*remoteNode)
	if !ok {
		return syscall.EXDEV
	}
	return errno(func() error {
		_, _, err := n.remote.Call(Header{Op: "rename", Path: path.Join(n.path, name), To: path.Join(np.path, newName)}, nil)
		return err
	}())
}

func (n *remoteNode) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	childPath := path.Join(n.path, name)
	_, _, err := n.remote.Call(Header{Op: "symlink", Path: childPath, To: target}, nil)
	if err != nil {
		return nil, errno(err)
	}
	attrs, err := n.remote.Stat(childPath)
	if err != nil {
		return nil, errno(err)
	}
	child := &remoteNode{remote: n.remote, path: childPath}
	inode := n.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFLNK, Ino: attrs.Ino})
	fillAttr(&out.Attr, attrs)
	return inode, 0
}

func (n *remoteNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	_, raw, _, err := n.remote.CallRaw(Header{Op: "readlink", Path: n.path}, nil)
	if err != nil {
		return nil, errno(err)
	}
	var parsed struct {
		To string `json:"to"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, syscall.EIO
	}
	return []byte(parsed.To), 0
}

func (n *remoteNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if m, ok := in.GetMode(); ok {
		if _, _, err := n.remote.Call(Header{Op: "chmod", Path: n.path, Mode: m}, nil); err != nil {
			return errno(err)
		}
	}
	if sz, ok := in.GetSize(); ok {
		if _, _, err := n.remote.Call(Header{Op: "truncate", Path: n.path, Size: int64(sz)}, nil); err != nil {
			return errno(err)
		}
	}
	attrs, err := n.remote.Stat(n.path)
	if err != nil {
		return errno(err)
	}
	fillAttr(&out.Attr, attrs)
	out.SetTimeout(time.Second)
	return 0
}

// MountRemote mounts remotePath from the helper at mountPoint. Caller must
// keep the returned server alive and eventually Unmount.
func MountRemote(mountPoint, fsName, remotePath string, remote *Remote) (*fuse.Server, error) {
	if err := os.MkdirAll(mountPoint, 0o700); err != nil {
		return nil, err
	}
	root := &remoteNode{remote: remote, path: remotePath}
	// DirectMount uses mount(2) inside the privileged control pod, which has
	// no fusermount. Unprivileged callers fall back to fusermount.
	opts := &fs.Options{MountOptions: fuse.MountOptions{FsName: fsName, Name: "apc", AllowOther: false, DirectMount: true}}
	return fs.Mount(mountPoint, root, opts)
}
