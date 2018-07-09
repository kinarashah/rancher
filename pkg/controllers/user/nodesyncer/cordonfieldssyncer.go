package nodesyncer

import (
	"encoding/json"

	"github.com/rancher/norman/types/convert"
	"github.com/rancher/rancher/pkg/kubectl"
	"github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	drainTokenPrefix = "drain-node-"
	description      = "token for drain"
)

func (m *NodesSyncer) syncCordonFields(key string, obj *v3.Node) error {
	if obj == nil || obj.DeletionTimestamp != nil || obj.Spec.DesiredNodeUnschedulable == "" || obj.Spec.DesiredNodeUnschedulable == "drain" {
		return nil
	}
	nodes, err := m.nodeLister.List("", labels.NewSelector())
	if err != nil {
		return err
	}
	node, err := m.getNode(obj, nodes)
	if err != nil {
		return err
	}
	desiredValue := convert.ToBool(obj.Spec.DesiredNodeUnschedulable)
	if node.Spec.Unschedulable != desiredValue {
		toUpdate := node.DeepCopy()
		toUpdate.Spec.Unschedulable = desiredValue
		if _, err := m.nodeClient.Update(toUpdate); err != nil {
			return err
		}
	}
	nodeCopy := obj.DeepCopy()
	nodeCopy.Spec.DesiredNodeUnschedulable = ""
	if _, err := m.machines.Update(nodeCopy); err != nil {
		return err
	}
	return nil
}

func (d *NodeDrain) drainNode(key string, obj *v3.Node) error {
	if obj == nil || obj.DeletionTimestamp != nil || obj.Spec.DesiredNodeUnschedulable != "drain" {
		return nil
	}
	logrus.Info("entered!(2)")
	cluster, err := d.clusterLister.Get("", d.clusterName)
	if err != nil {
		return err
	}
	user, err := d.systemAccountManager.GetSystemUser(cluster)
	if err != nil {
		return err
	}
	token, err := d.userManager.EnsureToken(drainTokenPrefix+user.Name, description, user.Name)
	if err != nil {
		return err
	}
	logrus.Infof("token %s", token)
	kubeConfig := d.kubeConfigGetter.KubeConfig(d.clusterName, token)
	node, err := kubectl.Drain(kubeConfig, obj.Spec.RequestedHostname)
	ans, _ := json.Marshal(node)
	logrus.Infof("node! %s", string(ans))
	return nil
}
