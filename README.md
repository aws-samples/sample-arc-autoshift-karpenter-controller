# zonal-autoshift-karpenter
This project is a reference implementation of a Kubernetes "controller" that listens for events that occur when a zonal autoshift is underway. Outages, FIS experiments, and autoshift practice runs can all trigger these types of events. When this happens, an EventBridge rule routes the messages to an SQS queue. The pods that are part of the controller poll the queue periodically. If the message indicates that a zonal shift is "in progress", the controller will evaluate and perform the following: 

1. If the `topology.kubernetes.io/zone` key exists, the controller will remove the impaired zone from the list of zones and temporarily store it in an annotation `zonal-autoshift.eks.amazonaws.com/away-zones: <zone_name>` where `zone_name` is the name of the impaired zone. 
2. If the `topology.kubernetes.io/zone` key is missing from the node pool, the controller will automatically discover the cluster's subnets and their availability zones, add the `topology.kubernetes.io/zone` key to the node pool's requirements section, add the zones names of discovered zones to the topology key; omitting the name of the impaired zone, and store the name of the impaired zone as an annotation. 
3. If the node pool is Auto Mode node pool, for example, the general-purpose or system node pool, the controller will create a copy of the node pool, remove the name of the impaired zone from the topology key, store the name of the impaired zone as an annotation, and assign the node pool a higher weight so it can be prioritized. 

When a zonal autoshift is "completed" or "cancelled", the changes are reverted, restoring each node pool's original settings. 

> Important: when zones are removed from a Karpenter node pool, all of the instances that were provisioned into that zone will be terminated. The pods running on those nodes will either be scheduled onto other nodes within the cluster or, Karpenter will provision new nodes in the healthy zones. So long as your pod's topology constraints are flexible enough, the pods will get rescheduled onto the new nodes. This differs from the behavior exhibited by nodes in a Managed Node Groups. During an autoshift, the Application Recovery Controller (ARC) will cordon the nodes in the impaired zone, remove the impaired zone from the ASGs that backs the MNG, and updates endpoint slices to divert traffic away from pods running on instances in the impaired zone, however, the pods (and nodes) in the impaired zone will continue running. The replica count for your deployments will be unaffected, but the capacity of your applications may temporarily drop, at least until the HPA can scale your Deployments.

Since this is reference implementation, this controller should not be used in production environments. AWS Support will not provide support for this project, however, it's maintainers will provide best effort support when possible. If you choose to use this project, you do so at your own risk. 


# Welcome to your CDK Python project!

This is a CDK example showcasing how you can use the Zonal Autoshift's integration with EventBridge to automatically remove zones from a Karpenter node pool when an autoshift is underway.



To manually create a virtualenv on MacOS and Linux:

```
$ python3 -m venv .venv
```

After the init process completes and the virtualenv is created, you can use the following
step to activate your virtualenv.

```
$ source .venv/bin/activate
```

If you are a Windows platform, you would activate the virtualenv like this:

```
% .venv\Scripts\activate.bat
```

Once the virtualenv is activated, you can install the required dependencies.

```
$ pip install -r requirements.txt
```

At this point you can now synthesize the CloudFormation template for this code.

```
$ cdk synth
```

To add additional dependencies, for example other CDK libraries, just add
them to your `setup.py` file and rerun the `pip install -r requirements.txt`
command.

## Useful commands

 * `cdk ls`          list all stacks in the app
 * `cdk synth`       emits the synthesized CloudFormation template
 * `cdk deploy`      deploy this stack to your default AWS account/region
 * `cdk diff`        compare deployed stack with current state
 * `cdk docs`        open CDK documentation


To deploy this stack run `cdk deploy --all`


## High level setup instructions
1. Create an EventBridge rule for zonal shift, "Autoshift in progress" events.
2. Configure the rule to send autoshift events to an SQS queue.
3. Create an IAM role for the zonal-autoshift-karpenter pod to assume that allows it to poll and read messages from the queue. If using IRSA, add the roleArn to the pod's service account. If using Pod Identity, create a pod identity association. 
4. Apply the zonal-autoshift-karpenter deployment.yaml

## Security
See [CONTRIBUTING](./CONTRIBUTING.md) for more information.

## License
This library is licensed under the MIT-0 License. See the LICENSE file.
