//go:build ignore

package ec2_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccEC2Parent_basic(t *testing.T) {
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      "aws_ec2_child_thing.test",
				ImportState:       true,
				ImportStateIdFunc: testAccParentImportStateIdFunc,
			},
		},
	})
}

func testAccParentImportStateIdFunc(s *terraform.State) (string, error) {
	rs := s.RootModule().Resources["aws_vpc.test"]
	return rs.Primary.ID + "/x", nil
}
