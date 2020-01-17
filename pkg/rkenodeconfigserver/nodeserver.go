package rkenodeconfigserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/pkg/errors"
	"github.com/rancher/norman/types/slice"
	"github.com/rancher/rancher/pkg/api/customization/clusterregistrationtokens"
	util "github.com/rancher/rancher/pkg/cluster"
	kd "github.com/rancher/rancher/pkg/controllers/management/kontainerdrivermetadata"
	"github.com/rancher/rancher/pkg/image"
	"github.com/rancher/rancher/pkg/librke"
	"github.com/rancher/rancher/pkg/rkeworker"
	"github.com/rancher/rancher/pkg/settings"
	"github.com/rancher/rancher/pkg/systemaccount"
	"github.com/rancher/rancher/pkg/taints"
	"github.com/rancher/rancher/pkg/tunnelserver"
	rkehosts "github.com/rancher/rke/hosts"
	rkepki "github.com/rancher/rke/pki"
	rkeservices "github.com/rancher/rke/services"
	"github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
)

var (
	b2Mount = "/mnt/sda1"
)

type RKENodeConfigServer struct {
	auth                 *tunnelserver.Authorizer
	lookup               *BundleLookup
	systemAccountManager *systemaccount.Manager
	serviceOptionsLister v3.RKEK8sServiceOptionLister
	serviceOptions       v3.RKEK8sServiceOptionInterface
	sysImagesLister      v3.RKEK8sSystemImageLister
	sysImages            v3.RKEK8sSystemImageInterface
	clusters             v3.ClusterInterface
	nodes                v3.NodeInterface
}

func Handler(auth *tunnelserver.Authorizer, scaledContext *config.ScaledContext) http.Handler {
	return &RKENodeConfigServer{
		auth:                 auth,
		lookup:               NewLookup(scaledContext.Core.Namespaces(""), scaledContext.Core),
		systemAccountManager: systemaccount.NewManagerFromScale(scaledContext),
		serviceOptionsLister: scaledContext.Management.RKEK8sServiceOptions("").Controller().Lister(),
		serviceOptions:       scaledContext.Management.RKEK8sServiceOptions(""),
		sysImagesLister:      scaledContext.Management.RKEK8sSystemImages("").Controller().Lister(),
		sysImages:            scaledContext.Management.RKEK8sSystemImages(""),
		clusters:             scaledContext.Management.Clusters(""),
		nodes:                scaledContext.Management.Nodes(""),
	}
}

