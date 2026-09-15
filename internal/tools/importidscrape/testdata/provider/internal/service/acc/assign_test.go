//go:build ignore

package acc_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/hashicorp/terraform-provider-aws/internal/acctest"
	"github.com/hashicorp/terraform-provider-aws/names"
)

// The appautoscaling_target shape: the joined ID is bound to a local and the
// local is returned. Still a pure join of this resource's own attributes.
func TestAccAcc_assign(t *testing.T) {
	resourceName := "aws_acc_assign.test"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateIdFunc: testAccAssignImportStateIdFunc(resourceName),
			},
		},
	})
}

func testAccAssignImportStateIdFunc(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("Not found: %s", resourceName)
		}

		id := fmt.Sprintf("%s/%s",
			rs.Primary.Attributes["service_namespace"],
			rs.Primary.Attributes[names.AttrName])
		return id, nil
	}
}

// Negative: the returned identifier has two assignments. Each RHS on its own
// is a provable template, so only the single-binding rule keeps this manual.
func TestAccAcc_assignTwice(t *testing.T) {
	resourceName := "aws_acc_assigntwice.test"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateIdFunc: testAccAssignTwiceImportStateIdFunc(resourceName),
			},
		},
	})
}

func testAccAssignTwiceImportStateIdFunc(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return "", fmt.Errorf("Not found: %s", resourceName)
		}

		id := rs.Primary.Attributes["first"]
		id = rs.Primary.Attributes["second"]
		return id, nil
	}
}

// The appautoscaling_policy shape: the helper returns an acctest helper call
// rather than a closure literal, passing its own parameter through.
func TestAccAcc_indirect(t *testing.T) {
	resourceName := "aws_acc_indirect.test"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateIdFunc: testAccIndirectImportStateIdFunc(resourceName),
			},
		},
	})
}

func testAccIndirectImportStateIdFunc(resourceName string) resource.ImportStateIdFunc {
	return acctest.AttrsImportStateIdFunc(resourceName, "/", "group", names.AttrName)
}

// Negative: the helper passes an address that is not the step's own resource.
func TestAccAcc_indirectOther(t *testing.T) {
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      "aws_acc_indirectother.test",
				ImportState:       true,
				ImportStateIdFunc: testAccIndirectOtherImportStateIdFunc("aws_acc_indirectother.test"),
			},
		},
	})
}

func testAccIndirectOtherImportStateIdFunc(resourceName string) resource.ImportStateIdFunc {
	return acctest.AttrImportStateIdFunc("aws_vpc.test", names.AttrName)
}

// Negative: the helper returns a call that is not an acctest helper.
func TestAccAcc_indirectForeign(t *testing.T) {
	resourceName := "aws_acc_indirectforeign.test"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateIdFunc: testAccIndirectForeignImportStateIdFunc(resourceName),
			},
		},
	})
}

func testAccIndirectForeignImportStateIdFunc(resourceName string) resource.ImportStateIdFunc {
	return localIDFunc(resourceName, "/", "group", "name")
}

func localIDFunc(resourceName, sep string, attrs ...string) resource.ImportStateIdFunc {
	return acctest.AttrsImportStateIdFunc(resourceName, sep, attrs...)
}

// Negative: this file imports the provider's real internal/acctest, but the
// helper is defined in indirect_helpers_test.go, where "acctest" is bound to
// a foreign package. The import check must follow the helper's file.
func TestAccAcc_indirectShadow(t *testing.T) {
	resourceName := "aws_acc_indirectshadow.test"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateIdFunc: testAccIndirectShadowImportStateIdFunc(resourceName),
			},
		},
	})
}
