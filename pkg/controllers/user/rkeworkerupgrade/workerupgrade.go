package rkeworkerupgrade

import (
	"context"
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/docker/docker/api/types"
	hash "github.com/mitchellh/hashstructure"
	"github.com/rancher/norman/types/convert"
	kd "github.com/rancher/rancher/pkg/controllers/management/kontainerdrivermetadata"
	"github.com/rancher/rancher/pkg/librke"
	rkeservices "github.com/rancher/rke/services"
	"github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

type upgradeHandler struct {
	nodesLister          v3.NodeLister
	nodes                v3.NodeInterface
	clusters             v3.ClusterInterface
	serviceOptionsLister v3.RKEK8sServiceOptionLister
	serviceOptions       v3.RKEK8sServiceOptionInterface
	sysImagesLister      v3.RKEK8sSystemImageLister
	sysImages            v3.RKEK8sSystemImageInterface
	ctx                  context.Context
}

var (
	localCache map[string]string
)

func Register(ctx context.Context, cluster *config.UserContext) {
	logrus.Infof("Registering upgradeHandler for upgrading worker nodes")
	uh := upgradeHandler{
		nodesLister:          cluster.Management.Management.Nodes("").Controller().Lister(),
		nodes:                cluster.Management.Management.Nodes(""),
		clusters:             cluster.Management.Management.Clusters(""),
		serviceOptionsLister: cluster.Management.Management.RKEK8sServiceOptions("").Controller().Lister(),
		serviceOptions:       cluster.Management.Management.RKEK8sServiceOptions(""),
		sysImagesLister:      cluster.Management.Management.RKEK8sSystemImages("").Controller().Lister(),
		sysImages:            cluster.Management.Management.RKEK8sSystemImages(""),
		ctx:                  ctx,
	}
	localCache = map[string]string{}
	cluster.Management.Management.Clusters("").AddHandler(ctx, "workerUpgradeHandler", uh.Sync)
}

func (uh *upgradeHandler) Sync(key string, cluster *v3.Cluster) (runtime.Object, error) {
	if cluster == nil || cluster.DeletionTimestamp != nil {
		return cluster, nil
	}
	logrus.Infof("kinara: received sync for %s", cluster.Name)

	if cluster.Status.AppliedSpec.RancherKubernetesEngineConfig == nil {
		return cluster, nil
	}

	nodeUpgradeStatus := cluster.Status.NodeUpgradeStatus

	if nodeUpgradeStatus == nil {
		if !v3.ClusterConditionReady.IsTrue(cluster) {
			return cluster, nil
		}

		nodes, err := uh.nodesLister.List(cluster.Name, labels.Everything())
		if err != nil {
			return nil, err
		}

		done := 0
		for _, node := range nodes {

			nodeGood := v3.NodeConditionRegistered.IsTrue(node) && v3.NodeConditionProvisioned.IsTrue(node) &&
				!v3.NodeConditionReady.IsUnknown(node) && node.DeletionTimestamp == nil

			if !nodeGood {
				logrus.Infof("nodeNotGoodForNil %s", node.Name)
				return cluster, nil
			}
			done += 1
		}

		if done == len(nodes) {
			clusterHash, err := uh.getClusterHash(cluster)
			if err != nil {
				return nil, err
			}
			clusterObj := cluster.DeepCopy()
			clusterObj.Status.NodeUpgradeStatus = &v3.NodeUpgradeStatus{
				LastAppliedToken: clusterHash,
				Nodes:            map[string]map[string]string{},
			}

			upd, err := uh.clusters.Update(clusterObj)
			if err != nil {
				logrus.Infof("nilUpgrade err updating obj %v", err)
				return nil, err
			}

			logrus.Infof("nodeUpgradeNil done %v len(nodes) %v", done, len(nodes))
			return upd, nil
		}

		return cluster, nil
	}

	oldHash := cluster.Status.NodeUpgradeStatus.LastAppliedToken
	currHash, err := uh.getClusterHash(cluster)
	if err != nil {
		logrus.Infof("error getting clusterHash %v", err)
		return nil, err
	}
	if oldHash == currHash {
		return cluster, nil
	}

	if currHash != cluster.Status.NodeUpgradeStatus.CurrentToken {
		logrus.Infof("hash changed old: %s new: %s, now updating cluster object with it", oldHash, currHash)

		clusterObj := cluster.DeepCopy()
		clusterObj.Status.NodeUpgradeStatus.CurrentToken = currHash
		if clusterObj.Status.NodeUpgradeStatus.Nodes == nil {
			clusterObj.Status.NodeUpgradeStatus.Nodes = map[string]map[string]string{}
		}
		clusterObj.Status.NodeUpgradeStatus.Nodes[currHash] = map[string]string{}

		cluster, err = uh.clusters.Update(clusterObj)
		if err != nil {
			logrus.Infof("clusterUpdateObj err updating obj %v", err)
			return nil, err
		}
	}

	nodes, err := uh.nodesLister.List(cluster.Name, labels.Everything())
	if err != nil {
		return nil, err
	}
	logrus.Infof("found nodesList: length %v", len(nodes))
	processing, done, nonWorker, toUpdate := getProcessingNumNodes(nodes, localCache, cluster.Status.NodeUpgradeStatus.Nodes[currHash])

	maxUnavailable := &cluster.Spec.RancherKubernetesEngineConfig.NodeUpgradeStrategy.RollingUpdate.MaxUnavailable
	worker := len(nodes) - nonWorker
	maxAllowed, err := intstr.GetValueFromIntOrPercent(maxUnavailable, worker, false)
	if err != nil {
		logrus.Infof("getMaxAllowed err %v", err)
		return nil, err
	}

	logrus.Infof("processing %v maxAllowed %v nodeMap %v toUpdate %v", processing, maxAllowed, localCache, toUpdate)

	if processing == maxAllowed {
		logrus.Infof("equal toUpdate %v", toUpdate)
		if toUpdate {
			clusterObj := cluster.DeepCopy()
			if clusterObj.Status.NodeUpgradeStatus == nil {
				logrus.Infof("SHOULD NEVER BE NIL!")
				clusterObj.Status.NodeUpgradeStatus = &v3.NodeUpgradeStatus{
					Nodes: map[string]map[string]string{},
				}
			}

			if !reflect.DeepEqual(clusterObj.Status.NodeUpgradeStatus.Nodes[currHash], localCache) {
				clusterObj.Status.NodeUpgradeStatus.Nodes[currHash] = localCache
				cluster, err = uh.clusters.Update(clusterObj)
				if err != nil {
					logrus.Infof("update err %v", err)
					return nil, err
				}
			}
		}
		return cluster, nil
	}

	for _, node := range nodes {

		if processing == maxAllowed {
			break
		}

		if val, ok := localCache[node.Name]; ok {
			logrus.Infof("node already present in nodeMap %v %s", node.Name, val)

			if val == "upgraded" {
				logrus.Infof("activate the node %s", node.Name)
				nodeCopy := node.DeepCopy()

				nodeCopy.Spec.DesiredNodeUnschedulable = "false"

				if _, err := uh.nodes.Update(nodeCopy); err != nil {
					return nil, fmt.Errorf("error updating drain on node %v", node.Name)
				}

				localCache[node.Name] = "active"
			}

			continue
		}

		nodeGood := v3.NodeConditionRegistered.IsTrue(node) && v3.NodeConditionProvisioned.IsTrue(node) &&
			!v3.NodeConditionReady.IsUnknown(node) && node.DeletionTimestamp == nil

		if !nodeGood {
			logrus.Infof("node is not good! %s", node.Name)
			continue
		}

		if !workerOnly(node.Status.NodeConfig.Role) {
			logrus.Infof("skipping node: not worker %s", node.Name)
			continue
		}

		desiredField := node.Spec.DesiredNodeUnschedulable

		nodeCopy := node.DeepCopy()
		if maxAllowed == 1000 {
			if desiredField == "drain" || desiredField == "stopDrain" {
				logrus.Infof("WHYYY this node is already set to drain %v", node.Name)
				continue
			}

			nodeCopy.Spec.DesiredNodeUnschedulable = "drain"
			nodeCopy.Spec.NodeDrainInput = &v3.NodeDrainInput{
				Force:            true,
				IgnoreDaemonSets: true,
				DeleteLocalData:  true,
			}
		} else {
			if desiredField == "true" {
				logrus.Infof("WHYYY this node is already set to CORDON %v", node.Name)
				continue
			}

			nodeCopy.Spec.DesiredNodeUnschedulable = "true"
		}

		if _, err := uh.nodes.Update(nodeCopy); err != nil {
			return nil, fmt.Errorf("error updating drain on node %v", node.Name)
		}

		localCache[node.Name] = "draining"
		processing += 1
		toUpdate = true
	}

	//toUpdate = !reflect.DeepEqual(localCache, cluster.Status.NodeUpgradeStatus.Nodes[currHash])

	//logrus.Infof("localCache %v %v", localCache, toUpdate)
	if toUpdate {
		logrus.Infof("updating cluster after nodeMap update old:[%v] new:[%v]", localCache, cluster.Status.NodeUpgradeStatus.Nodes[currHash])
		clusterObj := cluster.DeepCopy()
		if clusterObj.Status.NodeUpgradeStatus == nil {
			clusterObj.Status.NodeUpgradeStatus = &v3.NodeUpgradeStatus{
				Nodes: map[string]map[string]string{},
			}
		}
		// retry
		logrus.Infof("trying to update %v", clusterObj.Status.NodeUpgradeStatus.Nodes[currHash])
		clusterObj.Status.NodeUpgradeStatus.Nodes[currHash] = localCache

		upd, err := uh.clusters.Update(clusterObj)
		if err != nil {
			logrus.Infof("update err %v", err)
			return nil, err
		}

		logrus.Infof("update went through successfully %v", upd.Status.NodeUpgradeStatus.Nodes[currHash])
		return cluster, nil
	}

	if done == worker {
		if cluster.Status.NodeUpgradeStatus.LastAppliedToken != currHash {
			logrus.Infof("All nodes should be upgraded! updating hash")
			clusterCopy := cluster.DeepCopy()

			clusterCopy.Status.NodeUpgradeStatus.LastAppliedToken = currHash
			clusterCopy.Status.NodeUpgradeStatus.CurrentToken = ""

			logrus.Infof("deleting currHash from the status")
			delete(clusterCopy.Status.NodeUpgradeStatus.Nodes, currHash)
			localCache = map[string]string{}

			if _, err := uh.clusters.Update(clusterCopy); err != nil {
				return nil, fmt.Errorf("error updating cluster hash %v", err)
			}
		} else {
			logrus.Infof("total matched, hash same %s %s", cluster.Labels["hash"], currHash)
		}
	} else {
		logrus.Infof("done %v nonWorker %v nodes %v worker %v", done, nonWorker, len(nodes), worker)
	}

	return cluster, nil
}

func workerOnly(roles []string) bool {
	worker := false
	for _, role := range roles {
		if role == rkeservices.ETCDRole {
			return false
		}
		if role == rkeservices.ControlRole {
			return false
		}
		if role == rkeservices.WorkerRole {
			worker = true
		}
	}
	return worker
}

func (uh *upgradeHandler) getClusterHash(cluster *v3.Cluster) (string, error) {
	// check for node OS, if windowsPreferredCluster, we should also check for windows, else just empty would get default values
	osType := ""
	svcOptions, err := uh.getServiceOptions(cluster.Spec.RancherKubernetesEngineConfig.Version, osType)
	if err != nil {
		return "", err
	}

	hostDockerInfo := types.Info{OSType: osType}
	clusterPlan, err := librke.New().GenerateClusterPlan(uh.ctx, cluster.Status.AppliedSpec.RancherKubernetesEngineConfig, hostDockerInfo, svcOptions)
	if err != nil {
		logrus.Infof("clusterPlan error %v", err)
		return "", err
	}

	clusterHash, err := ClusterHash(clusterPlan.Processes)
	if err != nil {
		return "", err
	}

	return clusterHash, nil
}

func getProcessingNumNodes(nodes []*v3.Node, nodeMap, clusterMap map[string]string) (int, int, int, bool) {
	processing := 0
	done := 0
	nonWorker := 0
	toUpdate := false

	for _, node := range nodes {
		if !workerOnly(node.Status.NodeConfig.Role) {
			nonWorker += 1
		}

		state, ok := nodeMap[node.Name]
		if !ok {
			continue
		}

		nodeState, ok := clusterMap[node.Name]
		if ok && nodeState == "upgraded" {
			if state != "upgraded" {
				toUpdate = true
				nodeMap[node.Name] = "upgraded"
			}
		} else if state != "active" {
			processing += 1

			if state == "draining" && node.Spec.DesiredNodeUnschedulable == "" {
				nodeMap[node.Name] = "drained"
				toUpdate = true
			}

		} else {
			done += 1
		}
	}
	return processing, done, nonWorker, toUpdate
}

func (uh *upgradeHandler) getServiceOptions(k8sVersion, osType string) (map[string]interface{}, error) {
	data := map[string]interface{}{}
	svcOptions, err := kd.GetRKEK8sServiceOptions(k8sVersion, uh.serviceOptionsLister, uh.serviceOptions, uh.sysImagesLister, uh.sysImages, kd.Linux)
	if err != nil {
		logrus.Errorf("getK8sServiceOptions: k8sVersion %s [%v]", k8sVersion, err)
		return data, err
	}
	if svcOptions != nil {
		data["k8s-service-options"] = svcOptions
	}
	if osType == "windows" {
		svcOptionsWindows, err := kd.GetRKEK8sServiceOptions(k8sVersion, uh.serviceOptionsLister, uh.serviceOptions, uh.sysImagesLister, uh.sysImages, kd.Windows)
		if err != nil {
			logrus.Errorf("getK8sServiceOptionsWindows: k8sVersion %s [%v]", k8sVersion, err)
			return data, err
		}
		if svcOptionsWindows != nil {
			data["k8s-windows-service-options"] = svcOptionsWindows
		}
	}
	return data, nil
}

func ClusterHash(processes map[string]v3.RKEProcess) (string, error) {
	cHash, err := hash.Hash(processes, nil)
	if err != nil {
		return "", err
	}
	return convert.ToString(cHash), nil
}
