//go:build ignore

package wafv2_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/hashicorp/terraform-provider-aws/names"
)

// The attribute keys here are names.Attr* constants rather than string
// literals, which is how most of the provider now spells them.
func TestAccWAFV2IPSet_basic(t *testing.T) {
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      "aws_wafv2_ip_set.test",
				ImportState:       true,
				ImportStateIdFunc: testAccIPSetImportStateIdFunc("aws_wafv2_ip_set.test"),
			},
		},
	})
}

func testAccIPSetImportStateIdFunc(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("Not found: %s", resourceName)
		}

		return fmt.Sprintf("%s/%s/%s", rs.Primary.ID, rs.Primary.Attributes[names.AttrName], rs.Primary.Attributes[names.AttrScope]), nil
	}
}