func (n *RKENodeConfigServer) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// 404 tells the client to continue without plan
	// 5xx tells the client to try again later for plan

	client, ok, err := n.auth.Authorize(req)
	if err != nil {
		rw.WriteHeader(http.StatusInternalServerError)
		rw.Write([]byte(err.Error()))
		return
	}

	if !ok {
		rw.WriteHeader(http.StatusUnauthorized)
		return
	}

	if client.Node == nil {
		rw.WriteHeader(http.StatusNotFound)
		return
	}

	if client.Cluster.Status.Driver == "" {
		rw.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	if client.Cluster.Status.Driver != v3.ClusterDriverRKE {
		rw.WriteHeader(http.StatusNotFound)
		return
	}

	if client.Node.Status.NodeConfig == nil {
		rw.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	var nodeConfig *rkeworker.NodeConfig
	if isNonWorkerOnly(client.Node.Status.NodeConfig.Role) {
		nodeConfig, err = n.nonWorkerConfig(req.Context(), client.Cluster, client.Node)
	} else {
		if client.Cluster.Status.AppliedSpec.RancherKubernetesEngineConfig == nil {
			rw.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		//logrus.Infof("entered here???")
		cluster := client.Cluster
		token := req.Header.Get("upgrade_token")
		if token != "" {
			logrus.Infof("header for %s: %s, now to clean it up", client.Node.Name, token)
			cluster, err = n.markUpgraded(client.Cluster, client.Node, token)
		}
		//logrus.Infof("entered nodeConfig?")
		nodeConfig, err = n.nodeConfig(req.Context(), cluster, client.Node)

		if err != nil && err.Error() == "upgraded" {
			logrus.Infof("will wait to be upgraded %s", client.Node.Status.NodeName)
			rw.WriteHeader(http.StatusAccepted)
			return
		}
	}

	if err != nil {
		rw.WriteHeader(http.StatusInternalServerError)
		rw.Write([]byte("Failed to construct node config. Error: " + err.Error()))
		return
	}

	rw.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(rw).Encode(nodeConfig); err != nil {
		logrus.Errorf("failed to write nodeConfig to agent: %v", err)
	}
}

func isNonWorkerOnly(role []string) bool {
	if slice.ContainsString(role, rkeservices.ETCDRole) ||
		slice.ContainsString(role, rkeservices.ControlRole) {
		return true
	}
	return false
}

func (n *RKENodeConfigServer) markUpgraded(cluster *v3.Cluster, node *v3.Node, token string) (*v3.Cluster, error) {
	nodeMap, ok := cluster.Status.NodeUpgradeStatus.Nodes[token]
	if !ok {
		logrus.Infof("server: got to update, why token not present %v", cluster.Status.NodeUpgradeStatus.Nodes)
		return cluster, nil
	}

	state, ok := nodeMap[node.Name]
	if !ok {
		logrus.Infof("server: no state for this node? %s %s", nodeMap[node.Name], state)
		return cluster, nil
	}

	if state != "upgrading" {
		logrus.Infof("server: state for this node  %s %s", node.Name, state)
	} else {
		logrus.Infof("server: marking the state as upgraded! %s %s", node.Name, state)

		nodeCopy := node.DeepCopy()
		v3.NodeConditionUpdated.True(nodeCopy)
		v3.NodeConditionUpdated.Reason(nodeCopy, "")
		v3.NodeConditionUpdated.Message(nodeCopy, "")

		_, err := n.nodes.Update(nodeCopy)
		if err != nil {
			logrus.Infof("markUpgraded: couldn't update node to upgraded! %s %v", node.Name, err)
		}

		logrus.Infof("reaching here?ss")
		clusterObj := cluster.DeepCopy()
		clusterObj.Status.NodeUpgradeStatus.Nodes[token][node.Name] = "upgraded"

		upd, err := n.clusters.Update(clusterObj)
		if err != nil {
			logrus.Infof("not returning error because we'd update it next time? RETRY? %v", err)
			return cluster, nil
		}

		return upd, nil

	}
	return cluster, nil

}

func (n *RKENodeConfigServer) nonWorkerConfig(ctx context.Context, cluster *v3.Cluster, node *v3.Node) (*rkeworker.NodeConfig, error) {
	rkeConfig := cluster.Status.AppliedSpec.RancherKubernetesEngineConfig
	if rkeConfig == nil {
		rkeConfig = &v3.RancherKubernetesEngineConfig{}
	}

	rkeConfig = rkeConfig.DeepCopy()
	rkeConfig.Nodes = []v3.RKEConfigNode{
		*node.Status.NodeConfig,
	}
	rkeConfig.Nodes[0].Role = []string{rkeservices.WorkerRole, rkeservices.ETCDRole, rkeservices.ControlRole}

	infos, err := librke.GetDockerInfo(node)
	if err != nil {
		return nil, err
	}
	hostAddress := node.Status.NodeConfig.Address
	svcOptions, err := n.getServiceOptions(cluster.Spec.RancherKubernetesEngineConfig.Version, infos[hostAddress].OSType)
	if err != nil {
		return nil, err
	}

	plan, err := librke.New().GeneratePlan(ctx, rkeConfig, infos, svcOptions)
	if err != nil {
		return nil, err
	}

	nc := &rkeworker.NodeConfig{
		ClusterName: cluster.Name,
	}
	token, err := n.systemAccountManager.GetOrCreateSystemClusterToken(cluster.Name)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create or get cluster token for share-mnt")
	}
	for _, tempNode := range plan.Nodes {
		if tempNode.Address == node.Status.NodeConfig.Address {
			b2d := strings.Contains(infos[tempNode.Address].OperatingSystem, rkehosts.B2DOS)
			nc.Processes = augmentProcesses(token, tempNode.Processes, false, b2d,
				node.Status.NodeConfig.HostnameOverride, cluster)
			nc.Processes = appendTaintsToKubeletArgs(nc.Processes, node.Status.NodeConfig.Taints)
			return nc, nil
		}
	}

	return nil, fmt.Errorf("failed to find plan for non-worker %s", node.Status.NodeConfig.Address)
}

func proceed(cluster *v3.Cluster, node *v3.Node) (bool, bool) {
	nodeUpgradeStatus := cluster.Status.NodeUpgradeStatus
	if nodeUpgradeStatus == nil {
		return true, false
	}

	if len(cluster.Status.NodeUpgradeStatus.Nodes) == 0 {
		return true, false
	}

	currHash := cluster.Status.NodeUpgradeStatus.CurrentToken

	//logrus.Infof("currHash for node-name %s is %s", node.Name, currHash)

	nodeMap, ok := nodeUpgradeStatus.Nodes[currHash]
	if !ok {
		return false, false
	}

	state, ok := nodeMap[node.Name]
	if !ok || state == "draining" {
		//logrus.Infof("state for nodename %s %s %v", state, node.Name, nodeMap)
		return false, false
	}

	if state == "upgraded" || state == "active" {
		return true, false
	}

	if state == "upgrading" || state == "drained" {
		//logrus.Infof("state for nodename %s %s %v", state, node.Name, nodeMap)
		return true, true
	}

	logrus.Infof("server reached here why false, %s %v", node.Name, nodeMap)
	return false, false
}

func (n *RKENodeConfigServer) nodeConfig(ctx context.Context, cluster *v3.Cluster, node *v3.Node) (*rkeworker.NodeConfig, error) {

	toProceed, sendToken := proceed(cluster, node)
	if !toProceed {
		return nil, fmt.Errorf("wait")
	}

	spec := cluster.Status.AppliedSpec.DeepCopy()

	infos, err := librke.GetDockerInfo(node)
	if err != nil {
		return nil, err
	}

	bundle, err := n.lookup.Lookup(cluster)
	if err != nil {
		return nil, err
	}

	hostAddress := node.Status.NodeConfig.Address
	hostDockerInfo := infos[hostAddress]
	if hostDockerInfo.OSType == "windows" { // compatible with Windows
		bundle = bundle.ForWindowsNode(spec.RancherKubernetesEngineConfig, hostAddress)
	} else {
		bundle = bundle.ForNode(spec.RancherKubernetesEngineConfig, hostAddress)
	}

	rkeConfig := spec.RancherKubernetesEngineConfig
	filterHostForSpec(rkeConfig, node)
	logrus.Debugf("The number of nodes sent to the plan: %v", len(rkeConfig.Nodes))
	svcOptions, err := n.getServiceOptions(cluster.Spec.RancherKubernetesEngineConfig.Version, hostDockerInfo.OSType)
	if err != nil {
		return nil, err
	}
	plan, err := librke.New().GeneratePlan(ctx, rkeConfig, infos, svcOptions)
	if err != nil {
		return nil, err
	}

	nc := &rkeworker.NodeConfig{
		ClusterName: cluster.Name,
	}
	token, err := n.systemAccountManager.GetOrCreateSystemClusterToken(cluster.Name)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create or get cluster token for share-mnt")
	}
	for _, tempNode := range plan.Nodes {
		if tempNode.Address == hostAddress {
			if hostDockerInfo.OSType == "windows" { // compatible with Windows
				nc.Processes = enhanceWindowsProcesses(tempNode.Processes)
			} else {
				b2d := strings.Contains(infos[tempNode.Address].OperatingSystem, rkehosts.B2DOS)
				nc.Processes = augmentProcesses(token, tempNode.Processes, true, b2d,
					node.Status.NodeConfig.HostnameOverride, cluster)
			}
			nc.Processes = appendTaintsToKubeletArgs(nc.Processes, node.Status.NodeConfig.Taints)
			nc.Files = tempNode.Files
			// Adding kubelet certificate
			// Check if argument is set on kubelet for serving certificate
			if rkepki.IsKubeletGenerateServingCertificateEnabledinConfig(rkeConfig) {
				//			for _, command := range nc.Processes["kubelet"].Command {
				//				if strings.Contains(command, "tls-cert-file") {
				logrus.Debugf("nodeConfig: VerifyKubeletCAEnabled is true, generating kubelet certificate for [%s]", tempNode.Address)
				err := rkepki.GenerateKubeletCertificate(ctx, bundle.Certs(), *rkeConfig, "", "", false)
				if err != nil {
					return nil, errors.Wrapf(err, "failed to generate kubelet certificate")
				}
				//					break
				//				}
			}
			certString, err := bundle.Marshal()
			if err != nil {
				return nil, errors.Wrapf(err, "failed to marshal certificates bundle")
			}
			nc.Certs = certString

			if cluster.Status.NodeUpgradeStatus != nil && cluster.Status.NodeUpgradeStatus.CurrentToken != "" {
				logrus.Infof("ENTERED HERE %s", node.Name)

				currToken := cluster.Status.NodeUpgradeStatus.CurrentToken
				if cluster.Status.NodeUpgradeStatus.Nodes[currToken][node.Name] != "upgrading" {
					clusterObj := cluster.DeepCopy()
					logrus.Infof("DEEPCOPY %s", node.Name)

					logrus.Infof("map %v token %s", clusterObj.Status.NodeUpgradeStatus.Nodes[currToken], currToken)
					clusterObj.Status.NodeUpgradeStatus.Nodes[currToken][node.Name] = "upgrading"

					logrus.Infof("updating here %s", node.Name)
					_, err := n.clusters.Update(clusterObj)
					if err != nil {
						logrus.Infof("ENTERED HERE?")
						logrus.Infof("UPGRADING not sending token because cluster update failed %v", err)
					}
				}

				if !v3.NodeConditionUpdated.IsUnknown(node) {
					logrus.Infof("reached node copy?")
					nodeCopy := node.DeepCopy()
					v3.NodeConditionUpdated.Unknown(nodeCopy)
					v3.NodeConditionUpdated.Message(nodeCopy, "agent upgrading")
					v3.NodeConditionUpdated.Reason(nodeCopy, "agent upgrading")

					_, err = n.nodes.Update(nodeCopy)
					if err != nil {
						logrus.Infof("UPGRADING not sending token because node update failed %v", err)
					}
				}

				if sendToken {
					logrus.Infof("reached cluster token")
					token := cluster.Status.NodeUpgradeStatus.CurrentToken
					logrus.Infof("server sending token for %s: currToken %s nodeMap %v", node.Name, token, cluster.Status.NodeUpgradeStatus.Nodes[token])
					nc.Token = token
				}
			}

			logrus.Infof("RETURNING FROM %s", node.Name)
			return nc, nil
		}
	}

	return nil, fmt.Errorf("failed to find plan for %s", hostAddress)
}

