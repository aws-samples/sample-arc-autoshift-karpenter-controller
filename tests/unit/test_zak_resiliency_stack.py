import aws_cdk as core
import aws_cdk.assertions as assertions

from zak_resiliency.zak_resiliency_stack import ZakResiliencyStack

# example tests. To run these tests, uncomment this file along with the example
# resource in zak_resiliency/zak_resiliency_stack.py
def test_sqs_queue_created():
    app = core.App()
    stack = ZakResiliencyStack(app, "zak-resiliency")
    template = assertions.Template.from_stack(stack)

#     template.has_resource_properties("AWS::SQS::Queue", {
#         "VisibilityTimeout": 300
#     })
