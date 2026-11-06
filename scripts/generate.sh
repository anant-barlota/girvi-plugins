#!/usr/bin/env bash
# Regenerates the Go stubs from sdk/proto. Needs protoc, protoc-gen-go and
# protoc-gen-go-grpc on PATH.
set -euo pipefail
cd "$(dirname "$0")/.."
find sdk/proto -name '*.proto' -print0 | xargs -0 protoc \
  -I sdk/proto \
  --go_out=sdk/gen --go_opt=module=github.com/anant-barlota/girvi-plugins/sdk/gen \
  --go-grpc_out=sdk/gen --go-grpc_opt=module=github.com/anant-barlota/girvi-plugins/sdk/gen
