//go:build ignore

package dynamodb_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccDynamoDBTable_basic(t *testing.T) {
	name := "aws_dynamodb_table.test"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      name,
				ImportState:       true,
				ImportStateIdFunc: testAccTableImportStateIdFunc(name),
			},
		},
	})
}

// testAccTableImportStateIdFunc names its parameter "id" rather than the
// usual "resourceName", to confirm classify does not rely on that name.
func testAccTableImportStateIdFunc(id string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[id]
		if !ok {
			return "", fmt.Errorf("not found: %s", id)
		}
		return fmt.Sprintf("%s", rs.Primary.Attributes["name"]), nil
	}
}
