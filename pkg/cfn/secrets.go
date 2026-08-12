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

import (
	"sort"
	"strings"

	"github.com/pulumi-proserv/pulumi-tool-import/pkg"
)

// sensitiveProps maps a CFN resource type to properties whose literal values are
// secrets. Curated from the common inline-secret cases.
var sensitiveProps = map[string][]string{
	"AWS::SecretsManager::Secret":        {"SecretString"},
	"AWS::RDS::DBInstance":               {"MasterUserPassword"},
	"AWS::RDS::DBCluster":                {"MasterUserPassword"},
	"AWS::Redshift::Cluster":             {"MasterUserPassword"},
	"AWS::ElastiCache::ReplicationGroup": {"AuthToken"},
	"AWS::DirectoryService::MicrosoftAD": {"Password"},
	"AWS::DirectoryService::SimpleAD":    {"Password"},
	"AWS::Amplify::App":                  {"AccessToken", "OauthToken", "BasicAuthCredentials"},
	"AWS::DocDB::DBCluster":              {"MasterUserPassword"},
	"AWS::Neptune::DBCluster":            {"MasterUserPassword"},
}

// sensitiveNameHints: a property whose name contains any of these substrings
// (case-insensitive) is treated as a secret regardless of resource type.
// Deliberately conservative — e.g. "apikey" is excluded because API Gateway
// has many non-secret ApiKey* flags (ApiKeyRequired, ApiKeySourceType); real
// API-key values are curated per-type instead.
var sensitiveNameHints = []string{
	"password", "secretstring", "privatekey", "authtoken", "accesstoken", "oauthtoken",
}

func isSensitiveProp(cfnType, prop string) bool {
	for _, p := range sensitiveProps[cfnType] {
		if p == prop {
			return true
		}
	}
	lp := strings.ToLower(prop)
	for _, h := range sensitiveNameHints {
		if strings.Contains(lp, h) {
			return true
		}
	}
	return false
}

// ExtractSecrets scans the digest for sensitive literal property values,
// **redacts them in place** ("(sensitive)"), and returns the config entries to
// set as encrypted stack-config secrets (via pkg.SetSecretsFromState). Skipped
// resources, unresolved-intrinsic markers, empty strings, and non-string values
// are left untouched (nothing sensitive to extract). Entries are sorted by key
// for deterministic output.
func ExtractSecrets(d *StackDigest) []pkg.ConfigEntry {
	var entries []pkg.ConfigEntry
	for i := range d.Resources {
		r := &d.Resources[i]
		if r.Skipped || r.Attributes == nil {
			continue
		}
		for prop, v := range r.Attributes {
			if !isSensitiveProp(r.CfnType, prop) {
				continue
			}
			s, ok := v.(string)
			if !ok || s == "" || s == "(sensitive)" || strings.HasPrefix(s, "<unresolved-intrinsic:") {
				continue
			}
			entries = append(entries, pkg.ConfigEntry{
				ConfigKey: cfnConfigKey(r.LogicalID, prop),
				Value:     s,
				Secret:    true,
			})
			r.Attributes[prop] = "(sensitive)"
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ConfigKey < entries[j].ConfigKey })
	return entries
}

// cfnConfigKey derives a stack-config key for a sensitive resource property.
func cfnConfigKey(logicalID, prop string) string {
	return strings.ToLower(logicalID) + "_" + strings.ToLower(prop)
}
