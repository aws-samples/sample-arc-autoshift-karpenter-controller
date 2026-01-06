package main

import (
        "context"
        "encoding/json"
        "fmt"
        "log"
        "os"
        "sync"
        "time"
        "github.com/aws/aws-sdk-go-v2/aws"
        "github.com/aws/aws-sdk-go-v2/config"
        "github.com/aws/aws-sdk-go-v2/service/ec2"
        ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
        "github.com/aws/aws-sdk-go-v2/service/eks"
        "github.com/aws/aws-sdk-go-v2/service/sqs"
        "project/zonal-shift/pkg/controller"
        "k8s.io/client-go/kubernetes"
        "k8s.io/client-go/rest"
        "k8s.io/apimachinery/pkg/types"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/client-go/tools/leaderelection"
        "k8s.io/client-go/tools/leaderelection/resourcelock"
        "k8s.io/client-go/dynamic"
        "k8s.io/apimachinery/pkg/runtime/schema"
)

// Global zone mapping cache
var (
        zoneMapping = make(map[string]string) // zoneID -> zoneName
        zoneMutex   sync.RWMutex
        clusterZones []string
        clusterZonesMutex sync.RWMutex
        doNotDisruptEnabled bool // Flag to enable/disable do-not-disrupt feature
        isLeader bool // Flag to track leader status
        globalNodeController *controller.NodeClaimController // Global controller for node tracking
)

// SQSMessage represents the structure of an SQS message from EventBridge
type SQSMessage struct {
        MessageId     string `json:"MessageId"`
        ReceiptHandle string `json:"ReceiptHandle"`
        Body          string `json:"Body"`
}

type Event struct {
        Version    string   `json:"version"`
        ID         string   `json:"id"`
        DetailType string   `json:"detail-type"`
        Source     string   `json:"source"`
        Account    string   `json:"account"`
        Time       string   `json:"time"`
        Region     string   `json:"region"`
        Resources  []string `json:"resources"`
        Detail     Detail   `json:"detail"`
}

type Detail struct {
        Version            string   `json:"version,omitempty"`
        ResourceIdentifier string   `json:"resourceIdentifier"`
        AwayFrom          string   `json:"awayFrom"`
        Metadata          Metadata `json:"metadata,omitempty"`
        ExpiresIn         string   `json:"expiresIn,omitempty"`
        Comment           string   `json:"comment,omitempty"`
        ZonalShiftId      string   `json:"zonalShiftId,omitempty"`
        ExpiryTime        string   `json:"expiryTime,omitempty"`
        StartTime         string   `json:"startTime,omitempty"`
        Status            string   `json:"status,omitempty"`
}

type Metadata struct {
        AwayFrom string `json:"awayFrom"`
}



// NodePoolMetadata represents the metadata of a Karpenter NodePool
type NodePoolMetadata struct {
    Name        string            `json:"name"`
    Namespace   string            `json:"namespace,omitempty"`
    Labels      map[string]string `json:"labels,omitempty"`
    Annotations map[string]string `json:"annotations,omitempty"`
}

// Taint represents a Kubernetes taint
type Taint struct {
    Key    string `json:"key"`
    Value  string `json:"value,omitempty"`
    Effect string `json:"effect"`
}

// NodePool represents the structure of a Karpenter NodePool
type NodePool struct {
    APIVersion string           `json:"apiVersion"`
    Kind       string           `json:"kind"`
    Metadata   NodePoolMetadata `json:"metadata"`
    Spec struct {
        Weight     *int `json:"weight,omitempty"`
        Disruption struct {
            Budgets []struct {
                Nodes string `json:"nodes"`
            } `json:"budgets"`
            ConsolidateAfter     string `json:"consolidateAfter"`
            ConsolidationPolicy  string `json:"consolidationPolicy"`
        } `json:"disruption"`
        Template struct {
            Spec struct {
                Requirements []struct {
                    Key      string   `json:"key"`
                    Operator string   `json:"operator"`
                    Values   []string `json:"values"`
                } `json:"requirements"`
                Taints []Taint `json:"taints,omitempty"`
                NodeClassRef struct {
                    Name  string `json:"name"`
                    Kind  string `json:"kind"`
                    Group string `json:"group"`
                } `json:"nodeClassRef,omitempty"`
                ExpireAfter          string `json:"expireAfter,omitempty"`
                TerminationGracePeriod string `json:"terminationGracePeriod,omitempty"`
            } `json:"spec"`
        } `json:"template"`
        Limits struct {
            CPU    string `json:"cpu,omitempty"`
            Memory string `json:"memory,omitempty"`
        } `json:"limits,omitempty"`
    } `json:"spec"`
}

func init() {
        log.SetOutput(os.Stdout)
        log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}

// initializeZoneMapping loads zone mappings at startup
func initializeZoneMapping() error {
        region := os.Getenv("AWS_REGION")
        if region == "" {
                region = "us-west-2" // fallback
        }
        
        awsCfg, err := config.LoadDefaultConfig(context.TODO(), config.WithRegion(region))
        if err != nil {
                return fmt.Errorf("failed to load AWS config: %v", err)
        }
        
        ec2Client := ec2.NewFromConfig(awsCfg)
        
        // Load all AZ mappings
        azResult, err := ec2Client.DescribeAvailabilityZones(context.TODO(), &ec2.DescribeAvailabilityZonesInput{})
        if err != nil {
                return fmt.Errorf("failed to describe availability zones: %v", err)
        }
        
        zoneMutex.Lock()
        for _, az := range azResult.AvailabilityZones {
                if az.ZoneId != nil && az.ZoneName != nil {
                        zoneMapping[*az.ZoneId] = *az.ZoneName
                }
        }
        zoneMutex.Unlock()
        
        // Load cluster zones
        clusterName := os.Getenv("CLUSTER_NAME")
        if clusterName == "" {
                return fmt.Errorf("CLUSTER_NAME environment variable not set")
        }
        
        // Check if auto mode by looking for CRDs
        k8sConfig, err := rest.InClusterConfig()
        if err != nil {
                return fmt.Errorf("failed to create cluster config: %v", err)
        }
        
        isAutoMode := listInstalledCRDs(k8sConfig)
        zones, err := getEKSClusterZones(region, isAutoMode)
        if err != nil {
                return fmt.Errorf("failed to get cluster zones: %v", err)
        }
        
        clusterZonesMutex.Lock()
        clusterZones = zones
        clusterZonesMutex.Unlock()
        
        log.Printf("[initializeZoneMapping] Loaded %d zone mappings and %d cluster zones", len(zoneMapping), len(clusterZones))
        return nil
}

