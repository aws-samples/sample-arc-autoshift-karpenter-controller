package controller

import (
	"context"
	"testing"

	"project/zonal-shift/pkg/models"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestNodeClaimController tests the basic functionality of the NodeClaimController
func TestNodeClaimController(t *testing.T) {
	// Create a fake clientset
	clientset := fake.NewSimpleClientset()

	// Create a controller with the fake clientset
	controller := &NodeClaimController{
		clientset: clientset,
		nodeStore: models.NewNodeStore(),
		stopCh:    make(chan struct{}),
	}

	// Test adding nodes
	testCases := []struct {
		nodeName string
		az       string
	}{
		{"node-1", "us-west-2a"},
		{"node-2", "us-west-2b"},
		{"node-3", "us-west-2c"},
		{"node-4", "us-west-2a"},
	}

	for _, tc := range testCases {
		controller.handleNodeClaimCreate(tc.nodeName, tc.az)
	}

	// Test filtering by AZ
	nodes := controller.GetNodesByAZ("us-west-2a")
	if len(nodes) != 2 {
		t.Errorf("Expected 2 nodes in us-west-2a, got %d", len(nodes))
	}

	nodes = controller.GetNodesByAZ("us-west-2b")
	if len(nodes) != 1 {
		t.Errorf("Expected 1 node in us-west-2b, got %d", len(nodes))
	}

	// Test getting all nodes
	allNodes := controller.GetAllNodes()
	if len(allNodes) != 4 {
		t.Errorf("Expected 4 nodes total, got %d", len(allNodes))
	}

	// Test additional node filtering methods
	moreNodes := controller.GetNodesByAZ("us-west-2a")
	if len(moreNodes) != 2 {
		t.Errorf("Expected 2 nodes in us-west-2a, got %d", len(moreNodes))
	}

	moreAllNodes := controller.GetAllNodes()
	if len(moreAllNodes) != 4 {
		t.Errorf("Expected 4 nodes total, got %d", len(moreAllNodes))
	}

	// Test removing a node
	controller.handleNodeClaimDelete("node-1")
	controller.handleNodeClaimDelete("node-2")

	// Verify removal
	allNodes = controller.GetAllNodes()
	if len(allNodes) != 2 {
		t.Errorf("Expected 2 nodes after removal, got %d", len(allNodes))
	}
}

// TestAnnotateAndCordonNodes tests the node annotation and cordoning functionality
func TestAnnotateAndCordonNodes(t *testing.T) {
	// Create test nodes in the fake client
	node1 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node-1",
		},
	}

	node2 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node-2",
		},
	}

	// Create a fake clientset with the test nodes
	clientset := fake.NewSimpleClientset(node1, node2)

	// Create the controller with the fake clientset
	controller := &NodeClaimController{
		clientset: clientset,
		nodeStore: models.NewNodeStore(),
	}

	// Add the nodes to the store
	controller.handleNodeClaimCreate("test-node-1", "us-west-2a")
	controller.handleNodeClaimCreate("test-node-2", "us-west-2a")

	// Test annotating and cordoning a single node
	err := controller.annotateNode(context.Background(), "test-node-1")
	if err != nil {
		t.Errorf("Error annotating node: %v", err)
	}

	// Verify the annotation was added
	updatedNode, err := clientset.CoreV1().Nodes().Get(context.Background(), "test-node-1", metav1.GetOptions{})
	if err != nil {
		t.Errorf("Error getting node: %v", err)
	}

	if updatedNode.Annotations["karpenter.sh/do-not-disrupt"] != "true" {
		t.Errorf("Expected node to have do-not-disrupt annotation")
	}

	// Test cordoning the node
	err = controller.cordonNode(context.Background(), "test-node-1")
	if err != nil {
		t.Errorf("Error cordoning node: %v", err)
	}

	// Verify the node was cordoned
	updatedNode, err = clientset.CoreV1().Nodes().Get(context.Background(), "test-node-1", metav1.GetOptions{})
	if err != nil {
		t.Errorf("Error getting node: %v", err)
	}

	if !updatedNode.Spec.Unschedulable {
		t.Errorf("Expected node to be unschedulable (cordoned)")
	}

	// Test the full annotate and cordon function for a specific AZ
	err = controller.AnnotateAndCordonNodes(context.Background(), "us-west-2a")
	if err != nil {
		t.Errorf("Error in AnnotateAndCordonNodes: %v", err)
	}

	// Verify both nodes were updated
	for _, nodeName := range []string{"test-node-1", "test-node-2"} {
		node, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		if err != nil {
			t.Errorf("Error getting node %s: %v", nodeName, err)
			continue
		}

		if node.Annotations["karpenter.sh/do-not-disrupt"] != "true" {
			t.Errorf("Node %s should have do-not-disrupt annotation", nodeName)
		}

		if !node.Spec.Unschedulable {
			t.Errorf("Node %s should be cordoned", nodeName)
		}
	}
}

