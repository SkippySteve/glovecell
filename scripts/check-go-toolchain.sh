#!/bin/sh
# Run as the workspace user. Only the standard library is needed: this check
# must not download a toolchain or modules to conceal an incomplete image.
set -eu
task_dir=$(mktemp -d /tmp/apc-go-toolchain.XXXXXX)
trap 'rm -rf "$task_dir"' EXIT HUP INT TERM
command -v go
command -v gofmt
GOTOOLCHAIN=local go version
cat > "$task_dir/go.mod" <<'EOF'
module example.invalid/glovecell-toolchain-check

go 1.27.3
EOF
cat > "$task_dir/add.go" <<'EOF'
package toolchaincheck
func Add(a,b int)int{return a+b}
EOF
cat > "$task_dir/add_test.go" <<'EOF'
package toolchaincheck
import "testing"
func TestAdd(t *testing.T){if Add(2,3)!=5{t.Fatal("incorrect sum")}}
EOF
gofmt -w "$task_dir/add.go" "$task_dir/add_test.go"
test -z "$(gofmt -l "$task_dir/add.go" "$task_dir/add_test.go")"
cd "$task_dir"
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off \
    GOCACHE="$task_dir/cache" GOMODCACHE="$task_dir/modcache" \
    GOPATH="$task_dir/gopath" go test -count=1 ./...
