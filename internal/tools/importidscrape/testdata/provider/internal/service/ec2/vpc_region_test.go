//go:build ignore

package ec2_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccEC2RegionThing_basic(t *testing.T) {
	resourceName := "aws_ec2_regionthing.test"
	region := "us-west-2"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{{
			ResourceName:      resourceName,
			ImportState:       true,
			ImportStateIdFunc: testAccRegionThingImportStateIdFunc(resourceName, region),
		}},
	})
}

// testAccRegionThingImportStateIdFunc applies Terraform's <id>@<region>
// region-override syntax on top of a plain passthrough ID: the classifier
// must drop the "@region" suffix and prove {id}, the same as it does for
// acctest.CrossRegionImportStateIdFunc.
func testAccRegionThingImportStateIdFunc(resourceName, region string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs := s.RootModule().Resources[resourceName]
		return fmt.Sprintf("%s@%s", rs.Primary.ID, region), nil
	}
}

func TestAccEC2RegionThingOtherIdent_basic(t *testing.T) {
	resourceName := "aws_ec2_regionthing_other.test"
	other := "us-west-2"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{{
			ResourceName:      resourceName,
			ImportState:       true,
			ImportStateIdFunc: testAccRegionThingOtherImportStateIdFunc(resourceName, other),
		}},
	})
}

// testAccRegionThingOtherImportStateIdFunc uses the same "%s@%s" shape but
// the second argument is not the identifier "region" — the classifier must
// not recognize this as the region-override syntax and must fall back to
// manual.
func testAccRegionThingOtherImportStateIdFunc(resourceName, other string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs := s.RootModule().Resources[resourceName]
		return fmt.Sprintf("%s@%s", rs.Primary.ID, other), nil
	}
}

func TestAccEC2RegionThingHashSep_basic(t *testing.T) {
	resourceName := "aws_ec2_regionthing_hash.test"
	region := "us-west-2"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{{
			ResourceName:      resourceName,
			ImportState:       true,
			ImportStateIdFunc: testAccRegionThingHashImportStateIdFunc(resourceName, region),
		}},
	})
}

// testAccRegionThingHashImportStateIdFunc uses a different separator
// ("%s#%s") with a "region" second argument — only the exact "%s@%s" shape
// is recognized, so this must fall back to manual.
func testAccRegionThingHashImportStateIdFunc(resourceName, region string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs := s.RootModule().Resources[resourceName]
		return fmt.Sprintf("%s#%s", rs.Primary.ID, region), nil
	}
}
