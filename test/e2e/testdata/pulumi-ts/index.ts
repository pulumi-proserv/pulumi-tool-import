// Pulumi program mirroring test/e2e/testdata/tf/main.tf, for the
// patch-state-tf-non-importable end-to-end test (test/e2e/e2e_test.go).
//
// This is a TypeScript port of testdata/pulumi/Pulumi.yaml. Both programs
// declare the same resources under the same logical names and generate
// identical import files; the port exists because Pulumi YAML cannot declare
// a ComponentResource, which blocks the component-parent coverage gap
// (gap 7 in docs/superpowers/plans/2026-08-14-remaining-test-coverage.md).
//
// LOGICAL NAMES ARE THE CONTRACT. "resolve tf" matches a sidecar entry to an
// import-file entry by the resource's logical (URN) name: for a resource with
// no parent component, matchChildren (pkg/import_filler.go) matches on
// extractResourceName(terraformAddress), which is the last dot-separated
// segment of the Terraform address kept as-is -- brackets included for a
// count-indexed resource ("aws_route_table.rt[0]" -> "rt[0]") and quotes
// included for a for_each-keyed one ('aws_iot_certificate.each["alpha"]' ->
// 'each["alpha"]'). See pkg/import_filler_test.go:TestExtractResourceName.
//
// The YAML fixture needed a workaround for those names: its expression parser
// reads "rt[0]" as indexing a list-valued variable, so each bracket-named
// resource there uses an interpolation-safe map key and carries its real name
// in a separate "name:" field. TypeScript needs no such workaround -- the
// logical name is just the first constructor argument, and the variable it is
// assigned to is independent of it.

import * as pulumi from "@pulumi/pulumi";
import * as aws from "@pulumi/aws";

const tags = {
    Name: "tool-import-e2e",
    ManagedBy: "pulumi-tool-import-e2e",
    Purpose: "issue-22-non-importable-injection",
};

const main = new aws.ec2.Vpc("main", {
    cidrBlock: "10.42.0.0/16",
    enableDnsHostnames: true,
    tags: tags,
});

// count-indexed in Terraform, so the logical names carry brackets.
const routeTables = [0, 1, 2].map(i => new aws.ec2.RouteTable(`rt[${i}]`, {
    vpcId: main.id,
    tags: {
        Name: `tool-import-e2e-rt-${i}`,
        ManagedBy: "pulumi-tool-import-e2e",
        Purpose: "issue-22-non-importable-injection",
    },
}));

// amazonSideAsn and cgw's bgpAsn are declared as strings here where the YAML
// fixture writes bare numbers. The provider's schema type is string in
// aws v7, so YAML was relying on coercion; TypeScript's checker rejects it.
// Both programs send the same value to the provider -- verified by comparing
// the previews' resource inputs, not just their import files.
const vgw = new aws.ec2.VpnGateway("vgw", {
    vpcId: main.id,
    amazonSideAsn: "64512",
    tags: tags,
});

// Non-importable: "resolve tf" drops these three from the generated import
// file and writes them to the *.non-importable.json sidecar instead. Before
// injection they must preview as "create"; after
// "patch-state tf --non-importable" they must preview as "same".
const props = routeTables.map((rt, i) => new aws.ec2.VpnGatewayRoutePropagation(`prop[${i}]`, {
    vpnGatewayId: vgw.id,
    routeTableId: rt.id,
}));

const cgw = new aws.ec2.CustomerGateway("cgw", {
    bgpAsn: "65000",
    ipAddress: "203.0.113.1",
    type: "ipsec.1",
    tags: tags,
});

const vpn = new aws.ec2.VpnConnection("vpn", {
    vpnGatewayId: vgw.id,
    customerGatewayId: cgw.id,
    type: "ipsec.1",
    staticRoutesOnly: true,
    tags: tags,
});

// Non-importable: the second sidecar entry (see prop[0..2] above).
const route = new aws.ec2.VpnConnectionRoute("route", {
    destinationCidrBlock: "10.99.0.0/16",
    vpnConnectionId: vpn.id,
});

// Non-importable, and the only resource in this program with Sensitive
// properties (privateKey/certificatePem/publicKey are secret outputs; caPem
// is a secret INPUT and is deliberately absent -- see below).
//
// This must mirror testdata/tf/main.tf's "aws_iot_certificate" exactly, which
// sets only "active". caPem was set here to a placeholder string until the
// e2e run of 2026-08-15, which is when this first became reachable: caPem is
// ForceNew, so a program declaring it against injected state that correctly
// has none made the certificate preview as "replace" and reverted the run.
// That was the program disagreeing with the Terraform config it is supposed
// to be a translation of, not a tool defect -- "pulumi up" really would have
// replaced the certificate.
const cert = new aws.iot.Certificate("cert", {
    active: true,
});

// Backs the LAMBDA-type target group below (see testdata/tf/main.tf for why
// LAMBDA rather than IP: an IP target with nothing listening behind it never
// becomes healthy, timing out "tofu apply"). Both are importable and matched
// by Terraform address (aws_iam_role.lambda, aws_lambda_function.target), so
// they are imported like main/rt*/vgw/cgw/vpn/tg above -- their real,
// account-specific ARNs are never written into this file, only referenced.
const lambdaRole = new aws.iam.Role("lambda", {
    assumeRolePolicy: `{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Service": "lambda.amazonaws.com"},
    "Action": "sts:AssumeRole"
  }]
}
`,
    tags: tags,
});

