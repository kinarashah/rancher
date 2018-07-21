package networkpolicy

import (
	"github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/sirupsen/logrus"
)

type clusterNetPolHandler struct {
}

func (ch *clusterNetPolHandler) Sync(key string, cluster *v3.Cluster) error {
	if cluster == nil || cluster.DeletionTimestamp != nil ||
		!v3.ClusterConditionReady.IsTrue(cluster) ||
		cluster.Spec.EnableNetworkPolicy == cluster.Status.AppliedEnableNetworkPolicy {
		return nil
	}

	logrus.Info("enter")
	logrus.Info(cluster.Spec.EnableNetworkPolicy)
	return nil
}
