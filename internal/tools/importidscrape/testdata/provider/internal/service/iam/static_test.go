//go:build ignore

package iam_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccIAMStatic_basic(t *testing.T) {
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{{ResourceName: "aws_iam_static_thing.test", ImportState: true, ImportStateId: "fixed-id"}},
	})
}

// The kinesis_stream shape: an explicit ImportStateId that is a variable, so
// the scraper cannot read the value. The step still says the import ID is NOT
// the state ID, which is the opposite of passthrough.
func TestAccIAMDynamicStatic_basic(t *testing.T) {
	rName := randomName()
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{{ResourceName: "aws_iam_dynamic_static_thing.test", ImportState: true, ImportStateId: rName}},
	})
}
