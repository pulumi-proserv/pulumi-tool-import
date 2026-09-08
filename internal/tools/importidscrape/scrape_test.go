// Copyright 2016-2026, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixtureRoot(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("testdata/provider")
	require.NoError(t, err)
	return p
}

func stepsByType(t *testing.T) map[string]ImportStep {
	t.Helper()
	steps, err := collectImportSteps(fixtureRoot(t))
	require.NoError(t, err)
	out := map[string]ImportStep{}
	for _, s := range steps {
		out[s.TFType] = s
	}
	return out
}

// classify2 classifies a step against the fixture provider's names package.
func classify2(t *testing.T, step ImportStep) Classification {
	t.Helper()
	consts, err := loadNameConsts(fixtureRoot(t))
	require.NoError(t, err)
	return classify(step, consts)
}

func TestLoadNameConsts(t *testing.T) {
	consts, err := loadNameConsts(fixtureRoot(t))
	require.NoError(t, err)
	assert.Equal(t, "name", consts["AttrName"])
	assert.Equal(t, "scope", consts["AttrScope"])
	assert.NotContains(t, consts, "AttrNope")
}

// Most of the provider spells attribute keys as names.Attr* constants rather
// than literals; without resolving them the classifier proves no template.
func TestClassifyResolvesNamesConstants(t *testing.T) {
	c := classify2(t, stepsByType(t)["aws_wafv2_ip_set"])
	assert.Equal(t, "{id}/{name}/{scope}", c.Template)
	assert.False(t, c.Manual)

	// An unresolvable constant must stay manual rather than emit a bogus key.
	c = classify(stepsByType(t)["aws_wafv2_ip_set"], nil)
	assert.True(t, c.Manual)
	assert.Empty(t, c.Template)
}

// acctestTemplate hard-codes four helpers' semantics. The hash is what makes
// that safe across a provider bump, so it must match the checked-in copy of
// the helpers — which is therefore proven byte-faithful to the tag.
func TestAcctestHelperHashPinsTheFixture(t *testing.T) {
	got, err := hashAcctestHelpers(fixtureRoot(t))
	require.NoError(t, err)
	assert.Equal(t, acctestHelpersSHA256, got)
	require.NoError(t, checkAcctestHelpers(fixtureRoot(t)))
}

// A changed helper body must stop the scrape, not be absorbed silently.
func TestAcctestHelperHashRejectsAMutatedHelper(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "acctest")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	src, err := os.ReadFile(filepath.Join(fixtureRoot(t), "internal", "acctest", "state_id.go"))
	require.NoError(t, err)
	// Change the separator AttrsImportStateIdFunc joins with: same shape,
	// different composed ID.
	mutated := bytes.Replace(src, []byte(`}), sep), nil`), []byte(`}), sep+"!"), nil`), 1)
	require.NotEqual(t, string(src), string(mutated))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state_id.go"), mutated, 0o644))

	err = checkAcctestHelpers(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state_id.go")
	assert.Contains(t, err.Error(), "AttrsImportStateIdFunc")
	assert.Contains(t, err.Error(), acctestHelpersSHA256)
}

func TestCollectImportSteps(t *testing.T) {
	by := stepsByType(t)
	assert.Contains(t, by, "aws_cloudwatch_event_target")
	assert.Contains(t, by, "aws_elasticsearch_domain")
	assert.Contains(t, by, "aws_s3_bucket") // passthrough steps are collected; classification drops them
	assert.Equal(t, "internal/service/events/target_test.go", by["aws_cloudwatch_event_target"].File)
	assert.NotNil(t, by["aws_cloudwatch_event_target"].IDFuncExpr)
	assert.Nil(t, by["aws_s3_bucket"].IDFuncExpr)
	assert.Equal(t, "fixed-id", by["aws_iam_static_thing"].StaticID)
}

func TestClassifyTemplates(t *testing.T) {
	by := stepsByType(t)

	c := classify2(t, by["aws_cloudwatch_event_target"])
	assert.Equal(t, "{event_bus_name}/{rule}/{target_id}", c.Template)
	assert.False(t, c.Manual)
	assert.Equal(t, "testAccTargetImportStateIdFunc", c.Symbol)

	c = classify2(t, by["aws_elasticsearch_domain"])
	assert.Equal(t, "{domain_name}/{id}", c.Template)
	assert.Equal(t, "testAccDomainImportStateID", c.Symbol)
}

func TestClassifyManual(t *testing.T) {
	by := stepsByType(t)

	c := classify2(t, by["aws_lambda_layer_version_permission"])
	assert.True(t, c.Manual)
	assert.Empty(t, c.Template)
	assert.Contains(t, c.Snippet, "strings.Split")

	c = classify2(t, by["aws_ec2_cross_thing"])
	assert.True(t, c.Manual)
	assert.Contains(t, c.Snippet, `Resources["aws_vpc.test"]`)

	c = classify2(t, by["aws_iam_static_thing"])
	assert.True(t, c.Manual)
	assert.Equal(t, `ImportStateId: "fixed-id"`, c.Snippet)

	// The closure's only s.RootModule().Resources[...] binding is a
	// different resource ("aws_vpc.test") than the step under test
	// ("aws_ec2_child_thing.test"); it must not be accepted as a receiver.
	c = classify2(t, by["aws_ec2_child_thing"])
	assert.True(t, c.Manual)
	assert.Empty(t, c.Template)
	assert.Contains(t, c.Snippet, `Resources["aws_vpc.test"]`)
}

