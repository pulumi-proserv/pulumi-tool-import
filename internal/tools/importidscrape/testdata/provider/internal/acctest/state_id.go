//go:build ignore

// This file is a verbatim copy of terraform-provider-aws v6.38.0
// internal/acctest/state_id.go. The scraper never parses it: acctestTemplate
// in classify.go hard-codes these helpers' semantics. The copy exists so a
// reviewer can check those hard-coded templates against the bodies they claim
// to model, and so a future version bump can be diffed against it.

package acctest

import (
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	tfslices "github.com/hashicorp/terraform-provider-aws/internal/slices"
	"github.com/hashicorp/terraform-provider-aws/names"
)

// AttrImportStateIdFunc is a resource.ImportStateIdFunc that returns the value of the specified attribute
func AttrImportStateIdFunc(resourceName, attrName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("Not found: %s", resourceName)
		}

		return rs.Primary.Attributes[attrName], nil
	}
}

// AttrsImportStateIdFunc is a resource.ImportStateIdFunc that returns the values of the specified attributes concatenated with a separator
func AttrsImportStateIdFunc(resourceName, sep string, attrNames ...string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("Not found: %s", resourceName)
		}

		return strings.Join(tfslices.ApplyToAll(attrNames, func(attrName string) string {
			return rs.Primary.Attributes[attrName]
		}), sep), nil
	}
}

// CrossRegionAttrImportStateIdFunc is a resource.ImportStateIdFunc that returns the value
// of the specified attribute and appends the region
func CrossRegionAttrImportStateIdFunc(resourceName, attrName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("Not found: %s", resourceName)
		}

		id := rs.Primary.Attributes[attrName]
		region, ok := rs.Primary.Attributes[names.AttrRegion]
		if !ok {
			return "", fmt.Errorf("Attribute \"region\" not found in %s", resourceName)
		}

		return id + "@" + region, nil
	}
}

// CrossRegionAttrsImportStateIdFunc is a resource.ImportStateIdFunc that returns the values
// of the specified attributes concatenated with a separator and appends the region
//
// NOT recognized by the scraper: it is outside the whitelist Task 9 fixed.
func CrossRegionAttrsImportStateIdFunc(resourceName, sep string, attrNames ...string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("Not found: %s", resourceName)
		}

		id := strings.Join(tfslices.ApplyToAll(attrNames, func(attrName string) string {
			return rs.Primary.Attributes[attrName]
		}), sep)

		region, ok := rs.Primary.Attributes[names.AttrRegion]
		if !ok {
			return "", fmt.Errorf("Attribute \"region\" not found in %s", resourceName)
		}

		return id + "@" + region, nil
	}
}

// CrossRegionImportStateIdFunc is a resource.ImportStateIdFunc that appends the region
func CrossRegionImportStateIdFunc(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("Not found: %s", resourceName)
		}

		id := rs.Primary.ID
		region, ok := rs.Primary.Attributes[names.AttrRegion]
		if !ok {
			return "", fmt.Errorf("Attribute \"region\" not found in %s", resourceName)
		}

		return id + "@" + region, nil
	}
}

// CrossRegionImportStateIdFuncAdapter adapts an ImportStateIdFunc by appending the region
func CrossRegionImportStateIdFuncAdapter(resourceName string, f Func) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("Not found: %s", resourceName)
		}

		id, err := f(resourceName)(s)
		if err != nil {
			return "", err
		}

		region, ok := rs.Primary.Attributes[names.AttrRegion]
		if !ok {
			return "", fmt.Errorf("Attribute \"region\" not found in %s", resourceName)
		}

		return id + "@" + region, nil
	}
}

type Func func(resourceName string) resource.ImportStateIdFunc
