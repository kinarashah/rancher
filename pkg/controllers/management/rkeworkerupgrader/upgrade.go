package rkeworkerupgrader

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/rancher/norman/types/slice"
	nodehelper "github.com/rancher/rancher/pkg/node"
	nodeserver "github.com/rancher/rancher/pkg/rkenodeconfigserver"
	"github.com/rancher/rancher/pkg/systemaccount"
	rkeservices "github.com/rancher/rke/services"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	ignoreKey = "rke.cattle.io/ignore-during-upgrade"
)

var (
	clusterLockMutex = sync.RWMutex{}
	clusterLock      = map[string]*sync.RWMutex{}
)

type upgradeHandler struct {
	clusters             v3.ClusterInterface
	nodes                v3.NodeInterface
	nodeLister           v3.NodeLister
	clusterLister        v3.ClusterLister
	lookup               *nodeserver.BundleLookup
	systemAccountManager *systemaccount.Manager
	serviceOptionsLister v3.RKEK8sServiceOptionLister
	serviceOptions       v3.RKEK8sServiceOptionInterface
	sysImagesLister      v3.RKEK8sSystemImageLister
	sysImages            v3.RKEK8sSystemImageInterface
	ctx                  context.Context
}

func Register(ctx context.Context, mgmt *config.ManagementContext, scaledContext *config.ScaledContext) {

	uh := &upgradeHandler{
		clusters:             mgmt.Management.Clusters(""),
		clusterLister:        mgmt.Management.Clusters("").Controller().Lister(),
		nodes:                mgmt.Management.Nodes(""),
		nodeLister:           mgmt.Management.Nodes("").Controller().Lister(),
		lookup:               nodeserver.NewLookup(scaledContext.Core.Namespaces(""), scaledContext.Core),
		systemAccountManager: systemaccount.NewManagerFromScale(scaledContext),
		serviceOptionsLister: mgmt.Management.RKEK8sServiceOptions("").Controller().Lister(),
		serviceOptions:       mgmt.Management.RKEK8sServiceOptions(""),
		sysImagesLister:      mgmt.Management.RKEK8sSystemImages("").Controller().Lister(),
		sysImages:            mgmt.Management.RKEK8sSystemImages(""),

		ctx: ctx,
	}

	mgmt.Management.Nodes("").Controller().AddHandler(ctx, "rke-worker-upgrader", uh.Sync)
}

