#!/usr/bin/env python3
"""Jailed filesystem helper for agent-project-controller FUSE mounts.

Speaks length-prefixed frames on stdin/stdout:
  uint32 total_len (big-endian), uint32 header_len, JSON header, payload.

Roots are supplied as argv after --. Absolute realpaths must stay under a root.
"""
from __future__ import annotations

import json
import os
import struct
import sys
from typing import Iterable

MAX_FRAME = 32 << 20


def _roots(argv: list[str]) -> list[str]:
    if "--" in argv:
        argv = argv[argv.index("--") + 1 :]
    roots = [os.path.realpath(r) for r in argv]
    if not roots:
        raise SystemExit("helper requires at least one root after --")
    return roots


def _under(candidate: str, roots: list[str]) -> bool:
    for root in roots:
        if candidate == root or candidate.startswith(root + os.sep):
            return True
    return False


def _confine(path: str, roots: list[str], follow: bool = True) -> str:
    if not path or not os.path.isabs(path):
        raise OSError("path must be absolute")
    path = os.path.normpath(path)
    parent = os.path.dirname(path)
    name = os.path.basename(path)
    if parent == path:
        candidate = path
        if follow and os.path.lexists(candidate):
            candidate = os.path.realpath(candidate)
    else:
        resolved_parent = os.path.realpath(parent) if os.path.lexists(parent) else parent
        if not _under(resolved_parent, roots) and not _under(path, roots):
            raise OSError("path escapes jail")
        candidate = os.path.join(resolved_parent, name)
        if follow and os.path.lexists(candidate):
            candidate = os.path.realpath(candidate)
        elif not follow:
            # Keep the final symlink component; parent must stay jailed.
            if not _under(resolved_parent, roots) and candidate not in roots:
                if not _under(candidate, roots):
                    raise OSError("path escapes jail")
            return candidate
    if not _under(candidate, roots):
        raise OSError("path escapes jail")
    return candidate


def _read_frame(inp) -> tuple[dict, bytes]:
    hdr = inp.read(8)
    if not hdr:
        raise EOFError
    if len(hdr) < 8:
        raise OSError("short frame header")
    total, header_len = struct.unpack(">II", hdr)
    if total > MAX_FRAME or header_len > total:
        raise OSError("invalid frame lengths")
    body = inp.read(total)
    if len(body) < total:
        raise OSError("short frame body")
    header = json.loads(body[:header_len].decode("utf-8"))
    return header, body[header_len:]


def _write_frame(out, header: dict, payload: bytes = b"") -> None:
    hb = json.dumps(header, separators=(",", ":")).encode("utf-8")
    if len(hb) + len(payload) > MAX_FRAME:
        raise OSError("frame too large")
    out.write(struct.pack(">II", len(hb) + len(payload), len(hb)))
    out.write(hb)
    if payload:
        out.write(payload)
    out.flush()


def _stat_payload(path: str) -> dict:
    st = os.lstat(path)
    return {
        "mode": int(st.st_mode),
        "size": int(st.st_size),
        "mtime": int(st.st_mtime),
        "atime": int(st.st_atime),
        "uid": int(st.st_uid),
        "gid": int(st.st_gid),
        "nlink": int(st.st_nlink),
        "ino": int(st.st_ino),
    }


def handle(header: dict, payload: bytes, roots: list[str]) -> tuple[dict, bytes]:
    op = header.get("op")
    path = header.get("path") or ""
    try:
        if op == "ping":
            return {"op": "ping", "ok": True}, b""
        if op == "stat":
            p = _confine(path, roots, follow=False)
            return {"op": "stat", "ok": True, "path": p, **_stat_payload(p)}, b""
        if op == "readdir":
            p = _confine(path, roots, follow=True)
            names = sorted(os.listdir(p))
            return {"op": "readdir", "ok": True, "path": p, "names": names}, b""
        if op == "read":
            p = _confine(path, roots, follow=True)
            offset = int(header.get("offset") or 0)
            size = int(header.get("size") or 0)
            with open(p, "rb") as f:
                f.seek(offset)
                data = f.read(size)
            return {"op": "read", "ok": True, "path": p, "size": len(data)}, data
        if op == "write":
            p = _confine(path, roots, follow=True)
            offset = int(header.get("offset") or 0)
            flags = os.O_WRONLY
            fd = os.open(p, flags)
            try:
                os.lseek(fd, offset, os.SEEK_SET)
                n = os.write(fd, payload)
            finally:
                os.close(fd)
            return {"op": "write", "ok": True, "path": p, "size": n}, b""
        if op == "create":
            p = _confine(path, roots, follow=False)
            mode = int(header.get("mode") or 0o644)
            fd = os.open(p, os.O_CREAT | os.O_EXCL | os.O_WRONLY, mode)
            os.close(fd)
            return {"op": "create", "ok": True, "path": p}, b""
        if op == "mkdir":
            p = _confine(path, roots, follow=False)
            mode = int(header.get("mode") or 0o755)
            os.mkdir(p, mode)
            return {"op": "mkdir", "ok": True, "path": p}, b""
        if op == "unlink":
            p = _confine(path, roots, follow=False)
            os.unlink(p)
            return {"op": "unlink", "ok": True, "path": p}, b""
        if op == "rmdir":
            p = _confine(path, roots, follow=False)
            os.rmdir(p)
            return {"op": "rmdir", "ok": True, "path": p}, b""
        if op == "rename":
            src = _confine(path, roots, follow=False)
            dst = _confine(header.get("to") or "", roots, follow=False)
            os.rename(src, dst)
            return {"op": "rename", "ok": True, "path": src, "to": dst}, b""
        if op == "symlink":
            p = _confine(path, roots, follow=False)
            target = header.get("to") or ""
            if "\x00" in target:
                raise OSError("invalid symlink target")
            os.symlink(target, p)
            return {"op": "symlink", "ok": True, "path": p}, b""
        if op == "readlink":
            p = _confine(path, roots, follow=False)
            target = os.readlink(p)
            return {"op": "readlink", "ok": True, "path": p, "to": target}, b""
        if op == "chmod":
            p = _confine(path, roots, follow=False)
            os.chmod(p, int(header.get("mode") or 0), follow_symlinks=False)
            return {"op": "chmod", "ok": True, "path": p}, b""
        if op == "truncate":
            p = _confine(path, roots, follow=True)
            os.truncate(p, int(header.get("size") or 0))
            return {"op": "truncate", "ok": True, "path": p}, b""
        if op == "utimens":
            p = _confine(path, roots, follow=False)
            atime = float(header.get("atime") or 0)
            mtime = float(header.get("mtime") or 0)
            os.utime(p, (atime, mtime), follow_symlinks=False)
            return {"op": "utimens", "ok": True, "path": p}, b""
        return {"op": op or "", "ok": False, "error": "unknown op"}, b""
    except OSError as e:
        return {"op": op or "", "ok": False, "error": str(e)}, b""
    except Exception as e:  # noqa: BLE001 — surface to Go as fail-closed error
        return {"op": op or "", "ok": False, "error": str(e)}, b""


def main(argv: Iterable[str] | None = None) -> int:
    roots = _roots(list(argv if argv is not None else sys.argv[1:]))
    inp = sys.stdin.buffer
    out = sys.stdout.buffer
    while True:
        try:
            header, payload = _read_frame(inp)
        except EOFError:
            return 0
        resp, data = handle(header, payload, roots)
        _write_frame(out, resp, data)


if __name__ == "__main__":
    raise SystemExit(main())
