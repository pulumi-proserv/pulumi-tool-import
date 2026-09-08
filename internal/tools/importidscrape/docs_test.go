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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectDocs(t *testing.T) {
	docs, err := collectDocs(fixtureRoot(t))
	require.NoError(t, err)

	d := docs["aws_cloudwatch_event_target"]
	assert.Equal(t, "rule-name/target-id", d.Example)
	assert.Equal(t, "terraform-provider-aws/website/docs/r/cloudwatch_event_target.html.markdown", d.Source)
	assert.True(t, d.Divergent)

	assert.Equal(t, "domain_name", docs["aws_elasticsearch_domain"].Example)
	assert.True(t, docs["aws_elasticsearch_domain"].Divergent, "prose says 'using the `domain_name`'")

	// A single-segment example with no attribute prose is not divergent.
	assert.Equal(t, "plain-name", docs["aws_plain_thing"].Example)
	assert.False(t, docs["aws_plain_thing"].Divergent)

	// aws_s3_bucket's example is divergent; buildFormats suppresses it anyway
	// because an import step proves the type is passthrough.
	assert.Equal(t, "some-bucket/some-key", docs["aws_s3_bucket"].Example)
	assert.True(t, docs["aws_s3_bucket"].Divergent)

	assert.True(t, docs["aws_docs_only_thing"].Divergent)
	assert.Equal(t, "a/b", docs["aws_block_form_thing"].Example)
	assert.True(t, docs["aws_block_form_thing"].Divergent)
}
