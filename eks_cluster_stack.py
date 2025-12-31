from aws_cdk import (
    Stack,
    aws_ec2 as ec2,
    aws_eks as eks,
    aws_iam as iam,
    CfnOutput,
    Tags,
    CfnResource,
    custom_resources as cr,
)
from constructs import Construct
from aws_cdk.lambda_layer_kubectl_v32 import KubectlV32Layer
from vpc_stack import VpcStack
import yaml
import os
from string import Template
from aws_cdk.aws_eks import CfnCluster
import json

class EKSClusterStack(Stack):
    def __init__(self, scope: Construct, construct_id: str, vpc_stack: VpcStack, event_rule_stack, **kwargs) -> None:
        super().__init__(scope, construct_id, **kwargs)

        # Access the account
        account = Stack.of(self).account

        # Access the region
        region = Stack.of(self).region

        # Reference the VPC from the VPC stack
        vpc = vpc_stack.vpc


        cluster_role = iam.Role(self, id="EksClusterRole",
                    assumed_by=iam.AccountRootPrincipal())

        self.cluster_sg = ec2.SecurityGroup(
                self, "ClusterSecurityGroup",
                vpc=vpc,
                description="EKS Cluster Security Group",
                allow_all_outbound=True,
                security_group_name="eks-cluster-sg"
            )

        # Create the EKS cluster
        self.cluster = eks.Cluster(
            self, "EksCluster",
            version=eks.KubernetesVersion.V1_32,
            vpc=vpc,
            vpc_subnets=[ec2.SubnetSelection(subnet_type=ec2.SubnetType.PRIVATE_WITH_EGRESS)],
            default_capacity=0,  # We'll define our own nodegroup
            cluster_name=f"{construct_id}-cluster",
            masters_role=cluster_role,
            endpoint_access=eks.EndpointAccess.PUBLIC_AND_PRIVATE,
            kubectl_layer=KubectlV32Layer(self, "KubectlLayer"),
            security_group=self.cluster_sg,
        )

        # Enable ZonalShiftConfig using Custom Resource 
        enable_zonal_shift = cr.AwsCustomResource(
            self, "EnableZonalShift",
            on_create=cr.AwsSdkCall(
                service="EKS",
                action="updateClusterConfig",
                parameters={
                    "name": self.cluster.cluster_name,
                    "zonalShiftConfig": {"enabled": True}
                },
                physical_resource_id=cr.PhysicalResourceId.of(f"{self.cluster.cluster_name}-zonal-shift")
            ),
            policy=cr.AwsCustomResourcePolicy.from_statements([
                iam.PolicyStatement(
                    actions=["eks:UpdateClusterConfig"],
                    resources=[self.cluster.cluster_arn]
                )
            ])
        )
        enable_zonal_shift.node.add_dependency(self.cluster)
        #print("\n✓ Custom Resource created to enable ZonalShiftConfig post-deployment")
        

        
        # Create a managed node group with 2 m5.large instances
        
        nodegroup = self.cluster.add_nodegroup_capacity(
            "DefaultNodegroup",
            instance_types=[ec2.InstanceType("m5.large")],
            min_size=2,
            max_size=2,
            desired_size=2,
            disk_size=50,  # 50 GB disk size
            ami_type=eks.NodegroupAmiType.AL2_X86_64,  # Amazon Linux 2
            capacity_type=eks.CapacityType.ON_DEMAND,
            subnets=ec2.SubnetSelection(subnet_type=ec2.SubnetType.PRIVATE_WITH_EGRESS)
        )

        # Create Karpenter node role here
        self.karpenter_node_role = iam.Role(
            self, "KarpenterNodeRole",
            assumed_by=iam.ServicePrincipal("ec2.amazonaws.com"),
            managed_policies=[
                iam.ManagedPolicy.from_aws_managed_policy_name("AmazonEKSWorkerNodePolicy"),
                iam.ManagedPolicy.from_aws_managed_policy_name("AmazonEKS_CNI_Policy"),
                iam.ManagedPolicy.from_aws_managed_policy_name("AmazonEC2ContainerRegistryReadOnly"),
                iam.ManagedPolicy.from_aws_managed_policy_name("AmazonSSMManagedInstanceCore"),
            ]
        )

         # Create Security Group for Karpenter Nodes
        self.karpenter_node_sg = ec2.SecurityGroup(
            self,
            "KarpenterNodeSecurityGroup",
            vpc=vpc,
            description="Security group for Karpenter managed nodes",
            security_group_name="karpenter-node-sg",
            allow_all_outbound=True,
        )

        # Add tags required for Karpenter
        Tags.of(self.karpenter_node_sg).add(
            "karpenter.sh/discovery", self.cluster.cluster_name
        )
        
        # Allow inbound traffic from the cluster security group
        self.karpenter_node_sg.add_ingress_rule(
            peer=ec2.Peer.any_ipv4(),
            connection=ec2.Port.all_traffic(),
            description="Allow all inbound traffic"
        )

        self.cluster_sg.add_ingress_rule(
            peer=self.karpenter_node_sg,
            connection=ec2.Port.all_traffic(),
            description="Allow inbound traffic from Karpenter nodes"
        )

        # Create Karpenter controller policy
        karpenter_controller_policy = iam.Policy(
            self, "KarpenterControllerPolicy",
            statements=[
                iam.PolicyStatement(
                    actions=[
                        "ec2:CreateFleet",
                        "ec2:CreateLaunchTemplate",
                        "ec2:CreateTags",
                        "ec2:DescribeAvailabilityZones",
                        "ec2:DescribeInstanceTypeOfferings",
                        "ec2:DescribeInstanceTypes",
                        "ec2:DescribeInstances",
                        "ec2:DescribeLaunchTemplates",
                        "ec2:DescribeSecurityGroups",
                        "ec2:DescribeSpotPriceHistory",
                        "ec2:DescribeSubnets",
                        "ec2:RunInstances",
                        "ec2:DescribeImages",
                        "ec2:TerminateInstances",
                        "pricing:GetProducts",
                        "ssm:GetParameter",
                        "iam:GetInstanceProfile",
                        "eks:DescribeCluster",
                        "iam:CreateInstanceProfile",
                        "iam:DeleteInstanceProfile",
                        "iam:AddRoleToInstanceProfile",
                        "iam:RemoveRoleFromInstanceProfile",
                        "iam:TagInstanceProfile",
                        "ec2:DeleteLaunchTemplate",
                        "ec2:DescribeSubnets",
                        "ec2:RunInstances",
                        "ec2:DescribeImages",
                        "ec2:TerminateInstances",
                        "pricing:GetProducts",
                        "ssm:GetParameter",
                        "iam:GetInstanceProfile",
                        "eks:DescribeCluster",
                        "iam:CreateInstanceProfile",
                        "iam:DeleteInstanceProfile",
                        "iam:AddRoleToInstanceProfile",
                        "iam:RemoveRoleFromInstanceProfile",
                        "iam:TagInstanceProfile",
                        "ec2:DeleteLaunchTemplate"
                    ],
                    resources=["*"]
                ),
                iam.PolicyStatement(
                    actions=["iam:PassRole"],
                    resources=[self.karpenter_node_role.role_arn]
                )
            ]
        )

        # Create Karpenter service account
        karpenter_service_account = self.cluster.add_service_account(
            "karpenter",
            name="karpenter",
            namespace="kube-system"
        )

        # Attach the Karpenter controller policy to the service account
        karpenter_controller_policy.attach_to_role(karpenter_service_account.role)

        # Add IRSA mapping for the node role
        self.cluster.aws_auth.add_role_mapping(
            self.karpenter_node_role,
            groups=["system:bootstrappers", "system:nodes"],
            username="system:node:{{EC2PrivateDNSName}}"
        )

        # Create SQS subscriber service account
        karpenter_sqs_service_account = self.cluster.add_service_account(
            "karpenter-sqs-subscriber",
            name="karpenter-sqs-subscriber",
            namespace="default"
        )

        karpenter_sqs_service_account.role.add_managed_policy(
            iam.ManagedPolicy.from_managed_policy_arn(
                self,
                "EC2ReadOnlyPolicy",
                managed_policy_arn="arn:aws:iam::aws:policy/AmazonEC2ReadOnlyAccess"
            )
         )

        # Add EKS and SQS permissions for the SQS subscriber
        eks_sqs_policy = iam.Policy(
            self, "EKSSQSPolicy",
            statements=[
                iam.PolicyStatement(
                    actions=[
                        "eks:ListNodegroups",
                        "eks:DescribeCluster",
                        "eks:DescribeNodegroup"
                    ],
                    resources=[f"arn:aws:eks:{region}:{account}:cluster/*"]
                ),
                iam.PolicyStatement(
                    actions=[
                        "sqs:ReceiveMessage",
                        "sqs:DeleteMessage",
                        "sqs:GetQueueAttributes"
                    ],
                    resources=[event_rule_stack.sqs_queue_arn]
                )
            ]
        )
        eks_sqs_policy.attach_to_role(karpenter_sqs_service_account.role)

        instance_profile = iam.CfnInstanceProfile(
            self, "KarpenterNodeInstanceProfile",
            roles=[self.karpenter_node_role.role_name],
            instance_profile_name=f"{self.cluster.cluster_name}-KarpenterNodeInstanceProfile"
        )
        instance_profile.node.add_dependency(self.karpenter_node_role)

         # Install Karpenter using Helm
        karpenter_chart = self.cluster.add_helm_chart(
            "karpenter",
            chart="karpenter",
            repository="oci://public.ecr.aws/karpenter/karpenter",
            version="1.3.0",
            namespace="kube-system",
            release="karpenter",
            values={
                "serviceAccount": {
                    "create": False,
                    "name": "karpenter"
                },
                "settings": {
                    "aws": {
                        "clusterName": self.cluster.cluster_name,
                        "clusterEndpoint": self.cluster.cluster_endpoint,
                        "defaultInstanceProfile": instance_profile.ref,
                        "interruptionQueueName": f"{self.cluster.cluster_name}-karpenter"
                    }
                },
                "crds": {
                    "create": True
                },
                "controller": {
                    "env": [
                        {
                            "name": "CLUSTER_NAME",
                            "value": self.cluster.cluster_name  # Add this explicit environment variable
                        },
                        {
                            "name": "CLUSTER_ENDPOINT",
                             "value": self.cluster.cluster_endpoint
                         }
                    ],
                    "resources": {
                        "requests": {
                            "cpu": "1",
                            "memory": "1Gi"
                        },
                        "limits": {
                            "cpu": "1",
                            "memory": "1Gi"
                        }
                    }
                }
            }
        )

        # Create a default NodePool for Karpenter
        default_nodepool_manifest = {
            "apiVersion": "karpenter.sh/v1",
            "kind": "NodePool",
            "metadata": {
                "name": "default"
            },
            "spec": {
                "template": {
                    "metadata": {
                        "labels": {
                            "zonal-autoshift-karpenter": "karpenter"
                        }
                    },
                    "spec": {
                        "nodeClassRef": {
                            "group": "karpenter.k8s.aws",
                            "kind": "EC2NodeClass",
                            "name": "default"
                        },
                        "requirements": [
                            {
                                "key": "kubernetes.io/arch",
                                "operator": "In",
                                "values": ["amd64"]
                            },
                            {
                                "key": "kubernetes.io/os",
                                "operator": "In",
                                "values": ["linux"]
                            },
                            {
                                "key": "topology.kubernetes.io/zone",
                                "operator": "In",
                                "values": [
                                            f"{region}a",
                                            f"{region}b",
                                            f"{region}c"
                                        ]
                            },
                            {
                                "key": "karpenter.sh/capacity-type",
                                "operator": "In",
                                "values": ["on-demand"]
                            },
                            {
                                "key": "node.kubernetes.io/instance-type",
                                "operator": "In",
                                "values": [
                                    "m5.large",
                                    "m5.xlarge",
                                    "m5.2xlarge"
                                ]
                            }
                        ]
                    }
                },
                "limits": {
                    "cpu": "10",
                    "memory": "100Gi"
                },
                "disruption": {
                    "consolidationPolicy": "WhenEmptyOrUnderutilized",
                    "consolidateAfter": "30s"
                }
            }
        }

        # Create EC2NodeClass manifest
        ec2nodeclass_manifest = {
            "apiVersion": "karpenter.k8s.aws/v1",
            "kind": "EC2NodeClass",
            "metadata": {
                "name": "default"
            },
            "spec": {
                #"amiFamily": "AL2",  # Amazon Linux 2
                "amiSelectorTerms": [
                    {
                        "alias": "al2023@latest"
                    }
                ],
                "role": self.karpenter_node_role.role_name,  # Reference to the IAM role
                "subnetSelectorTerms": [
                    {
                        "tags": {
                            "karpenter.sh/discovery": self.cluster.cluster_name
                        }
                    }
                ],
                "securityGroupSelectorTerms": [
                    {
                        "tags": {
                            "karpenter.sh/discovery": self.cluster.cluster_name
                        }
                    }
                ],

                "tags": {
                    "NodeType": "karpenter-node",
                    "karpenter.sh/discovery": self.cluster.cluster_name
                }
            }
        }


        # Add a wait manifest to ensure CRDs are ready
        wait_manifest = {
            "apiVersion": "batch/v1",
            "kind": "Job",
            "metadata": {
                "name": "wait-for-crds",
                "namespace": "kube-system"
            },
            "spec": {
                "template": {
                    "spec": {
                        "serviceAccountName": "karpenter",
                        "containers": [{
                            "name": "wait",
                            "image": "bitnami/kubectl",
                            "command": ["/bin/sh", "-c", "until kubectl get crd ec2nodeclasses.karpenter.k8s.aws; do sleep 2; done"]
                        }],
                        "restartPolicy": "Never"
                    }
                }
            }
        }
    


        wait_job = self.cluster.add_manifest("wait-for-crds", wait_manifest)

        # Apply the NodePool manifests
        ec2nodeclass = self.cluster.add_manifest("default-ec2nodeclass", ec2nodeclass_manifest)
        default_nodepool = self.cluster.add_manifest("default-nodepool", default_nodepool_manifest)
        

        # Ensure proper dependency chain
        default_nodepool.node.add_dependency(wait_job)
        ec2nodeclass.node.add_dependency(wait_job)


        #Read the deployment config
        # Service account is created by CDK with IRSA, skip manual creation
        # with open("./app/serviceaccount.yaml", 'r') as stream:
        #      serviceaccount_yaml = yaml.load(stream, Loader=yaml.FullLoader)

        with open("./app/clusterrole.yaml", 'r', encoding='utf-8') as stream:
             clusterrole_yaml = yaml.safe_load(stream)

        with open("./app/rolebinding.yaml", 'r', encoding='utf-8') as stream:
             rolebinding_yaml = yaml.safe_load(stream)

        with open("./app/leader-election-rbac.yaml", 'r', encoding='utf-8') as stream:
             leader_election_rbac_yaml = list(yaml.safe_load_all(stream))

        with open("./app/deployment.yaml", 'r', encoding='utf-8') as stream:
            template = Template(stream.read())
            variables = {
                'AWS_ACCOUNT': account,
                'AWS_REGION': region,
                'CLUSTER_NAME': self.cluster.cluster_name,
                'SQS_QUEUE_URL': event_rule_stack.sqs_queue_url,
             }
        
            # Substitute the variables
            content = template.safe_substitute(variables)
        
            # Load the YAML with substituted values
            dep_yaml = yaml.safe_load(content)



        # Service account already created by CDK with IRSA
        # self.cluster.add_manifest(f"{construct_id}-app-serviceaccount", serviceaccount_yaml)

        self.cluster.add_manifest(f"{construct_id}-app-clusterrole", clusterrole_yaml)

        self.cluster.add_manifest(f"{construct_id}-app-rolebinding", rolebinding_yaml)

        # Apply leader election RBAC manifests
        for i, manifest in enumerate(leader_election_rbac_yaml):
            self.cluster.add_manifest(f"{construct_id}-leader-election-rbac-{i}", manifest)

        # Ensure service account is created before deployment
        dep_yaml_with_dependency = self.cluster.add_manifest(f"{construct_id}-app-deployment", dep_yaml)
        dep_yaml_with_dependency.node.add_dependency(karpenter_sqs_service_account)



        # Output the cluster name
        CfnOutput(
            self, "ClusterName",
            value=self.cluster.cluster_name,
            description="EKS Cluster Name"
        )

        # Output the kubectl role ARN
        CfnOutput(
            self, "KubectlRoleArn",
            value=self.cluster.kubectl_role.role_arn,
            description="IAM Role ARN for kubectl"
        )

        # Output the OIDC provider ARN
        CfnOutput(
            self, "OidcProviderArn",
            value=self.cluster.open_id_connect_provider.open_id_connect_provider_arn,
            description="OIDC Provider ARN"
        )

        # Output the nodegroup ARN
        CfnOutput(
            self, "NodegroupArn",
            value=nodegroup.nodegroup_arn,
            description="EKS Nodegroup ARN"
        )

        CfnOutput(
            self, "KubeconfigCommand",
            value=f"aws eks --region {Stack.of(self).region} update-kubeconfig --name {self.cluster.cluster_name} --role-arn {cluster_role.role_arn}",
            description="Command to update kubeconfig"
        )