// "code" and "sourceCodeHash" mirror testdata/tf/main.tf's "filename" and
// "source_code_hash". They are load-bearing, not decoration: "pulumi import"
// cannot write a Lambda's code into state, because AWS returns a presigned
// download URL rather than the bytes. That is exactly what the curated fields
// file exists for -- data/aws-import-diff-fields.json marks
// aws:lambda/function:Function's "code" as "not_read" and reconstructs it as
// a FileArchive hashed by "source_code_hash" -- so "patch-state" writes a
// code archive into state. A program declaring none would then disagree with
// the state the tool just patched, which is what made this Lambda the second
// failure in the e2e run of 2026-08-15.
//
// lambda.zip here is byte-identical to what main.tf's "archive_file" data
// source generates: the archive provider zeroes entry timestamps, so its
// output is deterministic (verified by generating it twice). To regenerate
// after changing the handler source, run that data source alone and copy its
// output_path here, keeping "sourceCodeHash" in step with its
// output_base64sha256.
const lambdaFn = new aws.lambda.Function("target", {
    role: lambdaRole.arn,
    handler: "index.handler",
    runtime: "nodejs20.x",
    code: new pulumi.asset.FileArchive("./lambda.zip"),
    sourceCodeHash: "kwwAvkbUDySp/SXW6Hk7mMS1/G44XcqJwswP2+6lQUU=",
    tags: tags,
});

const tg = new aws.vpclattice.TargetGroup("tg", {
    type: "LAMBDA",
    tags: tags,
});

// --- A second provider instance, in a different region.
//
// This is what makes the provider-reference resolution observable. The
// sidecar cannot carry a provider reference (the uuid exists only in the
// target stack), so injection takes it from the preview's create step. If it
// took the wrong one, "east" would be injected against us-west-2, where that
// certificate does not exist, and the preview could not report "same".
const eastProvider = new aws.Provider("eastProvider", {
    region: "us-east-1",
});

// Non-importable, under the aliased provider (aws.east in main.tf).
const eastCert = new aws.iot.Certificate("east", {
    active: true,
}, { provider: eastProvider });

// --- Importable; the policy the attachment below refers to.
const iotPolicy = new aws.iot.Policy("policy", {
    name: "tool-import-e2e-policy",
    policy: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["iot:Connect"],"Resource":["*"]}]}
`,
});

// Non-importable AND dependent on another non-importable resource: "target"
// is the certificate's ARN, and "cert" is itself injected. This is the edge
// orderInjected's topological sort exists for -- VerifyIntegrity rejects a
// resource whose dependency appears later in the deployment array, so if the
// sort got this wrong the run would fail loudly rather than subtly.
const policyAttach = new aws.iot.PolicyAttachment("policy_attach", {
    policy: iotPolicy.name,
    target: cert.arn,
});

// --- for_each rather than count. The logical names carry QUOTES inside the
// brackets ('each["alpha"]'), a shape the count-indexed names above do not
// have. In TypeScript these are ordinary strings needing no escaping beyond
// the quotes themselves.
const eachCerts = ["alpha", "beta"].map(k => new aws.iot.Certificate(`each["${k}"]`, {
    active: true,
}));

// Non-importable, and the only resource in this program with a nested
// list-of-objects property ("target": {id, port}) -- every other injected
// resource here is flat top-level strings. "port" is omitted -- it is not
// applicable to a Lambda target, matching testdata/tf/main.tf.
const attach = new aws.vpclattice.TargetGroupAttachment("attach", {
    targetGroupIdentifier: tg.id,
    target: {
        id: lambdaFn.arn,
    },
});

// --- A component parent, mirroring testdata/tf/modules/certs.
//
// This is the reason the fixture is TypeScript rather than YAML: Pulumi YAML
// cannot declare a ComponentResource, so a component parent was unreachable
// while the fixture was YAML.
//
// What it exercises that nothing else here does: the child's URN gains a
// qualified type ("<component type>$aws:iot/certificate:Certificate"), and its
// import entry carries a "parent". Injection takes the parent from the
// preview's create step, and VerifyIntegrity only WARNS -- it does not error
// -- when a child's URN does not agree with its parent's, so a wrong parent
// would not be caught by the integrity check the way a wrong provider
// reference was. The preview is what has to catch it.
//
// "resolve tf" needs "--map module.certs=certs" to connect the Terraform
// module to this component; see provisionStack in e2e_test.go.
class Certs extends pulumi.ComponentResource {
    constructor(name: string, opts?: pulumi.ComponentResourceOptions) {
        super("toolimport:index:Certs", name, {}, opts);

        // Non-importable, and the only resource in this program whose parent
        // is not the stack root.
        new aws.iot.Certificate("inmodule", { active: true }, { parent: this });

        this.registerOutputs({});
    }
}

const certs = new Certs("certs");
