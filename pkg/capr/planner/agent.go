package planner

import (
	"fmt"
	"sort"

	rkev1 "github.com/rancher/rancher/pkg/apis/rke.cattle.io/v1"
	"github.com/rancher/rancher/pkg/systemtemplate"
)

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

	taints, err := getTaints(entry, controlPlane)
	if err != nil {
		return nil, err
	}

	return systemtemplate.ForCluster(mgmtCluster, tokens[0].Status.Token, taints, p.secretCache)
}

func (p *Planner) generateClusterAgentManifestJob(controlPlane *rkev1.RKEControlPlane, path string) []byte {
	job := `
apiVersion: batch/v1
kind: Job
metadata:
  name: check-cluster-agent
  namespace: cattle-system
spec:
  backoffLimit: 1
  template:
    spec:
      containers:
      - name: agent
        image: rancher/kubectl:v1.23.3
        command: ["kubectl","get", "pods", "-n", "cattle-system"]
      restartPolicy: Never
`
	return []byte(fmt.Sprintf(job))
}