func main() {
        // Handle health check flag
        if len(os.Args) > 1 && os.Args[1] == "-health" {
                os.Exit(0)
        }

        // Parse command-line arguments for do-not-disrupt feature
        doNotDisruptEnabled = false
        for i := 1; i < len(os.Args); i++ {
                if os.Args[i] == "--enable-do-not-disrupt" || os.Args[i] == "-d" {
                        doNotDisruptEnabled = true
                        log.Println("[main] do-not-disrupt feature enabled via command-line argument")
                        break
                }
        }

        var err error

        // Get pod name and namespace for leader election
        podName := os.Getenv("POD_NAME")
        if podName == "" {
                // Try to get hostname as fallback
                podName, err = os.Hostname()
                if err != nil {
                        log.Fatalf("Failed to get hostname: %v", err)
                }
        }

        namespace := os.Getenv("POD_NAMESPACE")
        if namespace == "" {
                namespace = "default" // Default namespace
        }

        // Get SQS queue URL from environment
        queueURL := os.Getenv("SQS_QUEUE_URL")
        if queueURL == "" {
                log.Fatalf("SQS_QUEUE_URL environment variable is required")
        }

        // Load AWS configuration
        awsCfg, err := config.LoadDefaultConfig(context.TODO())
        if err != nil {
                log.Fatalf("Failed to load AWS config: %v", err)
        }

        // Initialize zone mapping cache
        log.Println("Initializing zone mapping cache...")
        if err := initializeZoneMapping(); err != nil {
                log.Fatalf("Failed to initialize zone mapping: %v", err)
        }
        log.Println("Zone mapping cache initialized successfully")

        // Create SQS client
        sqsClient := sqs.NewFromConfig(awsCfg)

        // Create Kubernetes clientset for leader election
        k8sConfig, err := rest.InClusterConfig()
        if err != nil {
                log.Fatalf("Failed to create cluster config: %v", err)
        }
        
        clientset, err := kubernetes.NewForConfig(k8sConfig)
        if err != nil {
                log.Fatalf("Failed to create clientset: %v", err)
        }

        // Initialize NodeClaim controller to track nodes in memory
        log.Println("Initializing NodeClaim controller...")
        dynamicClient, err := dynamic.NewForConfig(k8sConfig)
        if err != nil {
                log.Fatalf("Failed to create dynamic client: %v", err)
        }
        globalNodeController = controller.NewNodeClaimController(dynamicClient, clientset)
        
        // Start the controller in background to watch NodeClaims
        controllerCtx, controllerCancel := context.WithCancel(context.Background())
        defer controllerCancel()
        go func() {
                if err := globalNodeController.Run(controllerCtx); err != nil {
                        log.Printf("NodeClaim controller error: %v", err)
                }
        }()
        log.Println("NodeClaim controller started successfully")

        // Start leader election
        log.Printf("Starting leader election for pod %s in namespace %s", podName, namespace)
        
        ctx, cancel := context.WithCancel(context.Background())
        defer cancel()
        
        // Configure leader election
        lock := &resourcelock.LeaseLock{
                LeaseMeta: metav1.ObjectMeta{
                        Name:      "karpenter-sqs-subscriber-leader",
                        Namespace: namespace,
                },
                Client: clientset.CoordinationV1(),
                LockConfig: resourcelock.ResourceLockConfig{
                        Identity: podName,
                },
        }
        
        // Start leader election
        leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
                Lock:            lock,
                ReleaseOnCancel: true,
                LeaseDuration:   15 * time.Second,
                RenewDeadline:   10 * time.Second,
                RetryPeriod:     2 * time.Second,
                Callbacks: leaderelection.LeaderCallbacks{
                        OnStartedLeading: func(ctx context.Context) {
                                log.Printf("Pod %s started leading", podName)
                                isLeader = true
                                // Start SQS polling when becoming leader
                                log.Printf("Starting SQS polling for queue: %s", queueURL)
                                pollSQS(sqsClient, queueURL, podName)
                        },
                        OnStoppedLeading: func() {
                                log.Printf("Pod %s stopped leading", podName)
                                isLeader = false
                        },
                        OnNewLeader: func(identity string) {
                                if identity == podName {
                                        log.Printf("This pod (%s) is the new leader", podName)
                                } else {
                                        log.Printf("New leader elected: %s (this pod: %s)", identity, podName)
                                }
                        },
                },
        })


}

// listInstalledCRDs checks if nodeclasses.eks.amazonaws.com CRD exists
func listInstalledCRDs(config *rest.Config) bool {
        dynamicClient, err := dynamic.NewForConfig(config)
        if err != nil {
                log.Printf("[listInstalledCRDs] Failed to create dynamic client: %v", err)
                return false
        }

        crds, err := dynamicClient.Resource(schema.GroupVersionResource{
                Group:    "apiextensions.k8s.io",
                Version:  "v1",
                Resource: "customresourcedefinitions",
        }).List(context.TODO(), metav1.ListOptions{})
        if err != nil {
                log.Printf("[listInstalledCRDs] Failed to list CRDs: %v", err)
                return false
        }

        for _, crd := range crds.Items {
                if crd.GetName() == "nodeclasses.eks.amazonaws.com" {
                        log.Printf("[listInstalledCRDs] Found nodeclasses.eks.amazonaws.com CRD - EKS Auto mode detected")
                        return true
                }
        }
        log.Printf("[listInstalledCRDs] nodeclasses.eks.amazonaws.com CRD not found - not EKS Auto mode")
        return false
}
// pollSQS continuously polls SQS for messages
func pollSQS(sqsClient *sqs.Client, queueURL, podName string) {
        for {
                // Leader election check - only process messages if this pod is the leader
                if !isLeader {
                        log.Printf("[pollSQS] Pod %s is not leader, sleeping", podName)
                        time.Sleep(10 * time.Second)
                        continue
                }

                log.Printf("[pollSQS] Polling SQS queue from pod: %s", podName)
                
                // Receive messages from SQS
                result, err := sqsClient.ReceiveMessage(context.TODO(), &sqs.ReceiveMessageInput{
                        QueueUrl:            &queueURL,
                        MaxNumberOfMessages: 10,
                        WaitTimeSeconds:     20, // Long polling
                        VisibilityTimeout:   300,
                })
                
                if err != nil {
                        log.Printf("[pollSQS] Error receiving messages: %v", err)
                        time.Sleep(5 * time.Second)
                        continue
                }

                for _, message := range result.Messages {
                        log.Printf("[pollSQS] Processing message: %s", *message.MessageId)
                         
                        // Log raw message body for debugging
                        log.Printf("[pollSQS] Raw SQS message body: %s", *message.Body)
                        
                        // Parse the EventBridge event from SQS message body
                        var event Event
                        if err := json.Unmarshal([]byte(*message.Body), &event); err != nil {
                                log.Printf("[pollSQS] Failed to parse event from SQS message: %v", err)
                                continue
                        }
                        log.Printf("[pollSQS] Event %s ", event)
                        // Get awayFrom from metadata if available, otherwise from detail
                        awayFrom := event.Detail.AwayFrom
                        if event.Detail.Metadata.AwayFrom != "" {
                                awayFrom = event.Detail.Metadata.AwayFrom
                        }
                        
                        log.Printf("[pollSQS] Event received - ID: %s, Type: %s, AZ: %s",
                                event.ID,
                                event.DetailType,
                                awayFrom)

                        // Process the event asynchronously
                        go handleEvent(event)

                        // Delete the message from SQS after processing
                        _, err = sqsClient.DeleteMessage(context.TODO(), &sqs.DeleteMessageInput{
                                QueueUrl:      &queueURL,
                                ReceiptHandle: message.ReceiptHandle,
                        })
                        
                        if err != nil {
                                log.Printf("[pollSQS] Failed to delete message %s: %v", *message.MessageId, err)
                        } else {
                                log.Printf("[pollSQS] Successfully processed and deleted message %s", *message.MessageId)
                        }
                }
        }
}

// handleEvent processes the event regardless of how it was received
func handleEvent(event Event) {
        switch event.DetailType {
        case "Autoshift In Progress":
                log.Println("[handleEvent] Processing Autoshift In Progress event")
                updateKarpenterNodePool(event)
        case "FIS Experiment Autoshift In Progress":
                log.Println("[handleEvent] Processing FIS Experiment Autoshift In Progress event")
                updateKarpenterNodePool(event)
        case "Practice Run Started":
                log.Println("[handleEvent] Processing Practice Run Started event")
                updateKarpenterNodePool(event)
        case "Autoshift Completed":
                log.Println("[handleEvent] Processing Autoshift Completed event")
                restoreKarpenterNodePool(event)
        case "FIS Experiment Autoshift Canceled":
                log.Println("[handleEvent] Processing FIS Experiment Autoshift Canceled event")
                restoreKarpenterNodePool(event)
        case "FIS Experiment Autoshift Completed":
                log.Println("[handleEvent] Processing FIS Experiment Autoshift Completed event")
                restoreKarpenterNodePool(event)
        case "Practice Run Succeeded":
                log.Println("[handleEvent] Processing Practice Run Succeeded event")
                restoreKarpenterNodePool(event)
        case "Practice Run Interrupted":
                log.Println("[handleEvent] Processing Practice Run Interrupted event")
                restoreKarpenterNodePool(event)
        case "Practice Run Failed":
                log.Println("[handleEvent] Processing Practice Run Failed event")
                restoreKarpenterNodePool(event)
        default:
                log.Printf("[handleEvent] Unknown event detail-type: %s", event.DetailType)
        }
}

