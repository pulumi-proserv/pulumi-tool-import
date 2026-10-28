//go:build ignore

package acc_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	// Same package name, different package: the helper semantics the
	// classifier hard-codes belong to the provider's own internal/acctest,
	// so this step must not be trusted.
	acctest "example.com/other/acctest"
)

func TestAccAcc_shadow(t *testing.T) {
	resourceName := "aws_acc_shadow.test"
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateIdFunc: acctest.AttrImportStateIdFunc(resourceName, "x"),
			},
		},
	})
}
