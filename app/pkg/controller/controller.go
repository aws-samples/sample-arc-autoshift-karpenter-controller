package controller

import (
	"context"
	"fmt"
	"log"
	"time"

	"project/zonal-shift/pkg/client"
	"project/zonal-shift/pkg/models"
	"project/zonal-shift/pkg/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// NodeClaimController manages NodeClaim resources and node operations
type NodeClaimController struct {
	dynamicClient dynamic.Interface
	clientset     kubernetes.Interface
	nodeStore     *models.NodeStore
	stopCh        chan struct{}
}

// NewNodeClaimController creates a new controller instance
func NewNodeClaimController(dynamicClient dynamic.Interface, clientset kubernetes.Interface) *NodeClaimController {
	return &NodeClaimController{
		dynamicClient: dynamicClient,
		clientset:     clientset,
		nodeStore:     models.NewNodeStore(),
		stopCh:        make(chan struct{}),
	}
}

// processNodeClaim handles a NodeClaim event
func (c *NodeClaimController) processNodeClaim(obj interface{}, isDelete bool) {
	// Convert unstructured object to NodeClaim
	unstructuredObj, ok := obj.(*unstructured.Unstructured)
	if !ok {
		log.Printf("[processNodeClaim] Error: Object is not an Unstructured type")
		return
	}

	// Extract metadata and status
	metadata, found, err := unstructured.NestedMap(unstructuredObj.Object, "metadata")
	if !found || err != nil {
		log.Printf("[processNodeClaim] Error: Could not find metadata in NodeClaim")
		return
	}

	// Get name for logging
	name, _, _ := unstructured.NestedString(metadata, "name")

	status, found, err := unstructured.NestedMap(unstructuredObj.Object, "status")
	if !found || err != nil {
		log.Printf("[processNodeClaim] Error: Could not find status in NodeClaim %s", name)
		return
	}

	// Get nodeName from status
	nodeName, ok := status["nodeName"].(string)
	if !ok || nodeName == "" {
		log.Printf("[processNodeClaim] Warning: NodeClaim %s has no node name assigned", name)
		return
	}

	// Get zone from labels
	labels, found, err := unstructured.NestedMap(metadata, "labels")
	if !found || err != nil {
		log.Printf("[processNodeClaim] Error: Could not find labels in NodeClaim metadata for %s", name)
		return
	}

	// Try to get AZ from topology.kubernetes.io/zone label
	az, ok := labels["topology.kubernetes.io/zone"].(string)
	if !ok || az == "" {
		// Try legacy label as fallback
		az, ok = labels["failure-domain.beta.kubernetes.io/zone"].(string)
		if !ok || az == "" {
			log.Printf("[processNodeClaim] Warning: Could not find availability zone label for node %s", nodeName)
		}
	}

	// Handle the event based on type
	if isDelete {
		c.handleNodeClaimDelete(nodeName)
	} else {
		c.handleNodeClaimCreate(nodeName, az)
	}
}

// handleNodeClaimCreate handles a NodeClaim creation event
func (c *NodeClaimController) handleNodeClaimCreate(nodeName, az string) {
	log.Printf("[handleNodeClaimCreate] Adding node %s in AZ %s", nodeName, az)
	c.nodeStore.AddNode(nodeName, az)
}

// handleNodeClaimDelete handles a NodeClaim deletion event
func (c *NodeClaimController) handleNodeClaimDelete(nodeName string) {
	log.Printf("[handleNodeClaimDelete] Removing node %s", nodeName)
	c.nodeStore.RemoveNode(nodeName)
}

// Run starts the controller
func (c *NodeClaimController) Run(ctx context.Context) error {
	log.Println("[Run] Starting NodeClaim controller...")

	// Create event handler
	eventHandler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.processNodeClaim(obj, false)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			// We only care about the new object state
			c.processNodeClaim(newObj, false)
		},
		DeleteFunc: func(obj interface{}) {
			c.processNodeClaim(obj, true)
		},
	}

	// Create and start the informer
	informer := client.CreateNodeClaimInformer(c.dynamicClient, eventHandler)

	// Start the informer
	go informer.Run(c.stopCh)

	// Wait for the informer's cache to sync
	if !cache.WaitForCacheSync(c.stopCh, informer.HasSynced) {
		return fmt.Errorf("timed out waiting for caches to sync")
	}

	log.Println("[Run] NodeClaim controller is running and watching for NodeClaim events")

	// Initialize with existing NodeClaims
	log.Println("[Run] Initializing store with existing NodeClaims...")
	go c.initializeStore(ctx)

	// Wait until context is done
	<-ctx.Done()
	close(c.stopCh)
	return nil
}