func (uh *upgradeHandler) Sync(key string, node *v3.Node) (runtime.Object, error) {
	if strings.HasSuffix(key, "upgrade_") {

		cName := strings.Split(key, "/")[0]
		cluster, err := uh.clusterLister.Get("", cName)
		if err != nil {
			return nil, err
		}
		if cluster.DeletionTimestamp != nil || cluster.Status.AppliedSpec.RancherKubernetesEngineConfig == nil {
			return nil, nil
		}
		logrus.Infof("checking cluster [%s] for worker nodes upgrade", cluster.Name)

		if ok, err := uh.toUpgradeCluster(cluster); err != nil {
			return nil, err
		} else if ok {
			if err := uh.upgradeCluster(cluster, key); err != nil {
				return nil, err
			}
		}

		return nil, nil
	}

	if node == nil || node.DeletionTimestamp != nil || !v3.NodeConditionProvisioned.IsTrue(node) {
		return node, nil
	}

	cluster, err := uh.clusterLister.Get("", node.Namespace)
	if err != nil {
		return nil, err
	}

	if cluster.DeletionTimestamp != nil || cluster.Status.AppliedSpec.RancherKubernetesEngineConfig == nil {
		return node, nil
	}

	if node.Status.NodePlan == nil {
		logrus.Infof("creating node plan for node [%s]", node.Name)
		return uh.updateNodePlan(node, cluster)
	}

	if v3.ClusterConditionUpgraded.IsUnknown(cluster) {
		logrus.Infof("cluster [%s] upgrade is in progress, call upgrade to reconcile", cluster.Name)
		if err := uh.upgradeCluster(cluster, node.Name); err != nil {
			return nil, err
		}
		return node, nil
	}

	// check for upgrade if mismatch between node and cluster's versions
	toCheck := node.Status.AppliedNodeVersion != 0 && cluster.Status.NodeVersion != node.Status.AppliedNodeVersion
	if toCheck {
		logrus.Infof("toCheck %s is true node %v cluster %v", node.Name, node.Status.AppliedNodeVersion, cluster.Status.NodeVersion)
		nodePlan, err := uh.getNodePlan(node, cluster)
		if err != nil {
			return nil, err
		}

		if planChangedForUpgrade(nodePlan, node.Status.NodePlan.Plan) {
			logrus.Infof("plan [%s] changed for upgrade, call upgrade to reconcile cluster [%s]", node.Name, cluster.Name)
			if err := uh.upgradeCluster(cluster, node.Name); err != nil {
				return nil, err
			}
			return node, nil
		} else if planChangedForUpdate(nodePlan, node.Status.NodePlan.Plan) {
			logrus.Infof("plan [%s] changed for update", node.Name)
			return uh.updateNodePlan(node, cluster)
		} else {
			logrus.Infof("plan [%s] hasn't changed, syncing version nodePlan %v cluster %v", node.Name,
				node.Status.NodePlan.Version, cluster.Status.NodeVersion)
			return uh.updateNodePlanVersion(node, cluster)
		}
	}

	if node.Status.AppliedNodeVersion == 0 && workerOnly(node.Status.NodeConfig.Role) {
		nodePlan, err := uh.getNodePlan(node, cluster)
		if err != nil {
			return nil, err
		}
		if planChangedForUpgrade(nodePlan, node.Status.NodePlan.Plan) || planChangedForUpdate(nodePlan, node.Status.NodePlan.Plan) {
			return uh.updateNodePlan(node, cluster)
		}
		logrus.Infof("waiting for [%s] node-agent to sync, updating nodePlan version %v cluster %v", node.Name, node.Status.NodePlan.Version, cluster.Status.NodeVersion)
		return uh.updateNodePlanVersion(node, cluster)
	}

	return node, nil
}

func (uh *upgradeHandler) updateNodePlan(node *v3.Node, cluster *v3.Cluster) (*v3.Node, error) {
	nodePlan, err := uh.getNodePlan(node, cluster)
	if err != nil {
		return nil, fmt.Errorf("getNodePlan error for node [%s]: %v", node.Name, err)
	}

	nodeCopy := node.DeepCopy()
	np := &v3.NodePlan{
		Plan:    nodePlan,
		Version: cluster.Status.NodeVersion,
	}
	if node.Status.NodePlan == nil || node.Status.NodePlan.AgentCheckInterval == 0 {
		// default
		np.AgentCheckInterval = nodeserver.DefaultAgentCheckInterval
	} else {
		np.AgentCheckInterval = node.Status.NodePlan.AgentCheckInterval
	}
	nodeCopy.Status.NodePlan = np

	logrus.Infof("updating node [%s] with plan %v", node.Name, np.Version)

	updated, err := uh.nodes.Update(nodeCopy)
	if err != nil {
		return nil, fmt.Errorf("error updating node [%s] with plan %v", node.Name, err)
	}

	return updated, err
}

func (uh *upgradeHandler) updateNodePlanVersion(node *v3.Node, cluster *v3.Cluster) (*v3.Node, error) {
	if node.Status.NodePlan.Version == cluster.Status.NodeVersion {
		return node, nil
	}

	nodeCopy := node.DeepCopy()
	nodeCopy.Status.NodePlan.Version = cluster.Status.NodeVersion
	logrus.Infof("updating node [%s] with plan version %v", node.Name, nodeCopy.Status.NodePlan.Version)

	updated, err := uh.nodes.Update(nodeCopy)
	if err != nil {
		return nil, err
	}
	return updated, err

}

