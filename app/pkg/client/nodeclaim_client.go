package client

import (
	"context"

	"project/zonal-shift/pkg/models"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

// NodeClaimResource defines the GVR for the NodeClaim resource
var NodeClaimResource = schema.GroupVersionResource{
	Group:    "karpenter.sh",
	Version:  "v1",
	Resource: "nodeclaims",
}

// NodeClaimClient provides methods to access NodeClaim resources
type NodeClaimClient struct {
	dynamicClient dynamic.Interface
}

// NewNodeClaimClient creates a new NodeClaimClient
func NewNodeClaimClient(dynamicClient dynamic.Interface) *NodeClaimClient {
	return &NodeClaimClient{
		dynamicClient: dynamicClient,
	}
}

// ConvertToNodeClaim converts an unstructured resource to a NodeClaim
func ConvertToNodeClaim(obj *unstructured.Unstructured) (*models.NodeClaim, error) {
	nodeClaim := &models.NodeClaim{}
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.UnstructuredContent(), nodeClaim)
	if err != nil {
		return nil, err
	}
	return nodeClaim, nil
}

// CreateNodeClaimInformer creates an informer for NodeClaim resources
func CreateNodeClaimInformer(dynamicClient dynamic.Interface, handler cache.ResourceEventHandler) cache.SharedIndexInformer {
	// Create a factory for dynamic informers
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dynamicClient, 0, metav1.NamespaceAll, nil)

	// Create an informer for NodeClaims
	informer := factory.ForResource(NodeClaimResource).Informer()

	// Add event handlers
	_, err := informer.AddEventHandler(handler)
	if err != nil {
		// Log the error - in a real application, handle this properly
		panic(err)
	}

	return informer
}

// ListNodeClaims lists all NodeClaims in the cluster
func (c *NodeClaimClient) ListNodeClaims(ctx context.Context) ([]models.NodeClaim, error) {
	list, err := c.dynamicClient.Resource(NodeClaimResource).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var nodeClaims []models.NodeClaim
	for _, item := range list.Items {
		nodeClaim, err := ConvertToNodeClaim(&item)
		if err != nil {
			return nil, err
		}
		nodeClaims = append(nodeClaims, *nodeClaim)
	}

	return nodeClaims, nil
}

// GetNodeClaim gets a NodeClaim by name
func (c *NodeClaimClient) GetNodeClaim(ctx context.Context, name string) (*models.NodeClaim, error) {
	result, err := c.dynamicClient.Resource(NodeClaimResource).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return ConvertToNodeClaim(result)
}