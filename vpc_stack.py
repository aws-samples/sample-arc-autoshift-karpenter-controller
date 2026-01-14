from aws_cdk import (
    Stack,
    aws_ec2 as ec2,
    CfnOutput,
    Tags,
)
from constructs import Construct


class VpcStack(Stack):
    def __init__(self, scope: Construct, construct_id: str, **kwargs) -> None:
        super().__init__(scope, construct_id, **kwargs)

        # Create a VPC with 3 AZs, each with public and private subnets
        # NAT Gateway will be created in each AZ
        self.vpc = ec2.Vpc(
            self, "VPC",
            max_azs=3,
            cidr="10.0.0.0/16",
            # Configure subnet configuration
            subnet_configuration=[
                ec2.SubnetConfiguration(
                    name="Public",
                    subnet_type=ec2.SubnetType.PUBLIC,
                    cidr_mask=20
                ),
                ec2.SubnetConfiguration(
                    name="Private",
                    subnet_type=ec2.SubnetType.PRIVATE_WITH_NAT,
                    cidr_mask=20
                ),
            ],
            # Create a NAT Gateway in each AZ
            nat_gateways=3,
            # Enable DNS support
            enable_dns_support=True,
            enable_dns_hostnames=True,
        )

        # Tag all subnets for easier identification
        for subnet in self.vpc.public_subnets:
            Tags.of(subnet).add("Name", f"{construct_id}-Public-{subnet.availability_zone}")
            Tags.of(subnet).add("SubnetType", "Public")
            
        for subnet in self.vpc.private_subnets:
            Tags.of(subnet).add("Name", f"{construct_id}-Private-{subnet.availability_zone}")
            Tags.of(subnet).add("SubnetType", "Private")
            Tags.of(subnet).add("karpenter.sh/discovery", "EKSClusterStack-cluster")
            
        @property
        def get_vpc(self):
            return self.vpc

        # Output the VPC ID
        CfnOutput(
            self, "VpcId",
            value=self.vpc.vpc_id,
            description="VPC ID"
        )

        # Output the public subnet IDs
        CfnOutput(
            self, "PublicSubnets",
            value=",".join([subnet.subnet_id for subnet in self.vpc.public_subnets]),
            description="Public Subnet IDs"
        )

        # Output the private subnet IDs
        CfnOutput(
            self, "PrivateSubnets",
            value=",".join([subnet.subnet_id for subnet in self.vpc.private_subnets]),
            description="Private Subnet IDs"
        )