func (uh *upgradeHandler) getNodePlan(node *v3.Node, cluster *v3.Cluster) (*v3.RKEConfigNodePlan, error) {
	var (
		nodePlan *v3.RKEConfigNodePlan
		err      error
	)
	if isNonWorkerOnly(node.Status.NodeConfig.Role) {
		nodePlan, err = uh.nonWorkerPlan(node, cluster)
	} else {
		nodePlan, err = uh.workerPlan(node, cluster)
	}
	return nodePlan, err
}

func lock(name string) {
	clusterLockMutex.Lock()
	if _, ok := clusterLock[name]; !ok {
		clusterLock[name] = &sync.RWMutex{}
	}
	cMutex := clusterLock[name]
	clusterLockMutex.Unlock()

	cMutex.Lock()
}

func unlock(name string) {
	clusterLockMutex.RLock()
	cMutex := clusterLock[name]
	clusterLockMutex.RUnlock()

	cMutex.Unlock()
}

func (uh *upgradeHandler) upgradeCluster(cluster *v3.Cluster, nodeName string) error {
	clusterName := cluster.Name

	lock(clusterName)
	defer unlock(clusterName)
	logrus.Debugf("upgradeCluster: processing upgrade for [%s]", nodeName)

	if !v3.ClusterConditionUpgraded.IsUnknown(cluster) {
		clusterCopy := cluster.DeepCopy()
		v3.ClusterConditionUpgraded.Unknown(clusterCopy)
		v3.ClusterConditionUpgraded.Message(clusterCopy, "updating worker nodes")
		var err error
		cluster, err = uh.clusters.Update(clusterCopy)
		if err != nil {
			return err
		}
		logrus.Infof("updated cluster as upgrading...[%s]", cluster.Name)
	}

	nodes, err := uh.nodeLister.List(clusterName, labels.Everything())
	if err != nil {
		return err
	}

	toPrepareMap, toProcessMap, doneMap, notReadyMap, filtered, upgrading, done := filterNodes(nodes, cluster.Status.NodeVersion)

	maxAllowed, err := getNum(cluster.Spec.RancherKubernetesEngineConfig.UpgradeStrategy.MaxUnavailableWorker, filtered)
	if err != nil {
		return err
	}

	if len(notReadyMap) >= maxAllowed {
		return fmt.Errorf("not enough nodes to upgrade for cluster [%s]: nodes %v notReady %v maxUnavailable %v", cluster.Name, filtered, notReadyMap, maxAllowed)
	}

	unavailable := upgrading + len(notReadyMap)

	logrus.Infof("workerNodeInfo for cluster [%s]: nodes %v maxAllowed %v unavailable %v upgrading %v notReady %v "+
		"toProcess %v toPrepare %v done %v", cluster.Name, filtered, maxAllowed, unavailable, upgrading, len(notReadyMap), keys(toProcessMap), keys(toPrepareMap), keys(doneMap))

	if unavailable > maxAllowed {
		return fmt.Errorf("more than required nodes upgrading for cluster [%s]: unavailable %v maxUnavailable %v", cluster.Name, unavailable, maxAllowed)
	}

	for name, node := range doneMap {
		if v3.NodeConditionUpgraded.IsTrue(node) && node.Spec.DesiredNodeUnschedulable == "" {
			continue
		}

		if err := uh.updateNodeActive(node); err != nil {
			return err
		}

		logrus.Infof("updated node [%s] as done", name)
	}

	for _, node := range toProcessMap {
		if node.Status.NodePlan.Version == cluster.Status.NodeVersion {
			logrus.Infof("nodePlan version same, not updating [%s] %v", node.Name, cluster.Status.NodeVersion)
			continue
		}

		if err := uh.processNode(node, cluster); err != nil {
			return err
		}

		logrus.Infof("updated node \"upgrading\" and new plan %s", node.Name)
	}

	toDrain := cluster.Spec.RancherKubernetesEngineConfig.UpgradeStrategy.Drain
	var nodeDrainInput *v3.NodeDrainInput
	state := "cordon"
	if toDrain {
		nodeDrainInput = cluster.Spec.RancherKubernetesEngineConfig.UpgradeStrategy.DrainInput
		state = "drain"
	}

	for _, node := range toPrepareMap {
		if unavailable == maxAllowed {
			break
		}
		unavailable++

		if err := uh.prepareNode(node, toDrain, nodeDrainInput); err != nil {
			return err
		}

		logrus.Infof("updated node [%s] to %s", node.Name, state)
	}

	logrus.Infof("after toPrepare count %v prepare %v", upgrading, keys(toPrepareMap))

	if done == filtered {
		logrus.Infof("cluster is done upgrading! done %v len(nodes) %v", done, filtered)
		if !v3.ClusterConditionUpgraded.IsTrue(cluster) {
			clusterCopy := cluster.DeepCopy()
			v3.ClusterConditionUpgraded.True(clusterCopy)
			v3.ClusterConditionUpgraded.Message(clusterCopy, "")

			if _, err := uh.clusters.Update(clusterCopy); err != nil {
				return err
			}

			logrus.Infof("updated cluster as success upgraded [%s]", cluster.Name)
		}
	}

	return nil
}

