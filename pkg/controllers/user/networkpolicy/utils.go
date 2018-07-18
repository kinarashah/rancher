package networkpolicy

import (
	"fmt"
	"strings"

	"github.com/rancher/rancher/pkg/settings"
	"github.com/rancher/types/apis/management.cattle.io/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func GetSystemNamespaces() map[string]bool {
	systemNamespaces := make(map[string]bool)
	systemNamespacesStr := settings.SystemNamespaces.Get()
	if systemNamespacesStr != "" {
		splits := strings.Split(systemNamespacesStr, ",")
		for _, s := range splits {
			systemNamespaces[strings.TrimSpace(s)] = true
		}
	}
	return systemNamespaces
}

func isNetworkPolicyDisabled(clusterNamespace string, clusterLister v3.ClusterLister) (bool, error) {
	cluster, err := clusterLister.Get("", clusterNamespace)
	if err != nil && apierrors.IsNotFound(err) {
		return false, fmt.Errorf("error getting cluster %v", err)
	}
	return !cluster.Status.AppliedEnableNetworkPolicy, nil
}
