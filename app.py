#!/usr/bin/env python3
import os

import aws_cdk as cdk

from cdk_nag import AwsSolutionsChecks
from aws_cdk import Aspects
from cdk_nag import NagSuppressions


from docker_image_stack import DockerImageStack
from vpc_stack import VpcStack
from eks_cluster_stack import EKSClusterStack
from event_rule_stack import EventRuleStack

#AWS Checks
#from cdk_nag import AwsSolutionsChecks # added
#from aws_cdk import Aspects # added


# Get account ID and region from environment variables
account_id = os.environ.get('CDK_DEFAULT_ACCOUNT')
region_id = os.environ.get('CDK_DEFAULT_REGION')

if not account_id or not region_id:
    raise ValueError("CDK_DEFAULT_ACCOUNT and CDK_DEFAULT_REGION environment variables must be set")

app = cdk.App()

#spects.of(app).add(AwsSolutionsChecks(verbose=True)) # added

Aspects.of(app).add(AwsSolutionsChecks(verbose=True)) # added

docker_image_stack = DockerImageStack(app, "DockerImageStack",env=cdk.Environment(account=account_id, region=region_id),)
event_rule_stack = EventRuleStack(app, "EventRuleStack", env=cdk.Environment(account=account_id, region=region_id),)
vpc_stack = VpcStack(app, "VpcStack",env=cdk.Environment(account=account_id, region=region_id),)
eks_cluster_stack = EKSClusterStack(app, "EKSClusterStack",env=cdk.Environment(account=account_id, region=region_id),vpc_stack=vpc_stack, event_rule_stack=event_rule_stack)

NagSuppressions.add_stack_suppressions(stack=docker_image_stack, suppressions=[
    {"id": "AwsSolutions-IAM4", "reason": "CDK managed Lambda execution role"},
    {"id": "AwsSolutions-IAM5", "reason": "CDK managed Lambda permissions"}], apply_to_nested_stacks=True)

NagSuppressions.add_stack_suppressions(stack=event_rule_stack, suppressions=[
    {"id": "AwsSolutions-IAM4","reason": "ipsum lorum"},
    {"id": "AwsSolutions-L1","reason": "ipsum lorum"},
    {"id": "AwsSolutions-IAM5","reason": "ipsum lorum"}], apply_to_nested_stacks=True)
   

NagSuppressions.add_stack_suppressions(stack=vpc_stack, suppressions=[
    {"id": "AwsSolutions-IAM4","reason": "ipsum lorum"},
    {"id": "AwsSolutions-L1","reason": "ipsum lorum"},
    {"id": "AwsSolutions-IAM5","reason": "ipsum lorum"},
    {"id": "AwsSolutions-VPC7", "reason": "VPC Flow Logs not required for demo"}], apply_to_nested_stacks=True)

NagSuppressions.add_stack_suppressions(stack=eks_cluster_stack, suppressions=[
    {"id": "AwsSolutions-IAM4","reason": "ipsum lorum"},
    {"id": "AwsSolutions-L1","reason": "ipsum lorum"},
    {"id": "AwsSolutions-IAM5","reason": "ipsum lorum"},
    {"id": "AwsSolutions-EKS1", "reason": "Public access required for demo"},
    {"id": "AwsSolutions-EKS2", "appliesTo": ["LogExport::api", "LogExport::audit", "LogExport::authenticator", "LogExport::controllerManager", "LogExport::scheduler"], "reason": "Control plane logs not required for demo"},
    {"id": "AwsSolutions-SF1", "reason": "Step Function logging not required"},
    {"id": "AwsSolutions-SF2", "reason": "X-Ray tracing not required"},
    {"id": "AwsSolutions-EC23", "reason": "Open security group required for Karpenter nodes"}], apply_to_nested_stacks=True)


# Add dependencies to ensure proper deployment order
eks_cluster_stack.add_dependency(vpc_stack)
eks_cluster_stack.add_dependency(docker_image_stack)
eks_cluster_stack.add_dependency(event_rule_stack)

app.synth()