// CreateNodePool creates a new Karpenter NodePool
func CreateNodePool(ctx context.Context, namespace string,nodepoolName string, nodePoolSpec []byte) error {
    k8sConfig, err := rest.InClusterConfig()
        if err != nil {
                log.Printf("[CreateNodePool] Failed to create cluster config: %v", err)
                return fmt.Errorf("failed to create cluster config: %v", err)
        }
        
        clientset, err := kubernetes.NewForConfig(k8sConfig)
        if err != nil {
                log.Printf("[CreateNodePool] Failed to create clientset: %v", err)
                return fmt.Errorf("Failed to create clientset")
        }

        nodePoolSpec_String := string(nodePoolSpec)
        log.Printf("[CreateNodePool] Node pool spec: %s", nodePoolSpec_String)
        
        
        resp, err := clientset.RESTClient().Post().AbsPath("/apis/karpenter.sh/v1/nodepools").Body(nodePoolSpec).DoRaw(context.TODO())
        if err != nil {
                log.Printf("[CreateNodePool] Failed to CreateNodePool node pools: %v", err)
                return fmt.Errorf("Failed to create node pools")
        }
        log.Printf("[CreateNodePool] Succesfully created node pools: %v", nodepoolName)
        log.Printf("[CreateNodePool] Response: %s", string(resp))
        return nil
}

// Helper function to get zone name from zone ID using cached mapping
func getZoneNameFromZoneId(zoneId, region string) string {
        zoneMutex.RLock()
        zoneName, exists := zoneMapping[zoneId]
        zoneMutex.RUnlock()
        
        if exists {
                log.Printf("[getZoneNameFromZoneId] Found zone name %s for zone ID %s (cached)", zoneName, zoneId)
                return zoneName
        }
        
        log.Printf("[getZoneNameFromZoneId] No zone name found for zone ID %s in cached mapping", zoneId)
        return ""
}

// // getCredentialsProvider returns a credentials provider with fallback options
// func getCredentialsProvider() aws.CredentialsProvider {
// 	return aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
// 		// Try environment variables first
// 		if accessKey := os.Getenv("AWS_ACCESS_KEY_ID"); accessKey != "" {
// 			if secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY"); secretKey != "" {
// 				return aws.Credentials{
// 					AccessKeyID:     accessKey,
// 					SecretAccessKey: secretKey,
// 					SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
// 				}, nil
// 			}
// 		}
// 		// Fallback to default provider chain with shorter timeout
// 		cfg, err := config.LoadDefaultConfig(ctx, config.WithEC2IMDSClientOptions(func(o *imds.Options) {
// 			o.ClientTimeout = 1 * time.Second
// 		}))
// 		if err != nil {
// 			return aws.Credentials{}, fmt.Errorf("failed to load default config: %w", err)
// 		}
// 		return cfg.Credentials.Retrieve(ctx)
// 	}))
// }

// getEKSClusterVPCId gets the VPC ID of the EKS cluster
func getEKSClusterVPCId(region, clusterName string) (string, error) {
        awsCfg, err := config.LoadDefaultConfig(context.TODO(), config.WithRegion(region))
        if err != nil {
                return "", fmt.Errorf("failed to load AWS config: %v", err)
        }
        
        eksClient := eks.NewFromConfig(awsCfg)
        result, err := eksClient.DescribeCluster(context.TODO(), &eks.DescribeClusterInput{
                Name: &clusterName,
        })
        if err != nil {
                return "", fmt.Errorf("failed to describe EKS cluster: %v", err)
        }
        
        if result.Cluster == nil || result.Cluster.ResourcesVpcConfig == nil || result.Cluster.ResourcesVpcConfig.VpcId == nil {
                return "", fmt.Errorf("VPC ID not found for cluster %s", clusterName)
        }
        
        vpcId := *result.Cluster.ResourcesVpcConfig.VpcId
        log.Printf("[getEKSClusterVPCId] Found VPC ID %s for cluster %s", vpcId, clusterName)
        return vpcId, nil
}

// getEKSClusterZones gets zones where EKS cluster VPC has subnets
func getEKSClusterZones(region string, isAutoMode bool) ([]string, error) {
        awsCfg, err := config.LoadDefaultConfig(context.TODO(), config.WithRegion(region))
        if err != nil {
                return nil, fmt.Errorf("failed to load AWS config: %v", err)
        }
        ec2Client := ec2.NewFromConfig(awsCfg)
        
        clusterName := os.Getenv("CLUSTER_NAME")
        if clusterName == "" {
                return nil, fmt.Errorf("CLUSTER_NAME environment variable not set")
        }
        
        // Get the VPC ID of the EKS cluster
        vpcId, err := getEKSClusterVPCId(region, clusterName)
        if err != nil {
                return nil, fmt.Errorf("failed to get EKS cluster VPC ID: %v", err)
        }
        
        var subnetsInput *ec2.DescribeSubnetsInput
        
        if isAutoMode {
                // For auto mode, try multiple tag patterns
                clusterTagKey := "tag:kubernetes.io/cluster/" + clusterName
                log.Printf("[getEKSClusterZones] Looking for subnets with tag: %s in VPC: %s", clusterTagKey, vpcId)
                
                subnetsInput = &ec2.DescribeSubnetsInput{
                        Filters: []ec2types.Filter{
                                {
                                        Name:   &clusterTagKey,
                                        Values: []string{"owned", "shared"},
                                },
                                {
                                        Name:   aws.String("vpc-id"),
                                        Values: []string{vpcId},
                                },
                        },
                }
                
                // If no subnets found with cluster tag, try eks.amazonaws.com/cluster tag
                subnetsOutput, err := ec2Client.DescribeSubnets(context.TODO(), subnetsInput)
                if err == nil && len(subnetsOutput.Subnets) == 0 {
                        log.Printf("[getEKSClusterZones] No subnets found with kubernetes.io/cluster tag, trying eks.amazonaws.com/cluster")
                        eksTagKey := "tag:eks.amazonaws.com/cluster"
                        subnetsInput = &ec2.DescribeSubnetsInput{
                                Filters: []ec2types.Filter{
                                        {
                                                Name:   &eksTagKey,
                                                Values: []string{clusterName},
                                        },
                                        {
                                                Name:   aws.String("vpc-id"),
                                                Values: []string{vpcId},
                                        },
                                },
                        }
                }
        } else {
                // For standard mode, find subnets tagged for Karpenter discovery
                tagKey := "tag:karpenter.sh/discovery"
                subnetsInput = &ec2.DescribeSubnetsInput{
                        Filters: []ec2types.Filter{
                                {
                                        Name:   &tagKey,
                                        Values: []string{clusterName},
                                },
                                {
                                        Name:   aws.String("vpc-id"),
                                        Values: []string{vpcId},
                                },
                        },
                }
        }
        
        log.Printf("[getEKSClusterZones] Searching for subnets with filters: %+v", subnetsInput.Filters)
        subnetsOutput, err := ec2Client.DescribeSubnets(context.TODO(), subnetsInput)
        if err != nil {
                return nil, fmt.Errorf("failed to describe subnets: %v", err)
        }
        
        // If still no subnets found in auto mode, fall back to all subnets in the EKS cluster's VPC
        if isAutoMode && len(subnetsOutput.Subnets) == 0 {
                log.Printf("[getEKSClusterZones] No tagged subnets found, falling back to all subnets in VPC %s", vpcId)
                subnetsInput = &ec2.DescribeSubnetsInput{
                        Filters: []ec2types.Filter{
                                {
                                        Name:   aws.String("vpc-id"),
                                        Values: []string{vpcId},
                                },
                                {
                                        Name:   aws.String("state"),
                                        Values: []string{"available"},
                                },
                        },
                }
                subnetsOutput, err = ec2Client.DescribeSubnets(context.TODO(), subnetsInput)
                if err != nil {
                        return nil, fmt.Errorf("failed to describe subnets in VPC: %v", err)
                }
        }
        
        log.Printf("[getEKSClusterZones] Found %d subnets in VPC %s", len(subnetsOutput.Subnets), vpcId)
        
        // Extract unique zones from subnets
        zoneSet := make(map[string]bool)
        for _, subnet := range subnetsOutput.Subnets {
                if subnet.AvailabilityZone != nil {
                        log.Printf("[getEKSClusterZones] Found subnet %s in zone %s (VPC: %s)", *subnet.SubnetId, *subnet.AvailabilityZone, vpcId)
                        zoneSet[*subnet.AvailabilityZone] = true
                }
        }
        
        var zones []string
        for zone := range zoneSet {
                zones = append(zones, zone)
        }
        
        log.Printf("[getEKSClusterZones] Found EKS cluster zones (autoMode=%v, VPC=%s): %v", isAutoMode, vpcId, zones)
        return zones, nil
}

