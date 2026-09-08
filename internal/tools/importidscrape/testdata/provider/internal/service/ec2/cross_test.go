//go:build ignore

package ec2_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccEC2Cross_basic(t *testing.T) {
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{{ResourceName: "aws_ec2_cross_thing.test", ImportState: true, ImportStateIdFunc: crossID}},
	})
}

func crossID(s *terraform.State) (string, error) {
	rs := s.RootModule().Resources["aws_ec2_cross_thing.test"]
	other := s.RootModule().Resources["aws_vpc.test"]
	return other.Primary.ID + "/" + rs.Primary.ID, nil
}