// initializeStore loads existing NodeClaims into the store
func (c *NodeClaimController) initializeStore(ctx context.Context) {
	// Create NodeClaim client
	nodeClaimClient := client.NewNodeClaimClient(c.dynamicClient)

	// Use exponential backoff to handle potential issues
	backoff := wait.Backoff{
		Duration: 1 * time.Second,
		Factor:   2.0,
		Steps:    5,
	}

	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		// List existing NodeClaims
		log.Printf("[initializeStore] Listing existing NodeClaims...")
		nodeClaims, err := nodeClaimClient.ListNodeClaims(ctx)
		if err != nil {
			log.Printf("[initializeStore] Error listing NodeClaims: %v, retrying...", err)
			return false, nil
		}

		log.Printf("[initializeStore] Found %d NodeClaims", len(nodeClaims))

		// Process each NodeClaim
		for _, nodeClaim := range nodeClaims {
			// Extract important info
			metadata := nodeClaim.ObjectMeta

			// Get node name from NodeClaim
			nodeName := nodeClaim.Status.NodeName
			if nodeName == "" {
				log.Printf("[initializeStore] Warning: NodeClaim %s has no node name assigned", metadata.Name)
				continue
			}

			// Try to get AZ from labels
			az := ""
			if labels := metadata.Labels; len(labels) > 0 {
				// Try to get AZ from topology.kubernetes.io/zone label
				if zoneVal, exists := labels["topology.kubernetes.io/zone"]; exists {
					az = zoneVal
				} else if zoneVal, exists := labels["failure-domain.beta.kubernetes.io/zone"]; exists {
					// Try legacy label as fallback
					az = zoneVal
				}
			}

			log.Printf("[initializeStore] Adding node %s in AZ %s to store", nodeName, az)
			c.nodeStore.AddNode(nodeName, az)
		}

		log.Printf("[initializeStore] Successfully initialized store with %d nodes", len(nodeClaims))
		return true, nil
	})

	if err != nil {
		log.Printf("[initializeStore] Failed to initialize store after retries: %v", err)
	}
}

// GetNodesByAZ returns nodes filtered by availability zone
func (c *NodeClaimController) GetNodesByAZ(az string) []models.NodeInfo {
	return c.nodeStore.GetNodesByAZ(az)
}

// GetAllNodes returns all tracked nodes
func (c *NodeClaimController) GetAllNodes() []models.NodeInfo {
	return c.nodeStore.GetAllNodes()
}

// AnnotateAndCordonNodes adds the do-not-disrupt annotation and cordons the specified nodes
func (c *NodeClaimController) AnnotateAndCordonNodes(ctx context.Context, az string) error {
	log.Printf("[AnnotateAndCordonNodes] Looking for nodes in AZ %s", az)
	
	// Get nodes by AZ
	nodes := c.GetNodesByAZ(az)
	if len(nodes) == 0 {
		log.Printf("[AnnotateAndCordonNodes] No nodes found in AZ %s", az)
		return fmt.Errorf("no nodes found in availability zone %s", az)
	}

	log.Printf("[AnnotateAndCordonNodes] Found %d nodes in AZ %s", len(nodes), az)

	// Process nodes in batches
	return utils.BatchProcessor(ctx, nodes, func(ctx context.Context, batch []models.NodeInfo) error {
		for _, node := range batch {
			// Skip if node name is empty
			if node.NodeName == "" {
				continue
			}

			log.Printf("[AnnotateAndCordonNodes] Processing node %s", node.NodeName)

			// Step 1: Add the annotation
			err := c.annotateNode(ctx, node.NodeName)
			if err != nil {
				log.Printf("[AnnotateAndCordonNodes] Error annotating node %s: %v", node.NodeName, err)
				continue
			}
			log.Printf("[AnnotateAndCordonNodes] Successfully added do-not-disrupt annotation to node %s", node.NodeName)

			// Step 2: Cordon the node
			err = c.cordonNode(ctx, node.NodeName)
			if err != nil {
				log.Printf("[AnnotateAndCordonNodes] Error cordoning node %s: %v", node.NodeName, err)
				continue
			}
			log.Printf("[AnnotateAndCordonNodes] Successfully cordoned node %s", node.NodeName)

			log.Printf("[AnnotateAndCordonNodes] Successfully annotated and cordoned node %s", node.NodeName)
		}
		return nil
	})
}

