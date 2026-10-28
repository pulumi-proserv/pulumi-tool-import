//go:build ignore

package acc_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/hashicorp/terraform-provider-aws/internal/acctest"
	"github.com/hashicorp/terraform-provider-aws/names"
)

func TestAccAcc_attr(t *testing.T) {
	resourceName := "aws_acc_attr.test"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateIdFunc: acctest.AttrImportStateIdFunc(resourceName, names.AttrARN),
			},
		},
	})
}

func TestAccAcc_attrs(t *testing.T) {
	resourceName := "aws_acc_attrs.test"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateIdFunc: acctest.AttrsImportStateIdFunc(resourceName, ":", "group", names.AttrName),
			},
		},
	})
}

func TestAccAcc_cross(t *testing.T) {
	resourceName := "aws_acc_cross.test"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateIdFunc: acctest.CrossRegionImportStateIdFunc(resourceName),
			},
		},
	})
}

func TestAccAcc_crossAttr(t *testing.T) {
	resourceName := "aws_acc_crossattr.test"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateIdFunc: acctest.CrossRegionAttrImportStateIdFunc(resourceName, names.AttrName),
			},
		},
	})
}

func TestAccAcc_adapter(t *testing.T) {
	resourceName := "aws_acc_adapter.test"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateIdFunc: acctest.CrossRegionImportStateIdFuncAdapter(resourceName, testAccAccImportStateIDFunc),
			},
		},
	})
}

func testAccAccImportStateIDFunc(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs := s.RootModule().Resources[resourceName]
		return rs.Primary.Attributes["other"], nil
	}
}

// A wrong-address first argument must not be trusted: the helper reads a
// different resource's attributes than the step under test.
func TestAccAcc_otherAddress(t *testing.T) {
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      "aws_acc_otheraddr.test",
				ImportState:       true,
				ImportStateIdFunc: acctest.AttrImportStateIdFunc("aws_vpc.test", names.AttrName),
			},
		},
	})
}

// A non-literal attribute argument must stay manual.
func TestAccAcc_dynamicAttr(t *testing.T) {
	resourceName := "aws_acc_dynattr.test"
	attr := "computed"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateIdFunc: acctest.AttrImportStateIdFunc(resourceName, attr),
			},
		},
	})
}
