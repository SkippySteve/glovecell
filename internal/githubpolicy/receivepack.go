package githubpolicy

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
)

// RefUpdate is one command of a git-receive-pack request:
// "<old-oid> SP <new-oid> SP <refname>", where an all-zero old id creates
// the ref and an all-zero new id deletes it (gitprotocol-pack, "Reference
// Update Request and Packfile Transfer").
type RefUpdate struct {
	Old, New, Ref string
}

// Create and Delete classify the update.
func (u RefUpdate) Create() bool { return zeroID(u.Old) }
func (u RefUpdate) Delete() bool { return zeroID(u.New) }

func zeroID(id string) bool { return strings.Trim(id, "0") == "" }

// MaxCommandBytes bounds the command section the proxy buffers before it
// decides. Real pushes send a few hundred bytes per ref.
const MaxCommandBytes = 64 << 10

// MaxRefUpdates bounds the refs one push may update.
const MaxRefUpdates = 32

// ReadCommands reads the pkt-line command section of a git-receive-pack
// request body, up to and including its flush-pkt, and returns the updates
// and the exact bytes it consumed, so the caller can replay them upstream in
// front of the rest of the body (shallow lines, push options and the pack).
//
// A body that is only a flush-pkt is git's probe for large pushes (see
// http.postBuffer); it updates nothing and returns no updates. Push
// certificates and protocol v2 requests are refused.
func ReadCommands(r *bufio.Reader) ([]RefUpdate, []byte, error) {
	var consumed bytes.Buffer
	var updates []RefUpdate
	first := true
	for {
		if consumed.Len() > MaxCommandBytes {
			return nil, nil, errors.New("push command section too large")
		}
		head := make([]byte, 4)
		if _, err := io.ReadFull(r, head); err != nil {
			return nil, nil, errors.New("push request ended inside its command section")
		}
		consumed.Write(head)
		n, err := strconv.ParseUint(string(head), 16, 16)
		if err != nil {
			return nil, nil, errors.New("malformed pkt-line length")
		}
		if n == 0 {
			return updates, consumed.Bytes(), nil
		}
		if n < 4 || n > 65520 {
			return nil, nil, errors.New("unsupported pkt-line")
		}
		line := make([]byte, n-4)
		if _, err := io.ReadFull(r, line); err != nil {
			return nil, nil, errors.New("push request ended inside its command section")
		}
		consumed.Write(line)
		line = bytes.TrimSuffix(line, []byte("\n"))
		if bytes.HasPrefix(line, []byte("shallow ")) && len(updates) == 0 {
			continue // A shallow client names its boundary commits first.
		}
		if bytes.HasPrefix(line, []byte("push-cert")) {
			return nil, nil, errors.New("signed pushes are not brokered")
		}
		if first {
			// Capabilities follow a NUL on the first command only.
			line, _, _ = bytes.Cut(line, []byte{0})
			first = false
		} else if bytes.IndexByte(line, 0) >= 0 {
			return nil, nil, errors.New("malformed push command")
		}
		u, err := parseCommand(string(line))
		if err != nil {
			return nil, nil, err
		}
		updates = append(updates, u)
		if len(updates) > MaxRefUpdates {
			return nil, nil, errors.New("too many ref updates in one push")
		}
	}
}

func parseCommand(line string) (RefUpdate, error) {
	parts := strings.Split(line, " ")
	if len(parts) != 3 || !objectID(parts[0]) || !objectID(parts[1]) || len(parts[0]) != len(parts[1]) || !validRef(parts[2]) {
		return RefUpdate{}, errors.New("malformed push command")
	}
	if zeroID(parts[0]) && zeroID(parts[1]) {
		return RefUpdate{}, errors.New("malformed push command")
	}
	return RefUpdate{Old: parts[0], New: parts[1], Ref: parts[2]}, nil
}

func objectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// validRef applies the parts of git-check-ref-format that matter here.
func validRef(ref string) bool {
	if len(ref) > 255 || !strings.HasPrefix(ref, "refs/") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".lock") || strings.HasSuffix(ref, ".") {
		return false
	}
	if strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") {
		return false
	}
	for _, c := range ref {
		if c < 0x21 || c == 0x7f || strings.ContainsRune("~^:?*[\\", c) {
			return false
		}
	}
	return true
}

// CheckPush decides a push's ref updates. defaultBranch is the repository's
// current default branch as GitHub reports it; empty means unknown, which
// protects every branch. The push is refused as a whole if any update is:
// git applies the allowed ones otherwise, which would leave a half push.
//
//   - Branches: create and update unprotected branches. The default branch and
//     ProtectedBranches refuse every update, which covers force pushes to
//     them. Deleting any branch is refused.
//   - Tags: create only, and only with AllowReleases. Moving or deleting a tag
//     is refused.
//   - Any other ref namespace is refused.
func (p *Policy) CheckPush(updates []RefUpdate, defaultBranch string) error {
	for _, u := range updates {
		switch {
		case strings.HasPrefix(u.Ref, "refs/heads/"):
			branch := strings.TrimPrefix(u.Ref, "refs/heads/")
			if defaultBranch != "" && branch == defaultBranch {
				return errors.New("pushing to the default branch (" + branch + ") is not allowed; push another branch and open a pull request")
			}
			if p.BranchProtected(branch, defaultBranch) {
				return errors.New("branch " + branch + " is protected; push another branch")
			}
			if u.Delete() {
				return errors.New("deleting branch " + branch + " is not brokered")
			}
		case strings.HasPrefix(u.Ref, "refs/tags/"):
			if !p.AllowReleases {
				return errors.New("pushing tags is off for this project")
			}
			if !u.Create() {
				return errors.New("moving or deleting tag " + strings.TrimPrefix(u.Ref, "refs/tags/") + " is not brokered")
			}
		default:
			return errors.New("only branches and tags can be pushed")
		}
	}
	return nil
}
