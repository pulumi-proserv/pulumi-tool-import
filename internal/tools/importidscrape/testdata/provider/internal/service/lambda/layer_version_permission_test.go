//go:build ignore

package lambda_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccLambdaLayerVersionPermission_basic(t *testing.T) {
	resourceName := "aws_lambda_layer_version_permission.test"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{{
			ResourceName:      resourceName,
			ImportState:       true,
			ImportStateIdFunc: testAccLayerVersionPermissionImportStateIdFunc(resourceName),
		}},
	})
}

func testAccLayerVersionPermissionImportStateIdFunc(resourceName string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs := s.RootModule().Resources[resourceName]
		parts := strings.Split(rs.Primary.Attributes["layer_version_arn"], ":")
		return fmt.Sprintf("%s,%s", strings.Join(parts[:len(parts)-1], ":"), parts[len(parts)-1]), nil
	}
}
