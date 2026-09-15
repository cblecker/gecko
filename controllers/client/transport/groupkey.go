package transport

import "fmt"

// ClusterGroupKey returns the Firestore grouping key for a HostedCluster reconciliation.
// Format: projects/{namespace}/clusters/{clusterName}
func ClusterGroupKey(namespace, clusterName string) (string, error) {
	if namespace == "" {
		return "", fmt.Errorf("namespace must not be empty")
	}
	if clusterName == "" {
		return "", fmt.Errorf("clusterName must not be empty")
	}
	return fmt.Sprintf("projects/%s/clusters/%s", namespace, clusterName), nil
}

// NodePoolGroupKey returns the Firestore grouping key for a NodePool reconciliation.
// Format: projects/{namespace}/clusters/{clusterName}/nodepools/{nodePoolName}
func NodePoolGroupKey(namespace, clusterName, nodePoolName string) (string, error) {
	if namespace == "" {
		return "", fmt.Errorf("namespace must not be empty")
	}
	if clusterName == "" {
		return "", fmt.Errorf("clusterName must not be empty")
	}
	if nodePoolName == "" {
		return "", fmt.Errorf("nodePoolName must not be empty")
	}
	return fmt.Sprintf("projects/%s/clusters/%s/nodepools/%s", namespace, clusterName, nodePoolName), nil
}
