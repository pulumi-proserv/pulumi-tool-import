//go:build ignore

package events_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccEventsTarget_basic(t *testing.T) {
	resourceName := "aws_cloudwatch_event_target.test"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{Config: "..."},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateIdFunc: testAccTargetImportStateIdFunc(resourceName),
				ImportStateVerify: true,
			},
		},
	})
}

func testAccTargetImportStateIdFunc(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("not found: %s", resourceName)
		}
		return fmt.Sprintf("%s/%s/%s", rs.Primary.Attributes["event_bus_name"], rs.Primary.Attributes["rule"], rs.Primary.Attributes["target_id"]), nil
	}
}
