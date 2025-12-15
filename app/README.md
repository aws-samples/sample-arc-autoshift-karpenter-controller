# zonal-autoshift-karpenter
This is a concept showing how you can use zonal autoshift's integration with EventBridge to automatically remove zones from a Karpenter node pool when an autoshift is underway.

## High level setup instructions
1. Create an EventBridge rule for zonal shift, "Autoshift in progress" events.
2. Configure the rule to send autoshift events to an SNS topic.
3. Create an IAM role for the zonal-autoshift-karpenter pod to assume that allows it to subscribe to the topic. If using IRSA, add the roleArn to the pod's service account. If using Pod Identity, create a pod identity association. 
4. Apply the zonal-autoshift-karpenter deployment.yaml

## Do-Not-Disrupt Feature

The application supports an optional feature to label and cordon nodes in the impaired availability zone during an autoshift event. This prevents Karpenter from disrupting workloads on those nodes while they're in the impaired zone.

### How it works:
1. **During Autoshift**: When an autoshift event occurs, the application will:
   - Get all nodes in the impaired AZ
   - Apply the `karpenter.sh/do-not-disrupt=true` label to each node
   - Cordon the nodes (mark as unschedulable)
   - Then proceed with the normal nodepool modification

2. **After Autoshift**: When the autoshift completes, the application will:
   - Find all nodes with the `karpenter.sh/do-not-disrupt=true` label in the restored AZ
   - Remove the label
   - Uncordon the nodes (mark as schedulable again)
   - Then proceed with the normal nodepool restoration

### Enabling the feature:

To enable this feature, uncomment the `args` section in the `deployment.yaml`:

```yaml
containers:
  - name: karpenter-sqs-subscriber
    image: $AWS_ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com/zonalautoshiftkarpenter
    args:
      - "--enable-do-not-disrupt"
```

Alternatively, you can use the short form:
```yaml
args:
  - "-d"
```

By default, this feature is **disabled**. The application will only modify nodepools without touching individual nodes.

## TODO

1. If no topology.kubernetes.io/zone key exists, create it, then add the list of unimpared zones as values. Use DescribeCluster to get the current list of eligible subnets/availability zones. If it's an auto-mode cluster and there are no custom node pools, create a new node pool from the general purpose node pool and add the topology.kubeberes.io/zone key and the list of unimpared zones as values.
2. When a zonal shift occurs, and if the topology.kubernetes.io/zone key exists, store the name of the impared zone, e.g. us-west-2a, in a database like DynamoDB before removing it from the list of values. When service is restored (the zonal shift is cancelled or expires), restore the zone to the list of values by reading from the database. Alternatively, we can get the list of zones from the VPC subnets that have the Karpenter autodiscover tag and restore the missing zone. See [this issue](https://github.com/jicowan/zonal-autoshift-karpenter/issues/1) for further discussion on this topic. 
3. Secure the HTTP endpoint for the SNS client that runs in the k8s cluster or, if that's not possible, switch to SQS.
4. Use an Infrastructure as Code tool such as TF or CDK to automate the deployment.