func (uh *upgradeHandler) toUpgradeCluster(cluster *v3.Cluster) (bool, error) {
	if v3.ClusterConditionUpgraded.IsUnknown(cluster) {
		return true, nil
	}

	nodes, err := uh.nodeLister.List(cluster.Name, labels.Everything())
	if err != nil {
		return false, err
	}

	for _, node := range nodes {
		if node.Status.NodeConfig == nil {
			continue
		}

		if !workerOnly(node.Status.NodeConfig.Role) {
			continue
		}

		if node.Status.NodePlan == nil {
			continue
		}

		nodePlan, err := uh.getNodePlan(node, cluster)
		if err != nil {
			return false, err
		}

		if planChangedForUpgrade(nodePlan, node.Status.NodePlan.Plan) {
			return true, nil
		}
	}

	return false, nil
}

func (uh *upgradeHandler) prepareNode(node *v3.Node, toDrain bool, nodeDrainInput *v3.NodeDrainInput) error {
	var nodeCopy *v3.Node
	if toDrain {
		if node.Spec.DesiredNodeUnschedulable == "drain" {
			return nil
		}
		nodeCopy = node.DeepCopy()
		nodeCopy.Spec.DesiredNodeUnschedulable = "drain"
		nodeCopy.Spec.NodeDrainInput = &v3.NodeDrainInput{
			DeleteLocalData:  nodeDrainInput.DeleteLocalData,
			Force:            nodeDrainInput.Force,
			Timeout:          nodeDrainInput.Timeout,
			GracePeriod:      nodeDrainInput.GracePeriod,
			IgnoreDaemonSets: nodeDrainInput.IgnoreDaemonSets,
		}
	} else {
		if node.Spec.DesiredNodeUnschedulable == "true" || node.Spec.InternalNodeSpec.Unschedulable {
			return nil
		}
		nodeCopy = node.DeepCopy()
		nodeCopy.Spec.DesiredNodeUnschedulable = "true"
	}

	if _, err := uh.nodes.Update(nodeCopy); err != nil {
		return err
	}
	return nil
}

