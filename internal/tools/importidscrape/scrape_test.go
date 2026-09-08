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

	c := classify(by["aws_cloudwatch_event_target"])
	assert.Equal(t, "{event_bus_name}/{rule}/{target_id}", c.Template)
	assert.False(t, c.Manual)
	assert.Equal(t, "testAccTargetImportStateIdFunc", c.Symbol)

	c = classify(by["aws_elasticsearch_domain"])
	assert.Equal(t, "{domain_name}/{id}", c.Template)
	assert.Equal(t, "testAccDomainImportStateID", c.Symbol)
}

func TestClassifyManual(t *testing.T) {
	by := stepsByType(t)

	c := classify(by["aws_lambda_layer_version_permission"])
	assert.True(t, c.Manual)
	assert.Empty(t, c.Template)
	assert.Contains(t, c.Snippet, "strings.Split")

	c = classify(by["aws_ec2_cross_thing"])
	assert.True(t, c.Manual)
	assert.Contains(t, c.Snippet, `Resources["aws_vpc.test"]`)

	c = classify(by["aws_iam_static_thing"])
	assert.True(t, c.Manual)
	assert.Equal(t, `ImportStateId: "fixed-id"`, c.Snippet)
}

func TestClassifyPassthroughIsNeither(t *testing.T) {
	c := classify(stepsByType(t)["aws_s3_bucket"])
	assert.Empty(t, c.Template)
	assert.False(t, c.Manual)
}