// TestRemoveProtectionFromNodes tests removing annotation and uncordoning nodes
func TestRemoveProtectionFromNodes(t *testing.T) {
	// Create test nodes in the fake client with annotations and unschedulable set
	node1 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node-1",
			Annotations: map[string]string{
				"karpenter.sh/do-not-disrupt": "true",
			},
		},
		Spec: corev1.NodeSpec{
			Unschedulable: true,
		},
	}

	node2 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node-2",
			Annotations: map[string]string{
				"karpenter.sh/do-not-disrupt": "true",
			},
		},
		Spec: corev1.NodeSpec{
			Unschedulable: true,
		},
	}

	// Create a fake clientset with the test nodes
	clientset := fake.NewSimpleClientset(node1, node2)

	// Create the controller with the fake clientset
	controller := &NodeClaimController{
		clientset: clientset,
		nodeStore: models.NewNodeStore(),
	}

	// Add the nodes to the store
	controller.handleNodeClaimCreate("test-node-1", "us-west-2a")
	controller.handleNodeClaimCreate("test-node-2", "us-west-2a")

	// Test removing annotation from a single node
	err := controller.removeAnnotation(context.Background(), "test-node-1")
	if err != nil {
		t.Errorf("Error removing annotation: %v", err)
	}

	// Verify the annotation was removed
	updatedNode, err := clientset.CoreV1().Nodes().Get(context.Background(), "test-node-1", metav1.GetOptions{})
	if err != nil {
		t.Errorf("Error getting node: %v", err)
	}

	if _, exists := updatedNode.Annotations["karpenter.sh/do-not-disrupt"]; exists {
		t.Errorf("Expected node to not have do-not-disrupt annotation")
	}

	// Test uncordoning the node
	err = controller.uncordonNode(context.Background(), "test-node-1")
	if err != nil {
		t.Errorf("Error uncordoning node: %v", err)
	}

	// Verify the node was uncordoned
	updatedNode, err = clientset.CoreV1().Nodes().Get(context.Background(), "test-node-1", metav1.GetOptions{})
	if err != nil {
		t.Errorf("Error getting node: %v", err)
	}

	if updatedNode.Spec.Unschedulable {
		t.Errorf("Expected node to be schedulable (uncordoned)")
	}

	// Test the full remove protection function for a specific AZ
	err = controller.RemoveProtectionFromNodes(context.Background(), "us-west-2a")
	if err != nil {
		t.Errorf("Error in RemoveProtectionFromNodes: %v", err)
	}

	// Verify both nodes were updated
	for _, nodeName := range []string{"test-node-1", "test-node-2"} {
		node, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		if err != nil {
			t.Errorf("Error getting node %s: %v", nodeName, err)
			continue
		}

		if _, exists := node.Annotations["karpenter.sh/do-not-disrupt"]; exists {
			t.Errorf("Node %s should not have do-not-disrupt annotation", nodeName)
		}

		if node.Spec.Unschedulable {
			t.Errorf("Node %s should be schedulable (uncordoned)", nodeName)
		}
	}
}