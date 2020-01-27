package rkeworker

import (
	"context"
	"sync"
	"time"

	"github.com/rancher/rancher/pkg/randomtoken"

	"github.com/rancher/rancher/pkg/ticker"
	"github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/runtime"
)

type upgradeHandler struct {
	clusterLister v3.ClusterLister
	clusters      v3.ClusterInterface
	ctx           context.Context
}

var (
	interval       = 30 * time.Second
	clusterMapData = map[string]context.CancelFunc{}
	clusterLock    = sync.Mutex{}
)

func Register(ctx context.Context, mgmt *config.ManagementContext) {
	logrus.Infof("registering management upgrade handler for rke worker nodes")
	uh := upgradeHandler{
		clusters:      mgmt.Management.Clusters(""),
		clusterLister: mgmt.Management.Clusters("").Controller().Lister(),
		ctx:           ctx,
	}

	mgmt.Management.Clusters("").Controller().AddHandler(ctx, "rkeworkerupgradehandler", uh.Sync)

	clusterMapData = map[string]context.CancelFunc{}
}

func (uh *upgradeHandler) Sync(key string, cluster *v3.Cluster) (runtime.Object, error) {
	if cluster == nil || cluster.DeletionTimestamp != nil {
		return cluster, nil
	}

	if cluster.Status.AppliedSpec.RancherKubernetesEngineConfig == nil {
		return cluster, nil
	}

	nodeUpgradeStatus := cluster.Status.NodeUpgradeStatus

	if nodeUpgradeStatus == nil || nodeUpgradeStatus.CurrentToken == "" {
		stopTicker(cluster.Name)
		return cluster, nil
	}

	// cluster is undergoing an upgrade, start ticker if required
	uh.startTicker(uh.ctx, cluster.Name)

	return cluster, nil
}

func (uh *upgradeHandler) startTicker(ctx context.Context, cName string) {
	defer clusterLock.Unlock()
	clusterLock.Lock()

	_, ok := clusterMapData[cName]
	if ok {
		return
	}

	cctx, cancel := context.WithCancel(ctx)
	clusterMapData[cName] = cancel

	go uh.start(cctx, cName)
}

func (uh *upgradeHandler) start(ctx context.Context, cName string) {
	for range ticker.Context(ctx, interval) {
		logrus.Infof("upgradeHandler ticker enqueue [%s]", cName)

		cluster, err := uh.clusterLister.Get("", cName)
		if err != nil {
			logrus.Errorf("cluster errror %s", cName)
		}

		clusterCopy := cluster.DeepCopy()

		tok, err := randomtoken.Generate()
		if err != nil {
			logrus.Errorf("TOken err %s", tok)
		}
		clusterCopy.Annotations["KINARA"] = tok

		if _, err := uh.clusters.Update(clusterCopy); err != nil {
			logrus.Errorf("updatess err %v", err)
		}

		//uh.clusters.Controller().Enqueue("", cName)
	}
}

func stopTicker(cName string) {
	defer clusterLock.Unlock()
	clusterLock.Lock()

	cancelFunc, ok := clusterMapData[cName]
	if !ok {
		return
	}

	logrus.Infof("upgradeHandler stopping ticker for cluster [%s]", cName)

	if cancelFunc != nil {
		cancelFunc()
	}

	delete(clusterMapData, cName)
	return
}
