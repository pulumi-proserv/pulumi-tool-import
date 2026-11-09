#!/usr/bin/env python3
# Copyright 2026, Pulumi Corporation.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Verify production archive policy and optionally reject deliberate defects."""

import argparse
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import uuid


# Gobra v26.09, including its Java runtime and Z3 solver.
IMAGE = "ghcr.io/viperproject/gobra@sha256:5e96431b06c2e6209aa767df9153bb4cf8b2620598598f1d7d3f3a74c09c14f9"
ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "pkg" / "archive_policy.go"

# Replace implementation statements only. Keep each mutant's contract unchanged.
MUTATIONS = {
    "trust-missing-hash": (
        "case archiveHashMissing:\n",
        "case archiveHashMissing:\n\t\tdecision.Verified = true\n",
    ),
    "accept-mismatching-hash": (
        "decision.Rejection = archiveRejectMismatch",
        "decision.Rejection = archiveAccepted",
    ),
    "replace-unverified-conflict": (
        "decision.Rejection = archiveRejectConflict",
        "decision.Repair = true",
    ),
    "repair-already-matching-archive": (
        "decision.Repair = !state.InputMatches || !state.OutputMatches",
        "decision.Repair = true",
    ),
    "ignore-field-local-input-change": (
        "return facts.InputPatched && facts.DigestPresent && facts.OutputPresent && !facts.OutputMatches",
        "return facts.DigestPresent && facts.OutputPresent && !facts.OutputMatches",
    ),
    "lose-output-only-change": (
        "changed = inputPatched || outputPatched",
        "changed = inputPatched",
    ),
    "omit-output-only-validation": (
        "recordAsset = changed && assetCandidate && hasAssetKind",
        "recordAsset = inputPatched && assetCandidate && hasAssetKind",
    ),
}


def verify(runtime, source):
    name = "gobra-archive-" + uuid.uuid4().hex
    command = [
        runtime, "run", "--rm", "--name", name,
        "--platform", "linux/amd64", "--network", "none",
        "--tmpfs", "/tmp/gobra:rw,size=64m",
        "--volume", f"{source.parent}:/proof:ro",
        IMAGE,
        "--input", f"/proof/{source.name}",
        "--gobraDirectory", "/tmp/gobra",
        "--overflow", "--logLevel", "INFO",
    ]
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=180)
    except subprocess.TimeoutExpired:
        subprocess.run([runtime, "rm", "--force", name], capture_output=True, timeout=30)
        raise RuntimeError("Gobra exceeded the 180-second verification limit") from None
    output = result.stdout + result.stderr
    print(output, end="", flush=True)
    return result.returncode, output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mutations", action="store_true")
    parser.add_argument("--runtime", default=os.environ.get("CONTAINER_RUNTIME", "podman"))
    args = parser.parse_args()

    print("Verifying pkg/archive_policy.go", flush=True)
    code, output = verify(args.runtime, SOURCE)
    if code != 0 or "Gobra found 0 errors." not in output or " ERROR " in output:
        raise RuntimeError("Production policy did not verify successfully")

    if args.mutations:
        source = SOURCE.read_text()
        # macOS Podman machines share the home directory by default, not every temp directory.
        with tempfile.TemporaryDirectory(prefix=".gobra-mutations-", dir=Path.home()) as directory:
            mutant = Path(directory) / SOURCE.name
            for name, (old, new) in MUTATIONS.items():
                if source.count(old) != 1:
                    raise RuntimeError(f"Mutation {name} no longer matches exactly one statement")
                mutant.write_text(source.replace(old, new, 1))
                print(f"Checking mutation: {name}", flush=True)
                code, output = verify(args.runtime, mutant)
                # Parser failures, timeouts, and runtime errors are not proof failures.
                if code == 0 or "Postcondition might not hold." not in output:
                    raise RuntimeError(f"Mutation {name} did not produce a postcondition failure")
        print(f"All {len(MUTATIONS)} mutations failed their contracts.")


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, subprocess.TimeoutExpired) as error:
        print(f"Verification failed: {error}", file=sys.stderr)
        sys.exit(1)
