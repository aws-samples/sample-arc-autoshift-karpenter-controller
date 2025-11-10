from aws_cdk.aws_ecr_assets import DockerImageAsset, Platform
import cdk_ecr_deployment as ecrdeploy

from aws_cdk import (
    Stack,
    NestedStack,
    aws_ecr as ecr,
    Aws
)
from constructs import Construct

class DockerImageStack(Stack):

    def __init__(self, scope: Construct, construct_id: str, **kwargs) -> None:
        super().__init__(scope, construct_id, **kwargs)

        # Create ECR Repositories 
        ZonalAutoShiftKarpenterRepo = ecr.Repository(self,"ZonalAutoShiftKarpenterRepo", repository_name="zonalautoshiftkarpenter")
        ZonalAutoShiftKarpenterAsset = DockerImageAsset(self, "ZonalAutoShiftKarpenterAsset",
            directory="./app",
            build_args={},
            platform=Platform.LINUX_AMD64
        )
      
        #Deploying images to ECR
        ecrdeploy.ECRDeployment(self, "ZonalAutoShiftKarpenterImage",
            src=ecrdeploy.DockerImageName(ZonalAutoShiftKarpenterAsset.image_uri),
            dest=ecrdeploy.DockerImageName(f"{Aws.ACCOUNT_ID}.dkr.ecr.{Aws.REGION}.amazonaws.com/zonalautoshiftkarpenter:latest")
        )