func filterHostForSpec(spec *v3.RancherKubernetesEngineConfig, n *v3.Node) {
	nodeList := make([]v3.RKEConfigNode, 0)
	for _, node := range spec.Nodes {
		if isNonWorkerOnly(node.Role) || node.NodeName == n.Status.NodeConfig.NodeName {
			nodeList = append(nodeList, node)
		}
	}
	spec.Nodes = nodeList
}

func augmentProcesses(token string, processes map[string]v3.Process, worker, b2d bool, nodeName string,
	cluster *v3.Cluster) map[string]v3.Process {
	var shared []string

	if b2d {
		shared = append(shared, b2Mount)
	}

	for _, process := range processes {
		for _, bind := range process.Binds {
			parts := strings.Split(bind, ":")
			if len(parts) > 2 && strings.Contains(parts[2], "shared") {
				shared = append(shared, parts[0])
			}
		}
	}

	if len(shared) > 0 {
		agentImage := settings.AgentImage.Get()
		nodeCommand := clusterregistrationtokens.NodeCommand(token, cluster) + " --no-register --only-write-certs"
		args := []string{"--", "share-root.sh", strings.TrimPrefix(nodeCommand, "sudo "), "--node-name", nodeName}
		args = append(args, shared...)
		privateRegistryConfig, _ := util.GenerateClusterPrivateRegistryDockerConfig(cluster)
		processes["share-mnt"] = v3.Process{
			Name:                    "share-mnt",
			Args:                    args,
			Image:                   image.ResolveWithCluster(agentImage, cluster),
			Binds:                   []string{"/var/run:/var/run"},
			NetworkMode:             "host",
			RestartPolicy:           "always",
			PidMode:                 "host",
			Privileged:              true,
			ImageRegistryAuthConfig: privateRegistryConfig,
		}
	}

	if worker {
		// not sure if we really need this anymore
		delete(processes, "etcd")
	} else {
		if p, ok := processes["share-mnt"]; ok {
			processes = map[string]v3.Process{
				"share-mnt": p,
			}
		} else {
			processes = nil
		}
	}

	for _, p := range processes {
		for i, bind := range p.Binds {
			parts := strings.Split(bind, ":")
			if len(parts) > 1 && parts[1] == "/etc/kubernetes" {
				parts[0] = parts[1]
				p.Binds[i] = strings.Join(parts, ":")
			}
		}
	}

	return processes
}

