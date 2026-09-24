//go:build ignore

package events_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
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
