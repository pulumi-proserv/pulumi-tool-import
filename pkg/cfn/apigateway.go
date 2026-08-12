// Copyright 2016-2025, Pulumi Corporation.
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

package cfn

// nativeAPIGatewayImportID composes the aws-native (Cloud Control) import ID for
// an API Gateway family resource from its resolved attributes. The API Gateway
// family is the one place this migration uses aws-native (classic explodes it),
// and the native identifiers are composite and — for Deployment — in reversed
// order vs classic. Pre-resolved into the digest so `resolve cfn --provider
// native` can use it directly.
func nativeAPIGatewayImportID(cfnType string, attrs map[string]interface{}) (string, bool) {
	switch cfnType {
	case "AWS::ApiGateway::RestApi":
		return single(str(attrs, "Id")) // the RestApiId (physical id)
	case "AWS::ApiGateway::Resource":
		return joinPipe(str(attrs, "RestApiId"), str(attrs, "Id"))
	case "AWS::ApiGateway::Method":
		return joinPipe(str(attrs, "RestApiId"), str(attrs, "ResourceId"), str(attrs, "HttpMethod"))
	case "AWS::ApiGateway::Deployment":
		// Native identifier order is reversed vs classic: DeploymentId|RestApiId.
		return joinPipe(str(attrs, "Id"), str(attrs, "RestApiId"))
	case "AWS::ApiGateway::Stage":
		return joinPipe(str(attrs, "RestApiId"), str(attrs, "StageName"))
	case "AWS::ApiGateway::Authorizer":
		return joinPipe(str(attrs, "RestApiId"), str(attrs, "Id"))
	}
	return "", false
}

func single(v string) (string, bool) {
	if v == "" {
		return "", false
	}
	return v, true
}

func joinPipe(parts ...string) (string, bool) {
	out := ""
	for i, p := range parts {
		if p == "" {
			return "", false
		}
		if i > 0 {
			out += "|"
		}
		out += p
	}
	return out, true
}
