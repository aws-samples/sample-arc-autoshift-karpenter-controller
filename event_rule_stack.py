from aws_cdk import (
    Stack,
    aws_sqs as sqs,
    aws_events as events,
    aws_events_targets as targets,
    aws_iam as iam,
    Duration,
    CfnOutput,
)
from constructs import Construct

class EventRuleStack(Stack):
    def __init__(self, scope: Construct, construct_id: str, **kwargs) -> None:
        super().__init__(scope, construct_id, **kwargs)

        # Create DLQ
        dlq = sqs.Queue(
            self,
            "ZonalShiftDLQ",
            queue_name="zonal-shift-dlq"
        )

        # Add SSL enforcement policy to DLQ
        dlq_ssl_policy_statement = iam.PolicyStatement(
            effect=iam.Effect.DENY,
            principals=[iam.AnyPrincipal()],
            actions=["sqs:*"],
            resources=[dlq.queue_arn],
            conditions={
                "Bool": {
                    "aws:SecureTransport": "false"
                }
            }
        )
        dlq.add_to_resource_policy(dlq_ssl_policy_statement)

        # Create SQS Queue with DLQ
        zonal_shift_queue = sqs.Queue(
            self,
            "ZonalShiftQueue",
            queue_name="zonal-shift",
            visibility_timeout=Duration.seconds(300),
            dead_letter_queue=sqs.DeadLetterQueue(
                max_receive_count=3,
                queue=dlq
            )
        )

        # Add SSL enforcement policy to main queue
        ssl_policy_statement = iam.PolicyStatement(
            effect=iam.Effect.DENY,
            principals=[iam.AnyPrincipal()],
            actions=["sqs:*"],
            resources=[zonal_shift_queue.queue_arn],
            conditions={
                "Bool": {
                    "aws:SecureTransport": "false"
                }
            }
        )
        zonal_shift_queue.add_to_resource_policy(ssl_policy_statement)

        # Create EventBridge Rule
        event_pattern = events.EventPattern(
            source=["aws.arc-zonal-shift"],
            detail_type=[
                "Autoshift In Progress",
                "FIS Experiment Autoshift In Progress",
                "Autoshift Completed",
                "FIS Experiment Autoshift Canceled",
                "FIS Experiment Autoshift Completed",
                "Practice Run Started",
                "Practice Run Succeeded",
                "Practice Run Interrupted",
                "Practice Run Failed"
            ]
        )

        rule = events.Rule(
            self,
            "ZonalShiftRule",
            description="Rule to capture Zonal Shift events",
            event_pattern=event_pattern
        )

        # Add SQS Queue as target for the EventBridge Rule
        rule.add_target(
            targets.SqsQueue(
                zonal_shift_queue
            )
        )

        # Output the SQS Queue URL
        self.sqs_queue_url = zonal_shift_queue.queue_url
        self.sqs_queue_arn = zonal_shift_queue.queue_arn
        
        # Export the queue URL as a CloudFormation output
        CfnOutput(
            self,
            "SQSQueueURL",
            value=zonal_shift_queue.queue_url,
            description="SQS Queue URL for zonal shift events"
        )
