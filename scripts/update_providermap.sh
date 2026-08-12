#!/usr/bin/env bash

set -euo pipefail

go build -o ./pulumi-tool-import .
./pulumi-tool-import update-providermap pkg/providermap/versions.yaml "$@"
