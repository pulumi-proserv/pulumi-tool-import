//go:build ignore

package acc_test

import (
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	// Same package name, different package. The helper below is called from
	// assign_test.go, which imports the provider's real internal/acctest, so
	// classify must check the acctest import in THIS file — the one the
	// helper is written in — not in the file the TestStep is written in.
	acctest "example.com/other/acctest"

	"github.com/hashicorp/terraform-provider-aws/names"
)

func testAccIndirectShadowImportStateIdFunc(resourceName string) resource.ImportStateIdFunc {
	return acctest.AttrsImportStateIdFunc(resourceName, "/", "group", names.AttrName)
}
