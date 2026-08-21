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

package pkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
)

// SecretMapping maps a Pulumi config key to a Terraform state resource attribute.
// Format: "configKey=terraformAddress:attribute"
type SecretMapping struct {
	ConfigKey        string
	TerraformAddress string
	Attribute        string
}

// ParseSecretMapping parses a mapping string of the form "configKey=terraformAddress:attribute".
func ParseSecretMapping(s string) (SecretMapping, error) {
	eqIdx := strings.Index(s, "=")
	if eqIdx < 0 {
		return SecretMapping{}, fmt.Errorf("invalid mapping %q: expected format configKey=terraformAddress:attribute", s)
	}

	configKey := s[:eqIdx]
	rest := s[eqIdx+1:]

	// Split on the last ":" to separate address from attribute,
	// since terraform addresses can contain ":" in rare cases.
	colonIdx := strings.LastIndex(rest, ":")
	if colonIdx < 0 {
		return SecretMapping{}, fmt.Errorf("invalid mapping %q: expected format configKey=terraformAddress:attribute", s)
	}

	return SecretMapping{
		ConfigKey:        configKey,
		TerraformAddress: rest[:colonIdx],
		Attribute:        rest[colonIdx+1:],
	}, nil
}

// SetSecrets reads secret values from a Terraform state file and sets them
// as encrypted secrets in a Pulumi stack config.
//
// It initializes the stack if it doesn't exist.
func SetSecrets(stateFilePath, projectDir, projectName, stack, runtime string, mappings []SecretMapping) error {
	// Read and parse the state file.
	data, err := os.ReadFile(stateFilePath)
	if err != nil {
		return fmt.Errorf("reading state file: %w", err)
	}

	configMap, err := extractSecretValues(data, mappings)
	if err != nil {
		return err
	}

	// Ensure a Pulumi project exists before stack operations.
	if err := ensurePulumiProject(projectDir, projectName, runtime); err != nil {
		return err
	}

	if err := writeConfigValues(projectDir, stack, configMap); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Set %d secrets on stack %s\n", len(mappings), stack)
	return nil
}

// extractSecretValues pulls each mapping's attribute value out of a Terraform
// state file and returns it as a secret config entry. Decoded with UseNumber:
// these values become the stack's secrets, and a plain decode turns a large
// integer into a float64 that %v renders in scientific notation — a wrong
// secret, not a cosmetic difference (the DiscoverSensitiveSecrets bug, in its
// other home).
func extractSecretValues(data []byte, mappings []SecretMapping) (auto.ConfigMap, error) {
	var stateFile struct {
		Resources []struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			Module    string `json:"module"`
			Mode      string `json:"mode"`
			Instances []struct {
				IndexKey   interface{}            `json:"index_key"`
				Attributes map[string]interface{} `json:"attributes"`
			} `json:"instances"`
		} `json:"resources"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&stateFile); err != nil {
		return nil, fmt.Errorf("parsing state file: %w", err)
	}

	// Build a lookup map: terraform address -> attributes.
	// Addresses look like "aws_s3_bucket.example" or
	// "module.foo.aws_ssm_parameter.bar[\"key\"]"
	attrsByAddress := make(map[string]map[string]interface{})
	for _, res := range stateFile.Resources {
		for _, inst := range res.Instances {
			// Build the full address.
			addr := ""
			if res.Module != "" {
				addr = res.Module + "."
			}
			if res.Mode == "data" {
				addr += "data."
			}
			addr += res.Type + "." + res.Name
			if inst.IndexKey != nil {
				switch key := inst.IndexKey.(type) {
				case string:
					addr += fmt.Sprintf("[%q]", key)
				case json.Number:
					addr += fmt.Sprintf("[%s]", key.String())
				case float64:
					addr += fmt.Sprintf("[%d]", int(key))
				}
			}
			attrsByAddress[addr] = inst.Attributes
		}
	}

	// Extract secret values and build config map.
	configMap := make(auto.ConfigMap, len(mappings))
	for _, m := range mappings {
		attrs, ok := attrsByAddress[m.TerraformAddress]
		if !ok {
			return nil, fmt.Errorf("terraform address %q not found in state", m.TerraformAddress)
		}

		value, ok := attrs[m.Attribute]
		if !ok {
			return nil, fmt.Errorf("attribute %q not found on resource %q", m.Attribute, m.TerraformAddress)
		}

		fmt.Fprintf(os.Stderr, "  Mapping secret %s from %s:%s\n", m.ConfigKey, m.TerraformAddress, m.Attribute)
		configMap[m.ConfigKey] = auto.ConfigValue{Value: fmt.Sprintf("%v", value), Secret: true}
	}
	return configMap, nil
}
