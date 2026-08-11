# Provider choice & import-ID reference

How `resolve cfn` maps a CloudFormation resource to a Pulumi classic (or aws-native) type and composes its import ID, and how to extend it.

## Provider choice per resource type

- **Classic `@pulumi/aws` — default for everything** except the API Gateway family. Mature import for Lambda, IAM, S3, ECS, Secrets Manager, EC2/VPC, RDS, DynamoDB, SNS/SQS, CloudWatch, Route53, etc.
- **aws-native `@pulumi/aws-native` — the API Gateway family only:** `AWS::ApiGateway::{RestApi, Resource, Method, Deployment, Stage, Authorizer, Integration}` and the associated `AWS::Lambda::Permission` wiring. Reason: classic models a CFN Method as 4+ separate resources (method + integration + method-response + integration-response); aws-native is 1:1 with CloudFormation and imports by Cloud Control identifier. Set both `aws:region` and `aws-native:region`.

Mix providers in one program. Only switch to aws-native for the API Gateway nodes; keep everything else classic.

## How `resolve cfn` composes import IDs

The tool fills each import-skeleton entry by matching it to the digest on **CFN logical ID** (entry name suffix, or an explicit `mapping-file` entry), then:

1. **Pure composition** (most types) — builds the ID from resolved top-level attributes. Examples:
   - `aws:lambda/permission:Permission` → `FunctionName/Id`
   - `aws:apigateway/resource:Resource` → `RestApiId/Id` (native: `RestApiId|Id`)
   - `aws:apigateway/deployment:Deployment` → classic `RestApiId/Id`; **native reversed** `Id|RestApiId`
   - `aws:apigateway/method:Method` → `RestApiId/ResourceId/HttpMethod`
   - `aws:ecs/service:Service` → `cluster/name`; `aws:route53/record:Record` → `zone_Name_Type[_SetId]`
2. **Pre-resolved AWS-lookup types** (need a live AWS call, done at digest time): IAM managed-policy ARN, security-group rule id (`DescribeSecurityGroupRules`), EIP allocation id, VPC gateway attachment. The digest carries the resolved `importId`.
3. **Fallback** — the resource's `PhysicalID` (correct for e.g. S3 bucket name, Lambda function name, IAM role name, managed-policy ARN).

`resolve cfn` reports `Filled` / `Unmatched` and **warns** rather than emitting a wrong ID when an identifier can't be composed (a missing role or an unresolved intrinsic). An `Unmatched` with a warning means: author that resource's import ID by hand, or fix the skeleton.

## `--provider classic|native`

Run `resolve cfn` per node with the provider matching how you authored it. For an API Gateway node authored with aws-native, use `--provider native` (gets the native identifier order, including the reversed Deployment).

## Extending the resolver

Adding a new resource type = one entry in the tool's shared `pkg/importid` spec table (keyed on the Pulumi type token, with generic roles) plus the CFN role→property mapping in `pkg/cfn/adapter.go`. Custom composite/lookup cases live alongside. See the tool repo's design docs. When an import fails with **"resource does not exist"**, treat it as **wrong identifier format/order**, not a missing resource — verify against the type's documented import format (`pulumi package get-schema aws`, the `## Import` section).

## Known limitation: inline `AWS::IAM::Policy`

Not auto-resolved. CFN `AWS::IAM::Policy` is an **inline** policy (embedded in roles/users/groups), which maps to `aws:iam/rolePolicy:RolePolicy` with a `role:policy-name` import ID requiring a role-scoped `list-role-policies` lookup, and can attach to multiple principals. `digest cfn` leaves it unmapped (no `pulumiType`, no pre-resolved id). Author it by hand and set the import ID manually (`role-name:policy-name`). Managed policies (`AWS::IAM::ManagedPolicy` → `aws:iam/policy:Policy`) resolve fine via the ARN.
