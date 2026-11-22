# Every image target of the Dockerfile in one BuildKit graph (.forgejo/workflows/security.yml).
# Stages the targets share (the controller binary, the web console, the Go
# toolchain) build once; the rest build in parallel. Locally:
#   SOURCE_REVISION=$(git rev-parse HEAD) docker buildx bake
variable "SOURCE_REVISION" { default = "dev" }
variable "VERSION" { default = "0.1.0-alpha.1" }
variable "SOURCE_URL" { default = "" }

group "default" {
  targets = ["runtime", "workspace", "voice", "t3-controller", "glove-hub", "session-hub", "ao-workspace"]
}

target "_common" {
  context    = "."
  dockerfile = "Dockerfile"
  args = {
    VERSION         = VERSION
    SOURCE_REVISION = SOURCE_REVISION
  }
  labels = {
    "org.opencontainers.image.revision" = SOURCE_REVISION
    "org.opencontainers.image.source"   = SOURCE_URL
  }
  # Load into the daemon's image store: the gate scans and push-image.sh pushes from there.
  output = ["type=docker"]
}

target "runtime" {
  inherits = ["_common"]
  target   = "runtime"
  tags     = ["candidate-runtime:${SOURCE_REVISION}"]
}
target "workspace" {
  inherits = ["_common"]
  target   = "workspace"
  tags     = ["candidate-workspace:${SOURCE_REVISION}"]
}
target "voice" {
  inherits = ["_common"]
  target   = "voice"
  tags     = ["candidate-voice:${SOURCE_REVISION}"]
}
target "t3-controller" {
  inherits = ["_common"]
  target   = "t3-controller"
  tags     = ["candidate-t3-controller:${SOURCE_REVISION}"]
}
target "glove-hub" {
  inherits = ["_common"]
  target   = "glove-hub"
  tags     = ["candidate-glove-hub:${SOURCE_REVISION}"]
}
target "session-hub" {
  inherits = ["_common"]
  target   = "session-hub"
  tags     = ["candidate-session-hub:${SOURCE_REVISION}"]
}
target "ao-workspace" {
  inherits = ["_common"]
  target   = "ao-workspace"
  tags     = ["candidate-ao-workspace:${SOURCE_REVISION}"]
}
