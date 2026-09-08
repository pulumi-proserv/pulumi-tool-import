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
