# Security Policy

## Reporting a vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Report suspected vulnerabilities privately via GitHub's
[private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability)
("Report a vulnerability" under the repository's **Security** tab), or by
emailing the Pulumi security team at **security@pulumi.com**.

Please include enough detail to reproduce the issue: affected command/version,
steps, and impact. We will acknowledge your report and keep you informed of
remediation progress.

## Scope and handling notes

This tool reads Terraform/OpenTofu state and CloudFormation stacks, which
frequently contain secrets. Take care to treat generated artifacts (for example
the `tf-digest` / `digest cfn` JSON sidecars) as sensitive — they may embed
secret values in non-sensitive fields and should be `.gitignore`d, not
committed. See the README for per-command redaction caveats.

## Supported versions

This is an actively developed tool; fixes land on the latest release. Please
reproduce reports against the most recent tagged version where possible.
