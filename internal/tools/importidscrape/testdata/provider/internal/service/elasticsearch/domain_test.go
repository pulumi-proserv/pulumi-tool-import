//go:build ignore

package elasticsearch_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccElasticsearchDomain_basic(t *testing.T) {
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      "aws_elasticsearch_domain.test",
				ImportState:       true,
				ImportStateIdFunc: testAccDomainImportStateID,
			},
		},
	})
}

func testAccDomainImportStateID(s *terraform.State) (string, error) {
	rs := s.RootModule().Resources["aws_elasticsearch_domain.test"]
	return rs.Primary.Attributes["domain_name"] + "/" + rs.Primary.ID, nil
}
