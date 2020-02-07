package rkeworker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rancher/rancher/pkg/randomtoken"
	"github.com/rancher/rancher/pkg/ticker"
	rkeservices "github.com/rancher/rke/services"
	"github.com/rancher/types/apis/core/v1"
	"github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type upgradeHandler struct {
	store                *Store
	nodesLister          v3.NodeLister
	nodes                v3.NodeInterface
	clusters             v3.ClusterInterface
	clusterLister        v3.ClusterLister
	serviceOptionsLister v3.RKEK8sServiceOptionLister
	serviceOptions       v3.RKEK8sServiceOptionInterface
	sysImagesLister      v3.RKEK8sSystemImageLister
	sysImages            v3.RKEK8sSystemImageInterface

	cfgMapLister v1.ConfigMapLister
	cfgMaps      v1.ConfigMapInterface
	ctx          context.Context
	mutex        sync.RWMutex
}

const (
	UpgradeCfgName = "rkeworkerupgradestatus"
)

var (
	interval       = 30 * time.Second
	clusterMapData = map[string]context.CancelFunc{}
	clusterLock    = sync.Mutex{}
)

func Register(ctx context.Context, config *config.UserContext) {
	logrus.Infof("registering management upgrade handler for rke worker nodes %s", config.ClusterName)
	uh := upgradeHandler{
		nodesLister:          config.Management.Management.Nodes("").Controller().Lister(),
		nodes:                config.Management.Management.Nodes(""),
		clusters:             config.Management.Management.Clusters(""),
		clusterLister:        config.Management.Management.Clusters("").Controller().Lister(),
		serviceOptionsLister: config.Management.Management.RKEK8sServiceOptions("").Controller().Lister(),
		serviceOptions:       config.Management.Management.RKEK8sServiceOptions(""),
		sysImagesLister:      config.Management.Management.RKEK8sSystemImages("").Controller().Lister(),
		sysImages:            config.Management.Management.RKEK8sSystemImages(""),

		cfgMapLister: config.Management.Core.ConfigMaps(config.ClusterName).Controller().Lister(),
		cfgMaps:      config.Management.Core.ConfigMaps(config.ClusterName),
		ctx:          ctx,
	}

	uh.store = NewStore(uh.cfgMapLister, uh.cfgMaps, config.ClusterName)

	config.Management.Management.Clusters("").Controller().AddHandler(ctx, "rkeworkerupgradehandler", uh.Sync)

	config.Management.Core.ConfigMaps(config.ClusterName).Controller().AddHandler(ctx, "configMapupgradeHandler", uh.cfgSync)
	//
	//	clusterMapData = map[string]context.CancelFunc{}
}