func (uh *upgradeHandler) processNode(node *v3.Node, cluster *v3.Cluster) error {
	nodePlan, err := uh.getNodePlan(node, cluster)
	if err != nil {
		return fmt.Errorf("setNodePlan: error getting node plan for [%s]: %v", node.Name, err)
	}

	nodeCopy := node.DeepCopy()
	nodeCopy.Status.NodePlan.Plan = nodePlan
	nodeCopy.Status.NodePlan.Version = cluster.Status.NodeVersion
	nodeCopy.Status.NodePlan.AgentCheckInterval = nodeserver.AgentCheckIntervalDuringUpgrade

	v3.NodeConditionUpgraded.Unknown(nodeCopy)
	v3.NodeConditionUpgraded.Message(nodeCopy, "upgrading")

	if _, err := uh.nodes.Update(nodeCopy); err != nil {
		return err
	}

	return nil
}

func (uh *upgradeHandler) updateNodeActive(node *v3.Node) error {
	nodeCopy := node.DeepCopy()
	v3.NodeConditionUpgraded.True(nodeCopy)
	v3.NodeConditionUpgraded.Message(nodeCopy, "")

	// reset the node
	nodeCopy.Spec.DesiredNodeUnschedulable = "false"
	nodeCopy.Status.NodePlan.AgentCheckInterval = nodeserver.DefaultAgentCheckInterval

	if _, err := uh.nodes.Update(nodeCopy); err != nil {
		return err
	}

	return nil
}

func filterNodes(nodes []*v3.Node, expectedVersion int) (map[string]*v3.Node, map[string]*v3.Node, map[string]*v3.Node, map[string]*v3.Node, int, int, int) {
	done, upgrading, filtered := 0, 0, 0
	toProcessMap, toPrepareMap, doneMap, notReadyMap := map[string]*v3.Node{}, map[string]*v3.Node{}, map[string]*v3.Node{}, map[string]*v3.Node{}

	for _, node := range nodes {
		if node.DeletionTimestamp != nil || node.Status.NodeConfig == nil || !workerOnly(node.Status.NodeConfig.Role) {
			continue
		}

		// skip nodes marked for ignore by user
		if node.Labels != nil && node.Labels[ignoreKey] == "true" {
			continue
		}

		// skip provisioning nodes
		if !v3.NodeConditionProvisioned.IsTrue(node) || !v3.NodeConditionRegistered.IsTrue(node) {
			continue
		}

		filtered++

		// check for nodeConditionReady
		if !nodehelper.IsMachineReady(node) {
			notReadyMap[node.Name] = nil
			logrus.Infof("node is not ready %s", node.Name)
			continue
		}

		if node.Status.AppliedNodeVersion == expectedVersion {
			done++
			doneMap[node.Name] = node
			continue
		}

		if node.Spec.DesiredNodeUnschedulable == "drain" || node.Spec.DesiredNodeUnschedulable == "true" {
			// draining or cordoning
			upgrading++
			continue
		}

		if v3.NodeConditionDrained.IsTrue(node) || v3.NodeConditionUpgraded.IsUnknown(node) || node.Spec.InternalNodeSpec.Unschedulable {
			// node ready to upgrade
			upgrading++
			toProcessMap[node.Name] = node
			continue
		}

		toPrepareMap[node.Name] = node
	}

	return toPrepareMap, toProcessMap, doneMap, notReadyMap, filtered, upgrading, done
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

func isNonWorkerOnly(role []string) bool {
	if slice.ContainsString(role, rkeservices.ETCDRole) ||
		slice.ContainsString(role, rkeservices.ControlRole) {
		return true
	}
	return false
}

func getNum(maxUnavailable string, nodes int) (int, error) {
	parsedMax := intstr.Parse(maxUnavailable)
	maxAllowed, err := intstr.GetValueFromIntOrPercent(&parsedMax, nodes, false)
	if err != nil {
		return 0, err
	}
	if maxAllowed >= 1 {
		return maxAllowed, nil
	}
	return 1, nil
}

func keys(m map[string]*v3.Node) []string {
	k := []string{}
	for key, val := range m {
		if val != nil {
			k = append(k, val.Status.NodeName)
		} else {
			k = append(k, key)
		}
	}
	return k
}
