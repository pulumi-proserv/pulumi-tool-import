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

"""Disposable AWS Lambda archive experiment. Never invokes pulumi up/destroy.

Requires boto3, npm, and AWS credentials. Every Pulumi operation authenticates
with the supplied login command in the supplied isolated home. Raw state stays
in the private output directory; report.json contains diagnostic summaries.
"""

import argparse
import base64
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import shlex
import subprocess
import time
import uuid
import zipfile

import boto3
from botocore.exceptions import ClientError


def zip_bytes(text):
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w", zipfile.ZIP_DEFLATED) as archive:
        info = zipfile.ZipInfo("index.js", (2020, 1, 1, 0, 0, 0))
        info.external_attr = 0o100755 << 16
        archive.writestr(info, text)
        archive.writestr("opaque.bin", bytes([0, 255, 254, 128, 1, 2]))
    return stream.getvalue()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tool", type=Path, required=True)
    parser.add_argument("--legacy-tool", type=Path)
    parser.add_argument("--expect-fixed", action="store_true")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--region", default="us-west-2")
    parser.add_argument("--pulumi-home", type=Path, required=True)
    parser.add_argument("--login-command", required=True, help="Trusted shell login command executed before every Pulumi operation")
    args = parser.parse_args()
    root = args.output.resolve()
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
    project = root / "project"
    project.mkdir(mode=0o700)
    backend = root / "backend"
    backend.mkdir(mode=0o700)
    source = root / "source"
    source.mkdir(mode=0o700)
    name = "archive-repro-" + uuid.uuid4().hex[:10]
    stack = "archive-repro"
    project_name = "archive-repro"
    report = {"resource": name, "region": args.region, "provider": "7.49.0", "scenarios": [], "cleanup": {}}

    def save(path, value):
        path.write_text(json.dumps(value, indent=2) + "\n")
        path.chmod(0o600)

    counter = 0

    def invoke(executable, command):
        nonlocal counter
        counter += 1
        shell = (
            f'export PULUMI_HOME={shlex.quote(str(args.pulumi_home.expanduser().resolve()))}; unset PULUMI_BACKEND_URL; '
            f'{args.login_command} >&2 && '
            f'PULUMI_BACKEND_URL={shlex.quote(backend.as_uri())} '
            + shlex.join([executable, *command])
        )
        result = subprocess.run(
            ["zsh", "-ic", shell], cwd=project, text=True, capture_output=True,
            env={**os.environ, "PULUMI_CONFIG_PASSPHRASE": "disposable-archive-repro", "PULUMI_SKIP_UPDATE_CHECK": "true"},
            timeout=600,
        )
        save(root / f"command-{counter:02}.json", {"executable": executable, "args": command, "returncode": result.returncode, "stdout": result.stdout, "stderr": result.stderr})
        if result.returncode:
            raise RuntimeError(f"{executable} {command[0]} failed; see command-{counter:02}.json")
        return result.stdout

    def pulumi(*command):
        return invoke("pulumi", command)

    def function_resource(state):
        return next(r for r in state["deployment"]["resources"] if r["type"] == "aws:lambda/function:Function")

    def patch(label, state, digest, binary=None, extra_args=()):
        state_path = root / f"{label}-before.json"
        digest_path = root / f"{label}-digest.json"
        output_path = root / f"{label}-patched.json"
        save(state_path, state)
        save(digest_path, digest)
        result = subprocess.run([
            str((binary or args.tool).resolve()), "patch-state", "tf", "--state", str(state_path),
            "--digest", str(digest_path), "--fields", str(Path(__file__).resolve().parents[1] / "data/aws-import-diff-fields.json"),
            "--mapping-file", str(root / "mappings.yaml"), "--config-dir", str(source),
            "--out", str(output_path), *extra_args,
        ], cwd=project, capture_output=True, text=True, timeout=300)
        save(root / f"{label}-tool.json", {"returncode": result.returncode, "stdout": result.stdout, "stderr": result.stderr})
        if result.returncode:
            assert not output_path.exists(), "failed file-mode patch left output state"
            return None
        return json.loads(output_path.read_text())

    save(root / "report.json", report)
    save(project / "package.json", {"name": project_name, "private": True, "dependencies": {"@pulumi/pulumi": "3.255.0", "@pulumi/aws": "7.49.0"}, "devDependencies": {"typescript": "5.9.3", "@types/node": "22.18.0"}})
    save(project / "tsconfig.json", {"compilerOptions": {"module": "NodeNext", "moduleResolution": "NodeNext", "target": "ES2020", "strict": True}})
    (project / "Pulumi.yaml").write_text(f"name: {project_name}\nruntime: nodejs\n")
    subprocess.run(["npm", "install", "--ignore-scripts", "--no-audit", "--no-fund"], cwd=project, check=True, timeout=300)
    report["pulumi_cli"] = pulumi("version").strip()
    correct = zip_bytes("exports.handler = async () => ({ok: true});\n")
    stale = zip_bytes("exports.handler = async () => ({stale: true});\n")
    (project / "program.zip").write_bytes(correct)
    (source / "source.zip").write_bytes(correct)
    (root / "mappings.yaml").write_text("resources:\n  aws_lambda_function.fn: fn\n")
    iam = boto3.client("iam", region_name=args.region)
    aws_lambda = boto3.client("lambda", region_name=args.region)
    role_created = False
    function_created = False
    stack_created = False
    try:
        role = iam.create_role(RoleName=name, AssumeRolePolicyDocument=json.dumps({"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Principal": {"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}]}), Tags=[{"Key": "Purpose", "Value": "pulumi-tool-import-archive-repro"}])["Role"]
        role_created = True
        for attempt in range(20):
            try:
                aws_lambda.create_function(FunctionName=name, Role=role["Arn"], Runtime="nodejs22.x", Handler="index.handler", Code={"ZipFile": correct}, Tags={"Purpose": "pulumi-tool-import-archive-repro"})
                function_created = True
                break
            except ClientError as error:
                if error.response["Error"]["Code"] != "InvalidParameterValueException" or attempt == 19:
                    raise
                time.sleep(3)
        aws_lambda.get_waiter("function_active_v2").wait(FunctionName=name)
        original = aws_lambda.get_function(FunctionName=name)["Configuration"]
        report["original"] = {key: original[key] for key in ("CodeSha256", "LastModified", "RevisionId")}
        assert original["CodeSha256"] == base64.b64encode(hashlib.sha256(correct).digest()).decode()
        program = (
            'import * as pulumi from "@pulumi/pulumi";\nimport * as aws from "@pulumi/aws";\n'
            'new aws.lambda.Function("fn", {'
            f'name: {json.dumps(name)}, role: {json.dumps(role["Arn"])}, '
            'runtime: "nodejs22.x", handler: "index.handler", publish: false, tags: {Purpose: "pulumi-tool-import-archive-repro"}, code: new pulumi.asset.FileArchive("./program.zip")});\n'
        )
        (project / "index.ts").write_text(program)
        pulumi("stack", "init", stack, "--non-interactive")
        stack_created = True
        pulumi("config", "set", "aws:region", args.region, "--stack", stack)
        save(project / "imports.json", {"resources": [{"type": "aws:lambda/function:Function", "name": "fn", "id": name, "version": "7.49.0"}]})
        pulumi("import", "--file", "imports.json", "--yes", "--skip-preview", "--generate-code=false", "--protect=false", "--stack", stack)
        base = json.loads(pulumi("stack", "export", "--show-secrets", "--stack", stack))
        save(root / "original-state.json", base)
        # Control the independent, write-only publication setting so this
        # experiment isolates archive reconciliation rather than publish diffs.
        for direction in ("inputs", "outputs"):
            function_resource(base)[direction]["publish"] = False
        report["controls"] = "Program retains fixture tags and publish=false; imported publish input/output normalized to false before archive experiments. Raw import retained separately."
        save(root / "controlled-state.json", base)
        pulumi("stack", "import", "--file", str(root / "controlled-state.json"), "--stack", stack)
        baseline = json.loads(pulumi("preview", "--json", "--show-sames", "--stack", stack))
        save(root / "original-preview.json", baseline)
        goal_code = next(s["newState"]["inputs"]["code"] for s in baseline["steps"] if s.get("newState", {}).get("type") == "aws:lambda/function:Function")
        digest = {"digestFormatVersion": 1, "modules": {}, "rootResources": [{"mode": "managed", "terraformAddress": "aws_lambda_function.fn", "translatedUrn": function_resource(base)["urn"], "importId": name, "attributes": {"filename": "source.zip", "function_name": name, "arn": original["FunctionArn"], "source_code_hash": original["CodeSha256"]}}]}
        tool_correct = patch("correct-local", base, digest)
        assert tool_correct is not None, "correct local artifact was rejected"
        canonical = copy.deepcopy(tool_correct)
        for direction in ("inputs", "outputs"):
            function_resource(canonical)[direction]["code"] = goal_code
        legacy = copy.deepcopy(canonical)
        function_resource(legacy)["outputs"].pop("__pulumi_raw_state_delta", None)
        (source / "source.zip").write_bytes(stale)
        tool_stale = patch("stale-local", base, digest)
        program_override = patch("program-overrides-stale-local", base, digest, extra_args=("--preview-json", str(root / "original-preview.json"), "--project-dir", str(project)))
        old_bad = patch("legacy-stale-local", base, digest, args.legacy_tool) if args.legacy_tool else tool_stale
        (source / "source.zip").write_bytes(correct)
        corrected_retry = patch("corrected-retry", old_bad, digest) if old_bad else None
        (source / "source.zip").unlink()
        missing_local = patch("missing-local", base, digest)
        (source / "source.zip").write_bytes(correct)
        scenarios = [
            ("import-with-publish-control", base), ("tool-correct-local", tool_correct),
            ("canonical-file-with-delta", canonical), ("canonical-file-without-delta", legacy),
            ("tool-stale-local", tool_stale), ("tool-corrected-retry", corrected_retry),
            ("tool-missing-local", missing_local),
            ("program-overrides-stale-local", program_override),
        ]
        for label, state in scenarios:
            if state is None:
                row = {"scenario": label, "patch_rejected": True}
                report["scenarios"].append(row)
                save(root / "report.json", report)
                print(json.dumps(row), flush=True)
                continue
            state_path = root / f"{label}-state.json"
            save(state_path, state)
            pulumi("stack", "import", "--file", str(state_path), "--stack", stack)
            plan = json.loads(pulumi("preview", "--json", "--show-sames", "--stack", stack))
            save(root / f"{label}-preview.json", plan)
            live = aws_lambda.get_function(FunctionName=name)["Configuration"]
            fingerprint = {key: live[key] for key in report["original"]}
            assert fingerprint == report["original"], "deployed Lambda changed during read-only reproduction"
            changes = [{"op": step["op"], "type": (step.get("newState") or step.get("oldState") or {}).get("type"), "diff": step.get("detailedDiff")} for step in plan["steps"] if step["op"] != "same"]
            row = {"scenario": label, "changes": changes, "cloud_unchanged": True}
            report["scenarios"].append(row)
            save(root / "report.json", report)
            print(json.dumps(row), flush=True)
        if args.expect_fixed:
            (source / "source.zip").write_bytes(stale)
            pulumi("stack", "import", "--file", str(root / "controlled-state.json"), "--stack", stack)
            stack_command = ["patch-state", "tf", "--digest", str(root / "correct-local-digest.json"),
                "--fields", str(Path(__file__).resolve().parents[1] / "data/aws-import-diff-fields.json"),
                "--mapping-file", str(root / "mappings.yaml"), "--config-dir", str(source),
                "--project-dir", str(project), "--stack", stack, "--backup-dir", str(root / "stack-backups"), "--skip-refresh-report"]
            invoke(str(args.tool.resolve()), stack_command)
            plan = json.loads(pulumi("preview", "--json", "--show-sames", "--stack", stack))
            save(root / "stack-mode-preview.json", plan)
            live = aws_lambda.get_function(FunctionName=name)["Configuration"]
            assert {key: live[key] for key in report["original"]} == report["original"]
            row = {"scenario": "stack-mode-program-overrides-stale-local", "changes": [s["op"] for s in plan["steps"] if s["op"] != "same"], "cloud_unchanged": True}
            report["scenarios"].append(row)
            print(json.dumps(row), flush=True)

            # V3/V4: change only a local artifact between baseline and verifying
            # previews. The tool must reject the new operation and restore state.
            before_rollback = json.loads(pulumi("stack", "export", "--show-secrets", "--stack", stack))
            (project / "stale.zip").write_bytes(stale)
            adversarial_prefix = (
                'import * as fs from "fs";\n'
                'const count = fs.existsSync("preview-count") ? Number(fs.readFileSync("preview-count", "utf8")) : 0;\n'
                'fs.writeFileSync("preview-count", String(count + 1));\n'
                'if (count === 1) { fs.copyFileSync("stale.zip", "program.zip"); }\n'
            )
            (project / "index.ts").write_text(adversarial_prefix + program)
            rejected = False
            try:
                invoke(str(args.tool.resolve()), stack_command)
            except RuntimeError:
                diagnostic = json.loads((root / f"command-{counter:02}.json").read_text())
                assert "revert" in (diagnostic["stdout"] + diagnostic["stderr"]).lower(), "unexpected failure instead of verified rollback"
                rejected = True
            finally:
                (project / "index.ts").write_text(program)
                (project / "program.zip").write_bytes(correct)
            assert rejected, "verification accepted an introduced artifact change"
            restored = json.loads(pulumi("stack", "export", "--show-secrets", "--stack", stack))
            for direction in ("inputs", "outputs"):
                assert function_resource(restored)[direction] == function_resource(before_rollback)[direction]
            live = aws_lambda.get_function(FunctionName=name)["Configuration"]
            assert {key: live[key] for key in report["original"]} == report["original"]
            row = {"scenario": "stack-verification-regression-rollback", "rejected": True, "state_restored": True, "cloud_unchanged": True}
            report["scenarios"].append(row)
            print(json.dumps(row), flush=True)
        if args.expect_fixed:
            rows = {row["scenario"]: row for row in report["scenarios"]}
            for label in ("tool-correct-local", "canonical-file-with-delta", "canonical-file-without-delta", "tool-corrected-retry", "tool-missing-local", "program-overrides-stale-local", "stack-mode-program-overrides-stale-local"):
                assert rows[label].get("changes") == [], f"{label} did not reach a clean preview"
            assert rows["tool-stale-local"].get("patch_rejected"), "stale local bytes were accepted"
    finally:
        if function_created:
            try:
                aws_lambda.delete_function(FunctionName=name)
                report["cleanup"]["function"] = "deleted"
            except Exception as error:
                report["cleanup"]["function"] = str(error)
        if role_created:
            try:
                iam.delete_role(RoleName=name)
                report["cleanup"]["role"] = "deleted"
            except Exception as error:
                report["cleanup"]["role"] = str(error)
        if stack_created:
            try:
                pulumi("stack", "rm", stack, "--force", "--yes")
                report["cleanup"]["stack"] = "removed"
            except Exception as error:
                report["cleanup"]["stack"] = str(error)
        save(root / "report.json", report)
        print(json.dumps({"cleanup": report["cleanup"], "report": str(root / "report.json")}), flush=True)
        if any(value not in ("deleted", "removed") for value in report["cleanup"].values()):
            raise RuntimeError("Fixture cleanup failed; inspect the report and remove the named resources")


if __name__ == "__main__":
    main()