// Create a function to identity the updated Availability zones after ignoring the AwayFrom zone. 
func getUpdatedZones(event Event, isAutoMode bool) ([]string) {
        // Use cached cluster zones
        log.Printf("[getUpdatedZones-debug] event: %s, automode:%v", event.ID, isAutoMode)
        
        clusterZonesMutex.RLock()
        cachedClusterZones := make([]string, len(clusterZones))
        copy(cachedClusterZones, clusterZones)
        clusterZonesMutex.RUnlock()
        
        log.Printf("[getUpdatedZones] Cached cluster zones: %v", cachedClusterZones)
        
        // Get awayFrom from metadata if available, otherwise from detail
        awayFrom := event.Detail.AwayFrom
        if event.Detail.Metadata.AwayFrom != "" {
                awayFrom = event.Detail.Metadata.AwayFrom
        }
        
        log.Printf("[getUpdatedZones] AwayFrom zone ID: %s", awayFrom)
        
        // Get zone name from zone ID for comparison
        awayZoneName := getZoneNameFromZoneId(awayFrom, event.Region)
        if awayZoneName == "" {
                log.Printf("[getUpdatedZones] Failed to get zone name for zone ID: %s in cached mapping, returning all zones", awayFrom)
                return cachedClusterZones // Return all zones if we can't identify the away zone
        }
        
        log.Printf("[getUpdatedZones] AwayFrom zone name: %s", awayZoneName)
        
        var updatedZones []string
        for _, zone := range cachedClusterZones {
                if zone != awayZoneName {
                        log.Printf("[getUpdatedZones] Including AZ %s in updated zones", zone)
                        updatedZones = append(updatedZones, zone)
                } else {
                        log.Printf("[getUpdatedZones] Excluding AZ %s as it matches AwayFrom zone", zone)
                }
        }
        
        // Ensure we always have at least one zone
        if len(updatedZones) == 0 {
                log.Printf("[getUpdatedZones] No zones remaining after filtering, returning original zones")
                return cachedClusterZones
        }
        
        log.Printf("[getUpdatedZones] Updated zones: %v", updatedZones)
        return updatedZones
}

// checkEKSAutoMode checks if the EKS cluster is running in auto-mode
// func checkEKSAutoMode() bool {
//         region := os.Getenv("AWS_REGION")
//         if region == "" {
//                 region = "us-west-2" // fallback
//         }
        
//         awsCfg, err := config.LoadDefaultConfig(context.TODO(), config.WithRegion(region))
//         if err != nil {
//                 log.Printf("[checkEKSAutoMode] Failed to load AWS config: %v", err)
//                 return false
//         }
        
//         eksClient := eks.NewFromConfig(awsCfg)
//         clusterName := os.Getenv("CLUSTER_NAME")
//         if clusterName == "" {
//                 log.Println("[checkEKSAutoMode] CLUSTER_NAME environment variable not set")
//                 return false
//         }
        
//         // List managed node groups
//         nodeGroups, err := eksClient.ListNodegroups(context.TODO(), &eks.ListNodegroupsInput{
//                 ClusterName: &clusterName,
//         })
//         if err != nil {
//                 log.Printf("[checkEKSAutoMode] Failed to list node groups: %v", err)
//                 return false
//         }
        
//         // If no managed node groups exist, likely auto-mode
//         hasNodeGroups := len(nodeGroups.Nodegroups) > 0
//         log.Printf("[checkEKSAutoMode] Cluster has %d managed node groups", len(nodeGroups.Nodegroups))
        
//         return !hasNodeGroups
// }


