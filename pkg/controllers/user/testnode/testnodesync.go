package testnode

import (
	"context"
	"fmt"
	"sync"

	"github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type NodeClusterSyncer struct {
	clusterLister v3.ClusterLister
	clusters      v3.ClusterInterface
	nodes         v3.NodeInterface
	nodesLister   v3.NodeLister
}

var test sync.Mutex

func Register(ctx context.Context, cluster *config.UserContext) {
	nh := &NodeClusterSyncer{
		clusterLister: cluster.Management.Management.Clusters("").Controller().Lister(),
		clusters:      cluster.Management.Management.Clusters(""),

		nodesLister: cluster.Management.Management.Nodes("").Controller().Lister(),
		nodes:       cluster.Management.Management.Nodes(""),
	}

	cluster.Management.Management.Clusters("").Controller().AddHandler(ctx, "clusterSync", nh.clusterSync)

	cluster.Management.Management.Nodes("").Controller().AddHandler(ctx, "nodeSync", nh.nodeSync)
}

func (nh *NodeClusterSyncer) clusterSync(key string, cluster *v3.Cluster) (runtime.Object, error) {
	if cluster.Name == "local" {
		return cluster, nil
	}
	logrus.Infof("receive cluster %s <- %v", cluster.Name, cluster.Annotations["kinara"])
	return cluster, nil
}

func (nh *NodeClusterSyncer) nodeSync(key string, node *v3.Node) (runtime.Object, error) {
	if node.Namespace == "local" {
		return node, nil
	}

	logrus.Infof("receive %s %s %s", key, node.Name, node.Namespace)

	test.Lock()
	for i := 0; i < 10; i++ {
		cluster, err := nh.clusters.Get(node.Namespace, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}

		clusterCopy := cluster.DeepCopy()
		clusterCopy.Annotations["kinara"] = fmt.Sprintf("%v", i)

		if _, err := nh.clusters.Update(clusterCopy); err != nil {
			logrus.Infof("error updating %v", err)
			return nil, err
		}

		logrus.Infof("updating cluster %s -> %v", node.Namespace, i)
	}

	test.Unlock()
	//nh.clusters.Controller().Enqueue("", node.Namespace)

	return node, nil
}