func (uh *upgradeHandler) cfgSync(key string, cfg *corev1.ConfigMap) (runtime.Object, error) {
	if uh.store.ns == "local" {
		return cfg, nil
	}
	if cfg == nil || cfg.DeletionTimestamp != nil || cfg.Name != UpgradeCfgName {
		return cfg, nil
	}

	if cfg.Data == nil {
		return cfg, nil
	}

	_, ok := cfg.Data[lastAppliedKey]
	if !ok {
		return cfg, nil
	}

	currHash, ok := cfg.Data[currentKey]
	if !ok {
		return cfg, nil
	}

	logrus.Infof("received synncc %s", cfg.Namespace)

	cluster, err := uh.clusterLister.Get("", cfg.Namespace)
	if err != nil {
		return nil, err
	}

	nodes, err := uh.nodesLister.List(cluster.Name, labels.Everything())
	if err != nil {
		return nil, err
	}

	var (
		notReady, upgrading, done int
	)

	nodes, notReady = filterNodes(nodes)

	logrus.Infof("nodes %v notReady %v", len(nodes), notReady)

	maxAllowed, err := getNum(&cluster.Spec.RancherKubernetesEngineConfig.NodeUpgradeStrategy.RollingUpdate.MaxUnavailable,
		len(nodes), false)
	if err != nil {
		return nil, err
	}

	if notReady >= maxAllowed {
		return nil, fmt.Errorf("not enough nodes not upgrade for cluster [%s]: notReady %v maxUnavailable %v", cluster.Name, notReady, maxAllowed)
	}

	toProcessMap, toPrepareMap, doneMap := map[string]bool{}, map[string]bool{}, map[string]bool{}

	// add errgroup sync and wait
	for _, node := range nodes {
		if v3.NodeConditionUpdated.IsTrue(node) && currHash == v3.NodeConditionUpdated.GetReason(node) {
			done += 1
			doneMap[node.Name] = true
			continue
		}
		if (v3.NodeConditionUpdated.IsUnknown(node) && currHash == v3.NodeConditionUpdated.GetReason(node)) ||
			node.Spec.DesiredNodeUnschedulable == "drain" {
			upgrading += 1
			continue
		}
		if v3.NodeConditionDrained.IsTrue(node) {
			upgrading += 1
			toProcessMap[node.Name] = true
			continue
		}
		toPrepareMap[node.Name] = true
	}

	logrus.Infof("workerNodeInfo for cluster [%s]: maxAllowed %v upgrading %v "+
		"toProcess %v toPrepare %v done %v", cluster.Name, maxAllowed, upgrading, toProcessMap, toPrepareMap, done)

	status, err := uh.store.Load()
	if err != nil {
		logrus.Infof("error getting cfg lol %s", cluster.Name)
		return nil, err
	}

	if status.current == "" {
		logrus.Infof("returning from currentToken cuz not present")
		return cfg, nil
	}

	statusMap := status.nodes

	logrus.Infof("statusMap %v", statusMap)

	for name := range statusMap {
		if doneMap[name] {
			delete(statusMap, name)
		}
	}

	upgrading = len(statusMap)

	logrus.Infof("statusMap after done %v %v", statusMap, upgrading)

	for name := range toProcessMap {
		statusMap[name] = "updating"
	}

	logrus.Infof("statusMap after process %v %v", statusMap, upgrading)

	upgradeStrategy := cluster.Spec.RancherKubernetesEngineConfig.NodeUpgradeStrategy.RollingUpdate
	toDrain := upgradeStrategy.Drain

	for name := range toPrepareMap {
		if upgrading == maxAllowed {
			break
		}
		statusMap[name] = "draining"
		upgrading += 1
	}

	logrus.Infof("statusMap after prepare %v %v", statusMap, upgrading)

	status.nodes = statusMap

	if err := uh.store.Save(status); err != nil {
		return nil, err
	}

	nodeDrainInput := upgradeStrategy.DrainInput
	if toDrain && nodeDrainInput == nil {
		nodeDrainInput = &v3.NodeDrainInput{
			Force:            true,
			IgnoreDaemonSets: true,
			DeleteLocalData:  true,
		}
	}

	go uh.updateNodes(statusMap, toDrain, nodeDrainInput, currHash, cluster.Name)

	if done == len(nodes) {
		status.lastApplied = status.current
		status.current = ""

		if err := uh.store.Save(status); err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

func (uh *upgradeHandler) updateNodes(clusterMap map[string]string, toDrain bool, nodeDrainInput *v3.NodeDrainInput, currHash string, cName string) {
	enqueue := false
	for name, state := range clusterMap {
		node, err := uh.nodesLister.Get(cName, name)
		if err != nil {
			logrus.Errorf("error finding node [%s] for cluster [%s]: %v", name, cName, err)
			enqueue = true
			break
		}
		if state == "draining" {
			if err := uh.prepareNode(node, toDrain, nodeDrainInput, currHash); err != nil {
				if errors.IsConflict(err) {
					enqueue = true
					break
				}
				logrus.Errorf("error updating node [%s] for upgrade cluster [%s] %v", node.Name, cName)
				continue
			}
		} else if state == "updating" {
			if err := uh.processNode(node, toDrain, nodeDrainInput, currHash); err != nil {
				if errors.IsConflict(err) {
					enqueue = true
					break
				}
				logrus.Errorf("error updating node [%s] for upgrade cluster [%s] %v", node.Name, cName)
				continue
			}
		}
	}
	if enqueue {
		uh.cfgMaps.Controller().Enqueue(cName, UpgradeCfgName)
	}
}

func (uh *upgradeHandler) prepareNode(node *v3.Node, toDrain bool, nodeDrainInput *v3.NodeDrainInput, currHash string) error {
	var nodeCopy *v3.Node

	if !toDrain && node.Spec.DesiredNodeUnschedulable != "true" {
		nodeCopy = node.DeepCopy()
		nodeCopy.Spec.DesiredNodeUnschedulable = "true"
		v3.NodeConditionUpdated.Unknown(nodeCopy)
		v3.NodeConditionUpdated.Reason(nodeCopy, currHash)
		v3.NodeConditionUpdated.Message(nodeCopy, "updating!")
	}

	if toDrain && node.Spec.DesiredNodeUnschedulable != "drain" {
		nodeCopy = node.DeepCopy()
		nodeCopy.Spec.DesiredNodeUnschedulable = "drain"
		nodeCopy.Spec.NodeDrainInput = nodeDrainInput
	}

	if nodeCopy == nil {
		return nil
	}

	if nodeCopy != nil {
		logrus.Infof("updating with nodeCopy param %s %s", node.Name, nodeCopy.Spec.DesiredNodeUnschedulable)

		_, err := uh.nodes.Update(nodeCopy)
		if err != nil {
			return err
		}
	}
	return nil
}

func (uh *upgradeHandler) processNode(node *v3.Node, toDrain bool, nodeDrainInput *v3.NodeDrainInput, currHash string) error {
	logrus.Infof("processNode %s", node.Name)

	if !toDrain && !node.Spec.InternalNodeSpec.Unschedulable {
		logrus.Errorf("why node is in processing %s %v", node.Name, node.Spec.InternalNodeSpec.Unschedulable)
		return nil
	}

	if toDrain && !v3.NodeConditionDrained.IsTrue(node) {
		logrus.Errorf("why node is in processing %s for drain %v", node.Name, v3.NodeConditionDrained.GetStatus(node))
		return nil
	}

	nodeEqual := v3.NodeConditionUpdated.IsUnknown(node) && v3.NodeConditionUpdated.GetReason(node) == currHash
	logrus.Infof("nodeEqual %s %v", node.Name, nodeEqual)

	if !nodeEqual {
		logrus.Infof("updating node %s for upgrade", node.Name)
		nodeCopy := node.DeepCopy()
		v3.NodeConditionUpdated.Unknown(nodeCopy)
		v3.NodeConditionUpdated.Reason(nodeCopy, currHash)
		v3.NodeConditionUpdated.Message(nodeCopy, "updating!")

		_, err := uh.nodes.Update(nodeCopy)
		if err != nil {
			return err

		}
	}
	return nil
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

func (uh *upgradeHandler) initToken(cluster *v3.Cluster) (string, error) {
	nodes, err := uh.nodesLister.List(cluster.Name, labels.Everything())
	if err != nil {
		return "", err
	}

	nodes, _ = filterNodes(nodes)

	requiredForInit, err := getNum(&cluster.Spec.RancherKubernetesEngineConfig.NodeUpgradeStrategy.RollingUpdate.MaxUnavailable,
		len(nodes), true)
	if err != nil {
		return "", err
	}

	if requiredForInit <= len(nodes) {
		logrus.Infof("%v worker nodes ready for cluster [%s], setting token", requiredForInit, cluster.Name)
		clusterHash, err := uh.getClusterHash(cluster)
		if err != nil {
			return "", fmt.Errorf("error getting cluster hash for [%s]: %v", cluster.Name, err)
		}
		return clusterHash, nil
	}

	return "", nil
}

func getNum(maxUnavailable *intstr.IntOrString, nodes int, init bool) (int, error) {
	maxAllowed, err := intstr.GetValueFromIntOrPercent(maxUnavailable, nodes, false)
	if err != nil {
		logrus.Infof("getMaxAllowed err %v", err)
		return 0, err
	}
	if init {
		if nodes > maxAllowed {
			return nodes - maxAllowed, nil
		}
	}
	if maxAllowed >= 1 {
		return maxAllowed, nil
	}
	return 1, nil
}

func filterNodes(nodes []*v3.Node) ([]*v3.Node, int) {
	var filtered []*v3.Node
	notReady := 0

	// add errgroup
	for _, node := range nodes {
		if node.Status.NodeConfig == nil || !workerOnly(node.Status.NodeConfig.Role) || node.DeletionTimestamp != nil {
			continue
		}

		nodeGood := v3.NodeConditionRegistered.IsTrue(node) && v3.NodeConditionProvisioned.IsTrue(node) &&
			!v3.NodeConditionReady.IsUnknown(node)

		if !nodeGood {
			notReady += 1
			logrus.Infof("node is not ready %s", node.Name)
			continue
		}

		filtered = append(filtered, node)
	}

	return filtered, notReady
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
