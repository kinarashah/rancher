package planner

import (
	"fmt"
	"github.com/rancher/rancher/pkg/taints"
	"github.com/sirupsen/logrus"
	"sort"
	"strings"

	v3 "github.com/rancher/rancher/pkg/apis/management.cattle.io/v3"
	rkev1 "github.com/rancher/rancher/pkg/apis/rke.cattle.io/v1"
	"github.com/rancher/rancher/pkg/systemtemplate"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

var controlPlaneLabels = map[string]string{
	"node-role.kubernetes.io/master":        "true",
	"node-role.kubernetes.io/controlplane":  "true",
	"node-role.kubernetes.io/control-plane": "true",
}

// generateClusterAgentManifest generates a cluster agent manifest
func (p *Planner) generateClusterAgentManifest(controlPlane *rkev1.RKEControlPlane, entry *planEntry) ([]byte, error) {
	if controlPlane.Spec.ManagementClusterName == "local" {
		return nil, nil
	}

	tokens, err := p.clusterRegistrationTokenCache.GetByIndex(clusterRegToken, controlPlane.Spec.ManagementClusterName)
	if err != nil {
		return nil, err
	}

	if len(tokens) == 0 {
		return nil, fmt.Errorf("no cluster registration token found")
	}

	sort.Slice(tokens, func(i, j int) bool {
		return tokens[i].Name < tokens[j].Name
	})

	mgmtCluster, err := p.managementClusters.Get(controlPlane.Spec.ManagementClusterName)
	if err != nil {
		return nil, err
	}

	mgmtNodes, err := p.managementNodes.List(controlPlane.Spec.ManagementClusterName, labels.Everything())
	if err != nil {
		return nil, err
	}
	var taints []corev1.Taint
	if len(mgmtNodes) == 0 {
		logrus.Infof("agentManifest: zero nodes, calculating default taints")
		taints, err = getTaints(entry, controlPlane)
		if err != nil {
			return nil, err
		}
	} else {
		taints, err = getTaintsCP(mgmtNodes)
	}

	logrus.Infof("agentManifest taints %v", taints)

	return systemtemplate.ForCluster(mgmtCluster, tokens[0].Status.Token, taints, p.secretCache)
}

func getTaintsCP(nodes []*v3.Node) ([]corev1.Taint, error) {
	var allTaints []corev1.Taint
	var controlPlaneLabelFound bool
	logrus.Debugf("agentManifest clusterDeploy: getControlPlaneTaints: Length of nodes for cluster is: %d", len(nodes))
	for _, node := range nodes {
		controlPlaneLabelFound = false
		// Filtering nodes for controlplane nodes based on labels
		for controlPlaneLabelKey, controlPlaneLabelValue := range controlPlaneLabels {
			if labelValue, ok := node.Status.NodeLabels[controlPlaneLabelKey]; ok {
				logrus.Tracef("agentManifest clusterDeploy: getControlPlaneTaints: node [%s] has label key [%s]", node.Status.NodeName, controlPlaneLabelKey)
				if labelValue == controlPlaneLabelValue {
					logrus.Tracef("agentManifest clusterDeploy: getControlPlaneTaints: node [%s] has label key [%s] and label value [%s]", node.Status.NodeName, controlPlaneLabelKey, controlPlaneLabelValue)
					controlPlaneLabelFound = true
					break
				}
			}
		}
		if controlPlaneLabelFound {
			toAdd, _ := taints.GetToDiffTaints(allTaints, node.Spec.InternalNodeSpec.Taints)
			for _, taintStr := range toAdd {
				if !strings.HasPrefix(taintStr.Key, "node.kubernetes.io") {
					logrus.Debugf("agentManifest clusterDeploy: getControlPlaneTaints: toAdd: %v", toAdd)
					allTaints = append(allTaints, taintStr)
					continue
				}
				logrus.Tracef("agentManifest clusterDeploy: getControlPlaneTaints: skipping taint [%v] because its k8s internal", taintStr)
			}
		}
	}
	return allTaints, nil
}
