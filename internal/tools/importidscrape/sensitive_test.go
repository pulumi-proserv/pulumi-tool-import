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
)

func TestSensitiveAttrs(t *testing.T) {
	root := fixtureRoot(t)
	assert.Equal(t, []string{"target_id"}, sensitiveAttrs(root, "aws_cloudwatch_event_target", "{event_bus_name}/{rule}/{target_id}"))
	assert.Nil(t, sensitiveAttrs(root, "aws_cloudwatch_event_target", "{rule}"))
	assert.Nil(t, sensitiveAttrs(root, "aws_no_such_type", "{x}"))
}
