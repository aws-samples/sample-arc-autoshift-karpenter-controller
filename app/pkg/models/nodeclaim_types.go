package models

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeClaimStatus defines the observed state of NodeClaim
type NodeClaimStatus struct {
	// NodeName is the name of the corresponding node object
	NodeName string `json:"nodeName,omitempty"`
}

// NodeClaim is the Schema for the NodeClaims API
type NodeClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Name is the name of the NodeClaim
	Name   string           `json:"name,omitempty"`
	Status NodeClaimStatus `json:"status,omitempty"`
}

// NodeClaimList contains a list of NodeClaim
type NodeClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeClaim `json:"items"`
}