//go:build ignore

package events

import "github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"

// @SDKResource("aws_cloudwatch_event_target", name="Target")
func resourceTarget() *schema.Resource {
	return &schema.Resource{
		Schema: map[string]*schema.Schema{
			"event_bus_name": {Type: schema.TypeString, Optional: true},
			"rule":           {Type: schema.TypeString, Required: true},
			"target_id":      {Type: schema.TypeString, Optional: true, Sensitive: true},
		},
	}
}