func enhanceWindowsProcesses(processes map[string]v3.Process) map[string]v3.Process {
	newProcesses := make(map[string]v3.Process, len(processes))
	for k, p := range processes {
		p.Binds = append(p.Binds,
			"//./pipe/rancher_wins://./pipe/rancher_wins",
		)
		newProcesses[k] = p
	}

	return newProcesses
}

func appendTaintsToKubeletArgs(processes map[string]v3.Process, nodeConfigTaints []v3.RKETaint) map[string]v3.Process {
	if kubelet, ok := processes["kubelet"]; ok && len(nodeConfigTaints) != 0 {
		initialTaints := taints.GetTaintsFromStrings(taints.GetStringsFromRKETaint(nodeConfigTaints))
		var currentTaints []v1.Taint
		foundArgs := ""
		for i, arg := range kubelet.Command {
			if strings.HasPrefix(arg, "--register-with-taints=") {
				foundArgs = strings.TrimPrefix(arg, "--register-with-taints=")
				kubelet.Command = append(kubelet.Command[:i], kubelet.Command[i+1:]...)
				break
			}
		}
		if foundArgs != "" {
			currentTaints = taints.GetTaintsFromStrings(strings.Split(foundArgs, ","))
		}

		// The initial taints are from node pool and node template. They should override the taints from kubelet args.
		mergedTaints := taints.MergeTaints(currentTaints, initialTaints)

		taintArgs := fmt.Sprintf("--register-with-taints=%s", strings.Join(taints.GetStringsFromTaint(mergedTaints), ","))
		kubelet.Command = append(kubelet.Command, taintArgs)
		processes["kubelet"] = kubelet
	}
	return processes
}

func (n *RKENodeConfigServer) getServiceOptions(k8sVersion string, osType string) (map[string]interface{}, error) {
	data := map[string]interface{}{}
	svcOptions, err := kd.GetRKEK8sServiceOptions(k8sVersion, n.serviceOptionsLister, n.serviceOptions, n.sysImagesLister, n.sysImages, kd.Linux)
	if err != nil {
		logrus.Errorf("getK8sServiceOptions: k8sVersion %s [%v]", k8sVersion, err)
		return data, err
	}
	if svcOptions != nil {
		data["k8s-service-options"] = svcOptions
	}
	if osType == "windows" {
		svcOptionsWindows, err := kd.GetRKEK8sServiceOptions(k8sVersion, n.serviceOptionsLister, n.serviceOptions, n.sysImagesLister, n.sysImages, kd.Windows)
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