// annotateNode adds the do-not-disrupt annotation to a node
func (c *NodeClaimController) annotateNode(ctx context.Context, nodeName string) error {
	// Get the current node
	node, err := c.clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// Add the annotation
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}
	node.Annotations["karpenter.sh/do-not-disrupt"] = "true"

	// Update the node
	_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	return err
}

// cordonNode marks a node as unschedulable
func (c *NodeClaimController) cordonNode(ctx context.Context, nodeName string) error {
	// Get the current node
	node, err := c.clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// Set unschedulable to true
	node.Spec.Unschedulable = true

	// Update the node
	_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	return err
}

// RemoveProtectionFromNodes removes the do-not-disrupt annotation and uncordons the specified nodes
func (c *NodeClaimController) RemoveProtectionFromNodes(ctx context.Context, az string) error {
	log.Printf("[RemoveProtectionFromNodes] Looking for nodes in AZ %s", az)
	
	// Get nodes by AZ
	nodes := c.GetNodesByAZ(az)
	if len(nodes) == 0 {
		log.Printf("[RemoveProtectionFromNodes] No nodes found in AZ %s", az)
		return fmt.Errorf("no nodes found in availability zone %s", az)
	}

	log.Printf("[RemoveProtectionFromNodes] Found %d nodes in AZ %s", len(nodes), az)

	// Process nodes in batches
	return utils.BatchProcessor(ctx, nodes, func(ctx context.Context, batch []models.NodeInfo) error {
		for _, node := range batch {
			// Skip if node name is empty
			if node.NodeName == "" {
				continue
			}

			log.Printf("[RemoveProtectionFromNodes] Processing node %s", node.NodeName)

			// Step 1: Remove the annotation
			err := c.removeAnnotation(ctx, node.NodeName)
			if err != nil {
				log.Printf("[RemoveProtectionFromNodes] Error removing annotation from node %s: %v", node.NodeName, err)
				continue
			}
			log.Printf("[RemoveProtectionFromNodes] Successfully removed do-not-disrupt annotation from node %s", node.NodeName)

			// Step 2: Uncordon the node
			err = c.uncordonNode(ctx, node.NodeName)
			if err != nil {
				log.Printf("[RemoveProtectionFromNodes] Error uncordoning node %s: %v", node.NodeName, err)
				continue
			}
			log.Printf("[RemoveProtectionFromNodes] Successfully uncordoned node %s", node.NodeName)

			log.Printf("[RemoveProtectionFromNodes] Successfully removed protection from node %s", node.NodeName)
		}
		return nil
	})
}

// removeAnnotation removes the do-not-disrupt annotation from a node
func (c *NodeClaimController) removeAnnotation(ctx context.Context, nodeName string) error {
	// Get the current node
	node, err := c.clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// Check if annotations exist
	if node.Annotations == nil {
		// No annotations to remove
		return nil
	}

	// Delete the annotation if it exists
	_, exists := node.Annotations["karpenter.sh/do-not-disrupt"]
	if !exists {
		// Annotation doesn't exist, nothing to do
		return nil
	}

	// Remove the annotation
	delete(node.Annotations, "karpenter.sh/do-not-disrupt")

	// Update the node
	_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	return err
}

// uncordonNode marks a node as schedulable
func (c *NodeClaimController) uncordonNode(ctx context.Context, nodeName string) error {
	// Get the current node
	node, err := c.clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// Check if the node is already schedulable
	if !node.Spec.Unschedulable {
		// Node is already schedulable, nothing to do
		return nil
	}

	// Set unschedulable to false
	node.Spec.Unschedulable = false

	// Update the node
	_, err = c.clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	return err
}