func TestClassifyTemplateWithNamedParam(t *testing.T) {
	by := stepsByType(t)

	// The helper's parameter is named "id", not the usual "resourceName";
	// classify must still resolve it to the step's own address.
	c := classify2(t, by["aws_dynamodb_table"])
	assert.Equal(t, "{name}", c.Template)
	assert.False(t, c.Manual)
	assert.Equal(t, "testAccTableImportStateIdFunc", c.Symbol)
}

// The provider composes most import IDs with a handful of helpers in
// internal/acctest whose semantics are fixed; classify reads the call, not a
// body it can find in the test package.
func TestClassifyAcctestHelpers(t *testing.T) {
	by := stepsByType(t)
	assert.Equal(t, "{arn}", classify2(t, by["aws_acc_attr"]).Template)
	assert.Equal(t, "{group}:{name}", classify2(t, by["aws_acc_attrs"]).Template)
	assert.Equal(t, "{id}", classify2(t, by["aws_acc_cross"]).Template)
	assert.Equal(t, "{name}", classify2(t, by["aws_acc_crossattr"]).Template)
	assert.True(t, classify2(t, by["aws_acc_adapter"]).Manual)
	assert.True(t, classify2(t, by["aws_acc_shadow"]).Manual, "acctest bound to a foreign path must not be trusted")
	assert.True(t, classify2(t, by["aws_acc_otheraddr"]).Manual, "a helper reading another resource proves nothing")
	assert.True(t, classify2(t, by["aws_acc_dynattr"]).Manual, "a non-literal attribute name proves nothing")
}

// Two shapes the provider uses that are still pure joins of the step's own
// resource: a joined ID bound to a local and returned
// (aws_appautoscaling_target), and a helper that returns an acctest helper
// call instead of a closure literal (aws_appautoscaling_policy).
func TestClassifyIndirectShapes(t *testing.T) {
	by := stepsByType(t)

	c := classify2(t, by["aws_acc_assign"])
	assert.Equal(t, "{service_namespace}/{name}", c.Template)
	assert.False(t, c.Manual)

	c = classify2(t, by["aws_acc_indirect"])
	assert.Equal(t, "{group}/{name}", c.Template)
	assert.False(t, c.Manual)
	assert.Equal(t, "testAccIndirectImportStateIdFunc -> acctest.AttrsImportStateIdFunc", c.Symbol)
}

// The boundary of those two extensions.
func TestClassifyIndirectNegatives(t *testing.T) {
	by := stepsByType(t)

	// Each RHS alone would prove a template; two bindings must not.
	c := classify2(t, by["aws_acc_assigntwice"])
	assert.True(t, c.Manual)
	assert.Empty(t, c.Template)

	// The helper hands acctest a different resource's address.
	c = classify2(t, by["aws_acc_indirectother"])
	assert.True(t, c.Manual)
	assert.Empty(t, c.Template)

	// The helper returns a call that is not one of the acctest helpers whose
	// semantics acctestTemplate models.
	c = classify2(t, by["aws_acc_indirectforeign"])
	assert.True(t, c.Manual)
	assert.Empty(t, c.Template)
}

// Evidence must cite the file the helper is defined in. The step and the
// helper often live in different files of the same package, and citing the
// step's file with the helper's line points at an unrelated line — or past
// the end of the file.
func TestClassifyCitesHelperFile(t *testing.T) {
	c := classify2(t, stepsByType(t)["aws_cloudwatch_event_target"])
	assert.Equal(t, "internal/service/events/target_helpers_test.go", c.File)

	// With no resolved func there is nothing to cite but the step's own file.
	assert.Equal(t, "internal/service/iam/static_test.go", classify2(t, stepsByType(t)["aws_iam_static_thing"]).File)
}

func TestClassifyPassthroughIsNeither(t *testing.T) {
	c := classify2(t, stepsByType(t)["aws_s3_bucket"])
	assert.Empty(t, c.Template)
	assert.False(t, c.Manual)
}

// A step with an ImportStateId the scraper cannot read still says the import
// ID is not the state ID. Reading it as passthrough is the silent-wrong-value
// failure: aws_kinesis_stream imports by name via `ImportStateId: rName`
// while its state ID is the stream ARN (internal/service/kinesis/stream.go:215).
func TestClassifyUnreadableStaticIDIsManual(t *testing.T) {
	c := classify2(t, stepsByType(t)["aws_iam_dynamic_static_thing"])
	assert.True(t, c.Manual)
	assert.Empty(t, c.Template)
	assert.Equal(t, "ImportStateId: rName", c.Snippet)
}