// updateKarpenterNodePool updates the Karpenter node pool based on the event
func updateKarpenterNodePool(event Event) {
        // Get awayFrom from metadata if available, otherwise from detail
        awayFrom := event.Detail.AwayFrom
        if event.Detail.Metadata.AwayFrom != "" {
                awayFrom = event.Detail.Metadata.AwayFrom
        }
        
        log.Printf("[updateKarpenterNodePool] Processing event for AZ: %s", awayFrom)

        k8sConfig, err := rest.InClusterConfig()
        if err != nil {
                log.Printf("[updateKarpenterNodePool] Failed to create cluster config: %v", err)
                return
        }

        // Check if EKS Auto mode by looking for nodeclasses.eks.amazonaws.com CRD
        isAutoMode := listInstalledCRDs(k8sConfig)
        if isAutoMode {
                log.Println("[updateKarpenterNodePool] EKS cluster is running in AUTO-MODE")
        } else {
                log.Println("[updateKarpenterNodePool] EKS cluster is NOT running in auto-mode")
        }

        clientset, err := kubernetes.NewForConfig(k8sConfig)
        if err != nil {
                log.Printf("[updateKarpenterNodePool] Failed to create clientset: %v", err)
                return
        }

        // Check if do-not-disrupt feature is enabled
        if doNotDisruptEnabled {
                log.Println("[updateKarpenterNodePool] do-not-disrupt feature enabled")

                // Get zone name from zone ID
                awayZoneName := getZoneNameFromZoneId(awayFrom, event.Region)
                if awayZoneName == "" {
                        log.Printf("[updateKarpenterNodePool] Failed to get zone name for zone ID: %s", awayFrom)
                        awayZoneName = awayFrom
                }

                // Use global controller to annotate and cordon nodes
                if globalNodeController != nil {
                        if err := globalNodeController.AnnotateAndCordonNodes(context.TODO(), awayZoneName); err != nil {
                                // This is expected if no Karpenter nodes exist in the zone yet
                                log.Printf("[updateKarpenterNodePool] Note: %v (this is normal if no Karpenter nodes exist in this zone)", err)
                        } else {
                                log.Printf("[updateKarpenterNodePool] Successfully annotated and cordoned nodes in AZ %s", awayZoneName)
                        }
                } else {
                        log.Printf("[updateKarpenterNodePool] NodeClaim controller not initialized, skipping node annotation")
                }
        } else {
                log.Println("[updateKarpenterNodePool] do-not-disrupt feature disabled, skipping node annotation and cordoning")
        }

        log.Println("[updateKarpenterNodePool] Retrieving Karpenter node pools...")
        nodePools, err := clientset.RESTClient().Get().AbsPath("/apis/karpenter.sh/v1/nodepools").DoRaw(context.TODO())
        if err != nil {
                log.Printf("[updateKarpenterNodePool] Failed to get node pools: %v", err)
                return
        }

        var nodePoolList struct {
                Items []struct {
                        APIVersion string           `json:"apiVersion"`
                    Kind       string           `json:"kind"`
                        Metadata NodePoolMetadata `json:"metadata"`
                        Spec struct {
                                Disruption struct {
                                        Budgets []struct {
                                                Nodes string `json:"nodes"`
                                        } `json:"budgets"`
                                        ConsolidateAfter    string `json:"consolidateAfter"`
                                        ConsolidationPolicy string `json:"consolidationPolicy"`
                                } `json:"disruption"`
                                Template struct {
                                        Spec struct {
                                                Requirements []struct {
                                                        Key      string   `json:"key"`
                                                        Operator string   `json:"operator"`
                                                        Values   []string `json:"values"`
                                                } `json:"requirements"`
                                                Taints []Taint `json:"taints,omitempty"`
                                                NodeClassRef struct {
                                                        Name  string `json:"name"`
                                                        Kind  string `json:"kind"`
                                                        Group string `json:"group"`
                                                } `json:"nodeClassRef,omitempty"`
                                        } `json:"spec"`
                                } `json:"template"`
                        } `json:"spec"`
                } `json:"items"`
        }

        if err := json.Unmarshal(nodePools, &nodePoolList); err != nil {
                log.Printf("[updateKarpenterNodePool] Failed to parse node pools: %v", err)
                return
        }

        log.Printf("[updateKarpenterNodePool] Found %d node pools", len(nodePoolList.Items))
        // Let's log the raw JSON for debugging
        rawJSON, _ := json.MarshalIndent(nodePoolList, "", "  ")
        log.Printf("[updateKarpenterNodePool] Raw node pool list: %s", string(rawJSON))

        if isAutoMode {
                log.Println("[updateKarpenterNodePool] EKS Auto mode detected, processing node pools")
                
                updatedZones := getUpdatedZones(event, isAutoMode)
                if len(updatedZones) == 0 {
                        log.Printf("[updateKarpenterNodePool] No zones available for auto mode, skipping")
                        return
                }
                
                for _, pool := range nodePoolList.Items {
                        if pool.Metadata.Name == "general-purpose" || pool.Metadata.Name == "system" {
                                // Create new node pool for general-purpose and system
                                newNodePoolName := pool.Metadata.Name + "-kss"
                                log.Printf("[updateKarpenterNodePool] Creating new node pool %s from %s", newNodePoolName, pool.Metadata.Name)
                                
                                weight := 50
                                nodePoolItem := NodePool{
                                        APIVersion: pool.APIVersion,
                                        Kind:       pool.Kind,
                                        Metadata: NodePoolMetadata{
                                                Name:      newNodePoolName,
                                                Namespace: "default",
                                                Annotations: map[string]string{
                                                        "zonal-autoshift.eks.amazonaws.com/away-zones": awayFrom,
                                                },
                                        },
                                }
                                nodePoolItem.Spec.Weight = &weight
                                
                                // Initialize requirements
                                nodePoolItem.Spec.Template.Spec.Requirements = make([]struct {
                                        Key      string   `json:"key"`
                                        Operator string   `json:"operator"`
                                        Values   []string `json:"values"`
                                }, 0)
                                
                                // Copy requirements
                                for _, req := range pool.Spec.Template.Spec.Requirements {
                                        nodePoolItem.Spec.Template.Spec.Requirements = append(
                                                nodePoolItem.Spec.Template.Spec.Requirements,
                                                struct {
                                                        Key      string   `json:"key"`
                                                        Operator string   `json:"operator"`
                                                        Values   []string `json:"values"`
                                                }{
                                                        Key:      req.Key,
                                                        Operator: req.Operator,
                                                        Values:   req.Values,
                                                },
                                        )
                                }
                                
                                // Add topology.kubernetes.io/zone requirement with updated zones
                                nodePoolItem.Spec.Template.Spec.Requirements = append(nodePoolItem.Spec.Template.Spec.Requirements, struct {
                                        Key      string   `json:"key"`
                                        Operator string   `json:"operator"`
                                        Values   []string `json:"values"`
                                }{
                                        Key:      "topology.kubernetes.io/zone",
                                        Operator: "In",
                                        Values:   updatedZones,
                                })
                                
                                // Copy taints only for system nodepool (system has CriticalAddonsOnly taint)
                                if pool.Metadata.Name == "system" && len(pool.Spec.Template.Spec.Taints) > 0 {
                                        nodePoolItem.Spec.Template.Spec.Taints = make([]Taint, len(pool.Spec.Template.Spec.Taints))
                                        copy(nodePoolItem.Spec.Template.Spec.Taints, pool.Spec.Template.Spec.Taints)
                                        log.Printf("[updateKarpenterNodePool] Copied %d taints to new node pool %s", len(pool.Spec.Template.Spec.Taints), newNodePoolName)
                                }
                                
                                // Copy disruption settings
                                nodePoolItem.Spec.Disruption.Budgets = make([]struct {
                                        Nodes string `json:"nodes"`
                                }, 0)
                                if len(pool.Spec.Disruption.Budgets) > 0 {
                                        budgetValue := pool.Spec.Disruption.Budgets[0].Nodes
                                        // if doNotDisruptEnabled {
                                        //         budgetValue = "0"
                                        //         log.Printf("[updateKarpenterNodePool] Setting disruption budget to 0 for node pool %s (do-not-disrupt enabled)", newNodePoolName)
                                        // }
                                        nodePoolItem.Spec.Disruption.Budgets = append(nodePoolItem.Spec.Disruption.Budgets, struct {
                                                Nodes string `json:"nodes"`
                                        }{
                                                Nodes: budgetValue,
                                        })
                                }
                                nodePoolItem.Spec.Disruption.ConsolidateAfter = pool.Spec.Disruption.ConsolidateAfter
                                nodePoolItem.Spec.Disruption.ConsolidationPolicy = pool.Spec.Disruption.ConsolidationPolicy
                                
                                // Copy node class reference
                                nodePoolItem.Spec.Template.Spec.NodeClassRef.Name = pool.Spec.Template.Spec.NodeClassRef.Name
                                nodePoolItem.Spec.Template.Spec.NodeClassRef.Kind = pool.Spec.Template.Spec.NodeClassRef.Kind
                                nodePoolItem.Spec.Template.Spec.NodeClassRef.Group = pool.Spec.Template.Spec.NodeClassRef.Group
                                
                                // Convert to JSON and create
                                newNodePoolJSON, err := json.Marshal(nodePoolItem)
                                if err != nil {
                                        log.Printf("[updateKarpenterNodePool] Failed to marshal new node pool %s: %v", newNodePoolName, err)
                                        continue
                                }
                                
                                ctx := context.Background()
                                err = CreateNodePool(ctx, "default", newNodePoolName, newNodePoolJSON)
                                if err != nil {
                                        log.Printf("[updateKarpenterNodePool] Failed to create node pool %s: %v", newNodePoolName, err)
                                }
                                
                                // Add NoExecute taint to nodes in the impaired zone only if do-not-disrupt is disabled
                                if !doNotDisruptEnabled {
                                        awayZoneName := getZoneNameFromZoneId(awayFrom, event.Region)
                                        if awayZoneName != "" && globalNodeController != nil {
                                                if err := globalNodeController.TaintNodesInAZ(ctx, awayZoneName); err != nil {
                                                        log.Printf("[updateKarpenterNodePool] Error tainting nodes in AZ %s: %v", awayZoneName, err)
                                                }
                                        }
                                }
                        } else {
                                // Update other node pools in-place
                                log.Printf("[updateKarpenterNodePool] Processing node pool: %s", pool.Metadata.Name)
                                
                                zoneRequirementExists := false
                                
                                for i, req := range pool.Spec.Template.Spec.Requirements {
                                        if req.Key == "topology.kubernetes.io/zone" {
                                                log.Printf("[updateKarpenterNodePool] Node pool %s has a zone requirement: %+v", pool.Metadata.Name, req.Values)
                                                zoneRequirementExists = true
                                                
                                                // Get zone name from zone ID for comparison
                                                awayZoneName := getZoneNameFromZoneId(awayFrom, event.Region)
                                                
                                                // Filter existing zones to remove awayFrom zone
                                                var filteredZones []string
                                                for _, zone := range req.Values {
                                                        if zone != awayZoneName {
                                                                filteredZones = append(filteredZones, zone)
                                                        }
                                                }
                                                
                                                if len(filteredZones) != len(req.Values) {
                                                        log.Printf("[updateKarpenterNodePool] Updating node pool %s to remove AZ %s", pool.Metadata.Name, awayZoneName)
                                                        pool.Spec.Template.Spec.Requirements[i].Values = filteredZones

                                                        // Set disruption budget to 0 if do-not-disrupt is enabled
                                                        // if doNotDisruptEnabled && len(pool.Spec.Disruption.Budgets) > 0 {
                                                        //         pool.Spec.Disruption.Budgets[0].Nodes = "0"
                                                        //         log.Printf("[updateKarpenterNodePool] Setting disruption budget to 0 for node pool %s (do-not-disrupt enabled)", pool.Metadata.Name)
                                                        // }

                                                        // Create the update payload
                                                        updatePayload := map[string]interface{}{
                                                                "spec": map[string]interface{}{
                                                                        "template": map[string]interface{}{
                                                                                "spec": map[string]interface{}{
                                                                                        "requirements": pool.Spec.Template.Spec.Requirements,
                                                                                },
                                                                        },
                                                                        "disruption": pool.Spec.Disruption,
                                                                },
                                                        }

                                                        // Convert the payload to JSON
                                                        patchBytes, err := json.Marshal(updatePayload)
                                                        if err != nil {
                                                                log.Printf("[updateKarpenterNodePool] Failed to marshal update payload: %v", err)
                                                                continue
                                                        }

                                                        // Make the PATCH request to update the nodepool
                                                        result := clientset.RESTClient().Patch(types.MergePatchType).
                                                        AbsPath(fmt.Sprintf("/apis/karpenter.sh/v1/nodepools/%s", pool.Metadata.Name)).
                                                        Body(patchBytes).
                                                        Do(context.TODO())
                        
                                                        if err := result.Error(); err != nil {
                                                                log.Printf("[updateKarpenterNodePool] Failed to update node pool %s: %v", pool.Metadata.Name, err)
                                                        } else {
                                                                log.Printf("[updateKarpenterNodePool] Successfully updated node pool %s", pool.Metadata.Name)
                                                        }

                                                        // Update annotation
                                                        annotationPatch := map[string]interface{}{
                                                                "metadata": map[string]interface{}{
                                                                        "annotations": map[string]interface{}{
                                                                                "zonal-autoshift.eks.amazonaws.com/away-zones": awayFrom,
                                                                        },
                                                                },
                                                        }
                                                        annotationPatchBytes, err := json.Marshal(annotationPatch)
                                                        if err == nil {
                                                                clientset.RESTClient().Patch(types.MergePatchType).
                                                                AbsPath(fmt.Sprintf("/apis/karpenter.sh/v1/nodepools/%s", pool.Metadata.Name)).
                                                                Body(annotationPatchBytes).
                                                                Do(context.TODO())
                                                        }
                                                }
                                                break
                                        }
                                }
                                
                                // If no zone requirement exists, add one
                                if !zoneRequirementExists {
                                        log.Printf("[updateKarpenterNodePool] Node pool %s has no zone requirement, adding topology.kubernetes.io/zone", pool.Metadata.Name)
                                        pool.Spec.Template.Spec.Requirements = append(pool.Spec.Template.Spec.Requirements, struct {
                                                Key      string   `json:"key"`
                                                Operator string   `json:"operator"`
                                                Values   []string `json:"values"`
                                        }{
                                                Key:      "topology.kubernetes.io/zone",
                                                Operator: "In",
                                                Values:   updatedZones,
                                        })
                                        
                                        // Set disruption budget to 0 if do-not-disrupt is enabled
                                        // if doNotDisruptEnabled && len(pool.Spec.Disruption.Budgets) > 0 {
                                        //         pool.Spec.Disruption.Budgets[0].Nodes = "0"
                                        //         log.Printf("[updateKarpenterNodePool] Setting disruption budget to 0 for node pool %s (do-not-disrupt enabled)", pool.Metadata.Name)
                                        // }

                                        // Create the update payload
                                        updatePayload := map[string]interface{}{
                                                "spec": map[string]interface{}{
                                                        "template": map[string]interface{} {
                                                                "spec": map[string]interface{}{
                                                                        "requirements": pool.Spec.Template.Spec.Requirements,
                                                                },
                                                        },
                                                        "disruption": pool.Spec.Disruption,
                                                },
                                        }

                                        // Convert the payload to JSON
                                        patchBytes, err := json.Marshal(updatePayload)
                                        if err != nil {
                                                log.Printf("[updateKarpenterNodePool] Failed to marshal update payload: %v", err)
                                                continue
                                        }

                                        // Make the PATCH request to update the nodepool
                                        result := clientset.RESTClient().Patch(types.MergePatchType).
                                        AbsPath(fmt.Sprintf("/apis/karpenter.sh/v1/nodepools/%s", pool.Metadata.Name)).
                                        Body(patchBytes).
                                        Do(context.TODO())
                
                                        if err := result.Error(); err != nil {
                                                log.Printf("[updateKarpenterNodePool] Failed to update node pool %s: %v", pool.Metadata.Name, err)
                                        } else {
                                                log.Printf("[updateKarpenterNodePool] Successfully added zone requirement to node pool %s", pool.Metadata.Name)
                                        }

                                        // Update annotation
                                        annotationPatch := map[string]interface{}{
                                                "metadata": map[string]interface{}{
                                                        "annotations": map[string]interface{}{
                                                                "zonal-autoshift.eks.amazonaws.com/away-zones": awayFrom,
                                                        },
                                                },
                                        }
                                        annotationPatchBytes, err := json.Marshal(annotationPatch)
                                        if err == nil {
                                                clientset.RESTClient().Patch(types.MergePatchType).
                                                AbsPath(fmt.Sprintf("/apis/karpenter.sh/v1/nodepools/%s", pool.Metadata.Name)).
                                                Body(annotationPatchBytes).
                                                Do(context.TODO())
                                        }
                                }
                        }
                }
        } else {
                for _, pool := range nodePoolList.Items {
                        log.Printf("[updateKarpenterNodePool] Processing node pool: %s", pool.Metadata.Name)
                        log.Printf("[updateKarpenterNodePool] Number of requirements: %d", len(pool.Spec.Template.Spec.Requirements))
                        
                        updatedZones := getUpdatedZones(event, isAutoMode)
                        if len(updatedZones) == 0 {
                                log.Printf("[updateKarpenterNodePool] No zones available, skipping node pool %s", pool.Metadata.Name)
                                continue
                        }
                        zoneRequirementExists := false
                        
                        for i, req := range pool.Spec.Template.Spec.Requirements {
                                log.Printf("[updateKarpenterNodePool] Checking requirement %d: Key=%s", i, req.Key)
                                if req.Key == "topology.kubernetes.io/zone" {
                                        log.Printf("[updateKarpenterNodePool] Node pool %s has a zone requirement: %+v", pool.Metadata.Name, req.Values)
                                        zoneRequirementExists = true
                                        
                                        log.Printf("[updateKarpenterNodePool] Filter existing zones")
                                        // Get zone name from zone ID for comparison
                                        awayZoneName := getZoneNameFromZoneId(awayFrom, event.Region)
                                        
                                        // Filter existing zones to remove awayFrom zone
                                        var filteredZones []string
                                        for _, zone := range req.Values {
                                                log.Printf("[filteredzone] Zone=%s and awayZone=%s", zone, awayZoneName)
                                                if zone != awayZoneName {
                                                        filteredZones = append(filteredZones, zone)
                                                }
                                        }
                                        log.Printf("[updateKarpenterNodePool] Filtered zones: %v", filteredZones)
                                        if len(filteredZones) != len(req.Values) {
                                                log.Printf("[updateKarpenterNodePool] Zone list changed for node pool %s:", pool.Metadata.Name)
                                                log.Printf("[updateKarpenterNodePool] Original zones: %v", req.Values)
                                                log.Printf("[updateKarpenterNodePool] Filtered zones: %v", filteredZones)
                                                log.Printf("[updateKarpenterNodePool] Updating node pool %s to remove AZ %s",
                                                        pool.Metadata.Name, awayZoneName)
                                                pool.Spec.Template.Spec.Requirements[i].Values = filteredZones

                                                // Set disruption budget to 0 if do-not-disrupt is enabled
                                                // if doNotDisruptEnabled && len(pool.Spec.Disruption.Budgets) > 0 {
                                                //         pool.Spec.Disruption.Budgets[0].Nodes = "0"
                                                //         log.Printf("[updateKarpenterNodePool] Setting disruption budget to 0 for node pool %s (do-not-disrupt enabled)", pool.Metadata.Name)
                                                // }

                                                // Create the update payload
                                                updatePayload := map[string]interface{}{
                                                        "spec": map[string]interface{}{
                                                                "template": map[string]interface{}{
                                                                        "spec": map[string]interface{}{
                                                                                "requirements": pool.Spec.Template.Spec.Requirements,
                                                                        },
                                                                },
                                                                "disruption": pool.Spec.Disruption,
                                                        },
                                                }

                                                // Convert the payload to JSON
                                                patchBytes, err := json.Marshal(updatePayload)
                                                if err != nil {
                                                        log.Printf("[updateKarpenterNodePool] Failed to marshal update payload: %v", err)
                                                        continue
                                                }

                                                // Make the PATCH request to update the nodepool
                                                result := clientset.RESTClient().Patch(types.MergePatchType).
                                                AbsPath(fmt.Sprintf("/apis/karpenter.sh/v1/nodepools/%s", pool.Metadata.Name)).
                                                Body(patchBytes).
                                                Do(context.TODO())
                
                                                if err := result.Error(); err != nil {
                                                        log.Printf("[updateKarpenterNodePool] Failed to update node pool %s: %v", 
                                                                pool.Metadata.Name, err)
                                                } else {
                                                        log.Printf("[updateKarpenterNodePool] Successfully updated node pool %s", 
                                                                pool.Metadata.Name)
                                                }

                                                const (
                                                        zoneAwayAnnotation = "zonal-autoshift.eks.amazonaws.com/away-zones"
                                                )

                                                // After successful requirements update, update the annotation
                                                annotationPatch := map[string]interface{}{
                                                        "metadata": map[string]interface{}{
                                                                "annotations": map[string]interface{}{
                                                                        zoneAwayAnnotation: awayFrom,
                                                                },
                                                        },
                                                }
                                                annotationPatchBytes, err := json.Marshal(annotationPatch)
                                                if err != nil {
                                                        log.Printf("[updateKarpenterNodePool] Failed to marshal annotation patch: %v", err)
                                                        continue
                                                }

                                                // Apply the annotation patch
                                                annotationResult := clientset.RESTClient().Patch(types.MergePatchType).
                                                AbsPath(fmt.Sprintf("/apis/karpenter.sh/v1/nodepools/%s", pool.Metadata.Name)).
                                                Body(annotationPatchBytes).
                                                Do(context.TODO())

                                                if err := annotationResult.Error(); err != nil {
                                                        log.Printf("[updateKarpenterNodePool] Failed to update node pool %s annotations: %v", 
                                                                pool.Metadata.Name, err)
                                                } else {
                                                        log.Printf("[updateKarpenterNodePool] Successfully updated node pool %s annotations with away zone %s", 
                                                                pool.Metadata.Name, event.Detail.AwayFrom)
                                                }

                                        } else {
                                                log.Printf("[updateKarpenterNodePool] No changes needed for node pool %s - awayFrom zone not present",
                                                        pool.Metadata.Name)
                                        }
                                        break
                                }
                        }
                        
                        // If no zone requirement exists, add one
                        if !zoneRequirementExists {
                                log.Printf("[updateKarpenterNodePool] Node pool %s has no zone requirement, adding topology.kubernetes.io/zone", pool.Metadata.Name)
                                pool.Spec.Template.Spec.Requirements = append(pool.Spec.Template.Spec.Requirements, struct {
                                        Key      string   `json:"key"`
                                        Operator string   `json:"operator"`
                                        Values   []string `json:"values"`
                                }{
                                        Key:      "topology.kubernetes.io/zone",
                                        Operator: "In",
                                        Values:   updatedZones,
                                })
                                
                                // Set disruption budget to 0 if do-not-disrupt is enabled
                                // if doNotDisruptEnabled && len(pool.Spec.Disruption.Budgets) > 0 {
                                //         pool.Spec.Disruption.Budgets[0].Nodes = "0"
                                //         log.Printf("[updateKarpenterNodePool] Setting disruption budget to 0 for node pool %s (do-not-disrupt enabled)", pool.Metadata.Name)
                                // }

                                // Create the update payload
                                updatePayload := map[string]interface{}{
                                        "spec": map[string]interface{}{
                                                "template": map[string]interface{} {
                                                        "spec": map[string]interface{}{
                                                                "requirements": pool.Spec.Template.Spec.Requirements,
                                                        },
                                                },
                                                "disruption": pool.Spec.Disruption,
                                        },
                                }

                                // Convert the payload to JSON
                                patchBytes, err := json.Marshal(updatePayload)
                                if err != nil {
                                        log.Printf("[updateKarpenterNodePool] Failed to marshal update payload: %v", err)
                                        continue
                                }

                                // Make the PATCH request to update the nodepool
                                result := clientset.RESTClient().Patch(types.MergePatchType).
                                AbsPath(fmt.Sprintf("/apis/karpenter.sh/v1/nodepools/%s", pool.Metadata.Name)).
                                Body(patchBytes).
                                Do(context.TODO())
        
                                if err := result.Error(); err != nil {
                                        log.Printf("[updateKarpenterNodePool] Failed to update node pool %s: %v", 
                                                pool.Metadata.Name, err)
                                } else {
                                        log.Printf("[updateKarpenterNodePool] Successfully added zone requirement to node pool %s", 
                                                pool.Metadata.Name)
                                }

                                const (
                                        zoneAwayAnnotation = "zonal-autoshift.eks.amazonaws.com/away-zones"
                                )

                                // After successful requirements update, update the annotation
                                annotationPatch := map[string]interface{}{
                                        "metadata": map[string]interface{}{
                                                "annotations": map[string]interface{}{
                                                        zoneAwayAnnotation: awayFrom,
                                                },
                                        },
                                }
                                annotationPatchBytes, err := json.Marshal(annotationPatch)
                                if err != nil {
                                        log.Printf("[updateKarpenterNodePool] Failed to marshal annotation patch: %v", err)
                                        continue
                                }

                                // Apply the annotation patch
                                annotationResult := clientset.RESTClient().Patch(types.MergePatchType).
                                AbsPath(fmt.Sprintf("/apis/karpenter.sh/v1/nodepools/%s", pool.Metadata.Name)).
                                Body(annotationPatchBytes).
                                Do(context.TODO())

                                if err := annotationResult.Error(); err != nil {
                                        log.Printf("[updateKarpenterNodePool] Failed to update node pool %s annotations: %v", 
                                                pool.Metadata.Name, err)
                                } else {
                                        log.Printf("[updateKarpenterNodePool] Successfully updated node pool %s annotations with away zone %s", 
                                                pool.Metadata.Name, event.Detail.AwayFrom)
                                }
                        }
                }
        }
}

