package models

import (
	"testing"
)

// TestNodeStore tests the functionality of the NodeStore
func TestNodeStore(t *testing.T) {
	store := NewNodeStore()

	// Test adding nodes
	store.AddNode("node-1", "us-west-2a")
	store.AddNode("node-2", "us-west-2b")
	store.AddNode("node-3", "us-west-2a")

	// Test getting nodes by AZ
	nodes := store.GetNodesByAZ("us-west-2a")
	if len(nodes) != 2 {
		t.Errorf("Expected 2 nodes in us-west-2a, got %d", len(nodes))
	}

	// Test filtering by non-existent AZ
	nodes = store.GetNodesByAZ("us-west-2d")
	if len(nodes) != 0 {
		t.Errorf("Expected 0 nodes in us-west-2d, got %d", len(nodes))
	}

	// Test getting all nodes
	allNodes := store.GetAllNodes()
	if len(allNodes) != 3 {
		t.Errorf("Expected 3 nodes total, got %d", len(allNodes))
	}

	// Test updating a node
	store.AddNode("node-2", "us-west-2c") // Change AZ
	nodes = store.GetNodesByAZ("us-west-2b")
	if len(nodes) != 0 {
		t.Errorf("Expected 0 nodes in us-west-2b after update, got %d", len(nodes))
	}

	nodes = store.GetNodesByAZ("us-west-2c")
	if len(nodes) != 1 {
		t.Errorf("Expected 1 node in us-west-2c after update, got %d", len(nodes))
	}

	// Test removing a node
	store.RemoveNode("node-1")
	allNodes = store.GetAllNodes()
	if len(allNodes) != 2 {
		t.Errorf("Expected 2 nodes total after removal, got %d", len(allNodes))
	}

	// Test removing a non-existent node (should not error)
	store.RemoveNode("non-existent")
	allNodes = store.GetAllNodes()
	if len(allNodes) != 2 {
		t.Errorf("Expected still 2 nodes total, got %d", len(allNodes))
	}
}