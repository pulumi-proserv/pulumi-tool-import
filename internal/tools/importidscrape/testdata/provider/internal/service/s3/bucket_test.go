//go:build ignore

package s3_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccS3Bucket_basic(t *testing.T) {
	resource.ParallelTest(t, resource.TestCase{
		Steps: []resource.TestStep{{ResourceName: "aws_s3_bucket.test", ImportState: true, ImportStateVerify: true}},
	})
}
