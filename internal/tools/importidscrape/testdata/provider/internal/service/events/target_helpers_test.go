//go:build ignore

// The helper lives in a different file than the TestStep that names it, as
// many of the provider's do. Evidence must cite this file, not target_test.go.

package events_test

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func testAccTargetImportStateIdFunc(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("not found: %s", resourceName)
		}
		return fmt.Sprintf("%s/%s/%s", rs.Primary.Attributes["event_bus_name"], rs.Primary.Attributes["rule"], rs.Primary.Attributes["target_id"]), nil
	}
}
