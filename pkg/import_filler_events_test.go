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

package pkg

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTranslateEventTargetAlternatives(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"v6.83.4", "v7.48.0", "v7.51.1"} {
		for _, tc := range []struct{ bus, want string }{
			{"", "daily/lambda"},
			{"default", "daily/lambda"},
			{"prod", "prod/daily/lambda"},
			{"aws.partner/example.com/bus", "aws.partner/example.com/bus/daily/lambda"},
			{"arn:aws:events:us-west-2:123456789012:event-bus/prod", "arn:aws:events:us-west-2:123456789012:event-bus/prod/daily/lambda"},
		} {
			t.Run(version+"/"+tc.bus, func(t *testing.T) {
				digest := &ModuleMap{
					Providers: map[string]string{"hashicorp/aws": "aws@" + version},
					RootResources: []ModuleResource{{
						Mode: "managed", TerraformAddress: "aws_cloudwatch_event_target.target", ImportID: "daily-lambda",
						Attributes: map[string]interface{}{"event_bus_name": tc.bus, "rule": "daily", "target_id": "lambda"},
					}},
				}
				file := &ImportFile{Resources: []ImportEntry{{Type: "aws:cloudwatch/eventTarget:EventTarget", Name: "target", ID: "daily-lambda"}}}
				result := TranslateImportIDsForProviders(file, digest, nil)
				assert.Equal(t, tc.want, file.Resources[0].ID)
				assert.Equal(t, 1, result.Translated)
				assert.Zero(t, result.Unverified)
				if version != "v7.51.1" {
					assert.Empty(t, result.Notes)
				}
			})
		}
	}
}