// restoreKarpenterNodePool restores the Karpenter node pool when autoshift is completed
func restoreKarpenterNodePool(event Event) {
        // Get awayFrom from metadata if available, otherwise from detail
        awayFrom := event.Detail.AwayFrom
        if event.Detail.Metadata.AwayFrom != "" {
                awayFrom = event.Detail.Metadata.AwayFrom
        }
        
        log.Printf("[restoreKarpenterNodePool] Processing autoshift completed event for AZ: %s", awayFrom)

        k8sConfig, err := rest.InClusterConfig()
        if err != nil {
                log.Printf("[restoreKarpenterNodePool] Failed to create cluster config: %v", err)
                return
        }

        isAutoMode := listInstalledCRDs(k8sConfig)
        clientset, err := kubernetes.NewForConfig(k8sConfig)
        if err != nil {
                log.Printf("[restoreKarpenterNodePool] Failed to create clientset: %v", err)
                return
        }

        // Get zone name from zone ID (needed for both do-not-disrupt and zone restoration)
        awayZoneName := getZoneNameFromZoneId(awayFrom, event.Region)
        if awayZoneName == "" {
                log.Printf("[restoreKarpenterNodePool] Failed to get zone name for zone ID: %s, using zone ID as fallback", awayFrom)
                awayZoneName = awayFrom
        }

        // Check if do-not-disrupt feature is enabled
        if doNotDisruptEnabled {
                log.Println("[restoreKarpenterNodePool] do-not-disrupt feature enabled")

                // Use global controller to remove protection from nodes
                if globalNodeController != nil {
                        if err := globalNodeController.RemoveProtectionFromNodes(context.TODO(), awayZoneName); err != nil {
                                // This is expected if no Karpenter nodes exist in the zone
                                log.Printf("[restoreKarpenterNodePool] Note: %v (this is normal if no Karpenter nodes exist in this zone)", err)
                        } else {
                                log.Printf("[restoreKarpenterNodePool] Successfully removed protection from nodes in AZ %s", awayZoneName)
                        }
                } else {
                        log.Printf("[restoreKarpenterNodePool] NodeClaim controller not initialized, skipping node restoration")
                }
        } else {
                log.Println("[restoreKarpenterNodePool] do-not-disrupt feature disabled, skipping node restoration")
        }

        nodePools, err := clientset.RESTClient().Get().AbsPath("/apis/karpenter.sh/v1/nodepools").DoRaw(context.TODO())
        if err != nil {
                log.Printf("[restoreKarpenterNodePool] Failed to get node pools: %v", err)
                return
        }

        var nodePoolList struct {
                Items []struct {
                        APIVersion string           `json:"apiVersion"`
                        Kind       string           `json:"kind"`
                        Metadata   NodePoolMetadata `json:"metadata"`
                        Spec struct {
                                Template struct {
                                        Spec struct {
                                                Requirements []struct {
                                                        Key      string   `json:"key"`
                                                        Operator string   `json:"operator"`
                                                        Values   []string `json:"values"`
                                                } `json:"requirements"`
                                        } `json:"spec"`
                                } `json:"template"`
                        } `json:"spec"`
                } `json:"items"`
        }

        if err := json.Unmarshal(nodePools, &nodePoolList); err != nil {
                log.Printf("[restoreKarpenterNodePool] Failed to parse node pools: %v", err)
                return
        }

        if isAutoMode {
                // Remove NoExecute taint from nodes in the restored zone only if do-not-disrupt is disabled
                // (taints were only added when do-not-disrupt was disabled)
                if !doNotDisruptEnabled && globalNodeController != nil {
                        if err := globalNodeController.RemoveTaintFromNodes(context.TODO(), awayZoneName); err != nil {
                                log.Printf("[restoreKarpenterNodePool] Error removing taint from nodes in AZ %s: %v", awayZoneName, err)
                        }
                }
                
                // Handle both temporary and custom node pools
                for _, pool := range nodePoolList.Items {
                        if pool.Metadata.Name == "general-purpose-kss" || pool.Metadata.Name == "system-kss" {
                                // Delete temporary node pools created during autoshift
                                log.Printf("[restoreKarpenterNodePool] Deleting temporary node pool: %s", pool.Metadata.Name)
                                result := clientset.RESTClient().Delete().
                                        AbsPath(fmt.Sprintf("/apis/karpenter.sh/v1/nodepools/%s", pool.Metadata.Name)).
                                        Do(context.TODO())
                                if err := result.Error(); err != nil {
                                        log.Printf("[restoreKarpenterNodePool] Failed to delete node pool %s: %v", pool.Metadata.Name, err)
                                } else {
                                        log.Printf("[restoreKarpenterNodePool] Successfully deleted node pool %s", pool.Metadata.Name)
                                }
                        } else if pool.Metadata.Annotations != nil {
                                // Restore custom node pools that were updated during autoshift
                                if _, exists := pool.Metadata.Annotations["zonal-autoshift.eks.amazonaws.com/away-zones"]; exists {
                                        log.Printf("[restoreKarpenterNodePool] Restoring custom node pool: %s", pool.Metadata.Name)
                                        
                                        for i, req := range pool.Spec.Template.Spec.Requirements {
                                                if req.Key == "topology.kubernetes.io/zone" {
                                                        // Add back the away zone
                                                        restoredZones := append(req.Values, awayZoneName)
                                                        pool.Spec.Template.Spec.Requirements[i].Values = restoredZones
                                                        
                                                        updatePayload := map[string]interface{}{
                                                                "spec": map[string]interface{}{
                                                                        "template": map[string]interface{}{
                                                                                "spec": map[string]interface{}{
                                                                                        "requirements": pool.Spec.Template.Spec.Requirements,
                                                                                },
                                                                        },
                                                                },
                                                        }
                                                        
                                                        patchBytes, err := json.Marshal(updatePayload)
                                                        if err != nil {
                                                                log.Printf("[restoreKarpenterNodePool] Failed to marshal update payload: %v", err)
                                                                continue
                                                        }
                                                        
                                                        result := clientset.RESTClient().Patch(types.MergePatchType).
                                                                AbsPath(fmt.Sprintf("/apis/karpenter.sh/v1/nodepools/%s", pool.Metadata.Name)).
                                                                Body(patchBytes).
                                                                Do(context.TODO())
                                                        
                                                        if err := result.Error(); err != nil {
                                                                log.Printf("[restoreKarpenterNodePool] Failed to restore zone to node pool %s: %v", pool.Metadata.Name, err)
                                                        } else {
                                                                log.Printf("[restoreKarpenterNodePool] Successfully restored zone %s to node pool %s", awayZoneName, pool.Metadata.Name)
                                                        }
                                                        
                                                        // Remove the annotation
                                                        annotationPatch := map[string]interface{}{
                                                                "metadata": map[string]interface{}{
                                                                        "annotations": map[string]interface{}{
                                                                                "zonal-autoshift.eks.amazonaws.com/away-zones": nil,
                                                                        },
                                                                },
                                                        }
                                                        annotationPatchBytes, _ := json.Marshal(annotationPatch)
                                                        clientset.RESTClient().Patch(types.MergePatchType).
                                                                AbsPath(fmt.Sprintf("/apis/karpenter.sh/v1/nodepools/%s", pool.Metadata.Name)).
                                                                Body(annotationPatchBytes).
                                                                Do(context.TODO())
                                                        
                                                        log.Printf("[restoreKarpenterNodePool] Removed away-zones annotation from node pool %s", pool.Metadata.Name)
                                                        break
                                                }
                                        }
                                }
                        }
                }
        } else {
                // Restore zones to node pools
                for _, pool := range nodePoolList.Items {
                        if pool.Metadata.Annotations != nil {
                                if awayZone, exists := pool.Metadata.Annotations["zonal-autoshift.eks.amazonaws.com/away-zones"]; exists {
                                        log.Printf("[restoreKarpenterNodePool] Restoring zone to node pool: %s", pool.Metadata.Name)
                                        
                                        for i, req := range pool.Spec.Template.Spec.Requirements {
                                                if req.Key == "topology.kubernetes.io/zone" {
                                                        // Add back the away zone
                                                        restoredZones := append(req.Values, awayZoneName)
                                                        pool.Spec.Template.Spec.Requirements[i].Values = restoredZones
                                                        
                                                        updatePayload := map[string]interface{}{
                                                                "spec": map[string]interface{}{
                                                                        "template": map[string]interface{}{
                                                                                "spec": map[string]interface{}{
                                                                                        "requirements": pool.Spec.Template.Spec.Requirements,
                                                                                },
                                                                        },
                                                                },
                                                        }
                                                        
                                                        patchBytes, err := json.Marshal(updatePayload)
                                                        if err != nil {
                                                                log.Printf("[restoreKarpenterNodePool] Failed to marshal update payload: %v", err)
                                                                continue
                                                        }
                                                        
                                                        result := clientset.RESTClient().Patch(types.MergePatchType).
                                                                AbsPath(fmt.Sprintf("/apis/karpenter.sh/v1/nodepools/%s", pool.Metadata.Name)).
                                                                Body(patchBytes).
                                                                Do(context.TODO())
                                                        
                                                        if err := result.Error(); err != nil {
                                                                log.Printf("[restoreKarpenterNodePool] Failed to restore zone to node pool %s: %v", pool.Metadata.Name, err)
                                                        } else {
                                                                log.Printf("[restoreKarpenterNodePool] Successfully restored zone %s to node pool %s", awayZoneName, pool.Metadata.Name)
                                                        }
                                                        
                                                        // Remove the annotation
                                                        annotationPatch := map[string]interface{}{
                                                                "metadata": map[string]interface{}{
                                                                        "annotations": map[string]interface{}{
                                                                                "zonal-autoshift.eks.amazonaws.com/away-zones": nil,
                                                                        },
                                                                },
                                                        }
                                                        annotationPatchBytes, _ := json.Marshal(annotationPatch)
                                                        clientset.RESTClient().Patch(types.MergePatchType).
                                                                AbsPath(fmt.Sprintf("/apis/karpenter.sh/v1/nodepools/%s", pool.Metadata.Name)).
                                                                Body(annotationPatchBytes).
                                                                Do(context.TODO())
                                                        
                                                        log.Printf("[restoreKarpenterNodePool] Removed away-zones annotation from node pool %s", pool.Metadata.Name)
                                                        break
                                                }
                                        }
                                        
                                        _ = awayZone // Suppress unused variable warning
                                }
                        }
                }
        }
}

