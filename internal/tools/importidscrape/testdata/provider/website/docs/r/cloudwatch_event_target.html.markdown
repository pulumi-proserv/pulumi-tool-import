# Resource: aws_cloudwatch_event_target

## Import

In Terraform v1.5.0 and later, use an `import` block:

```terraform
import {
  to = aws_cloudwatch_event_target.test-event-target
  id = "rule-name/target-id"
}
```

Using `terraform import`:

```console
% terraform import aws_cloudwatch_event_target.test-event-target rule-name/target-id
```
