package models

import (
	"sync"
)

// NodeInfo stores information about a Kubernetes node
type NodeInfo struct {
	NodeName         string
	AvailabilityZone string
}

// NodeStore holds all the Kubernetes nodes being tracked
type NodeStore struct {
	nodes map[string]NodeInfo // Key is nodeName
	mu    sync.RWMutex
}

// NewNodeStore initializes a new NodeStore
func NewNodeStore() *NodeStore {
	return &NodeStore{
		nodes: make(map[string]NodeInfo),
	}
}

// AddNode adds a node to the store
func (s *NodeStore) AddNode(nodeName, az string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[nodeName] = NodeInfo{
		NodeName:         nodeName,
		AvailabilityZone: az,
	}
}

// RemoveNode removes a node from the store
func (s *NodeStore) RemoveNode(nodeName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.nodes, nodeName)
}

// GetNodesByAZ returns nodes filtered by availability zone
func (s *NodeStore) GetNodesByAZ(az string) []NodeInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []NodeInfo

	// If empty AZ is provided, return all nodes
	if az == "" {
		for _, info := range s.nodes {
			result = append(result, info)
		}
		return result
	}

	// Filter by AZ
	for _, info := range s.nodes {
		if info.AvailabilityZone == az {
			result = append(result, info)
		}
	}

	return result
}

// GetAllNodes returns all nodes
func (s *NodeStore) GetAllNodes() []NodeInfo {
	return s.GetNodesByAZ("")
}