package nodesyncer

import (
	"github.com/rancher/types/apis/management.cattle.io/v3"
	"k8s.io/apimachinery/pkg/labels"
)

func (m *NodesSyncer) syncCordonFields(key string, obj *v3.Node) error {
	if obj == nil {
		return nil
	}
	machine := obj.DeepCopy()
	nodes, err := m.nodeLister.List("", labels.NewSelector())
	if err != nil {
		return err
	}
	node, err := m.getNode(machine, nodes)
	if err != nil {
		return err
	}
	shouldUpdate := false
	if node.Spec.Unschedulable != machine.Spec.InternalNodeSpec.Unschedulable {
		node.Spec.Unschedulable = machine.Spec.InternalNodeSpec.Unschedulable
		shouldUpdate = true
	}
	if shouldUpdate {
		if _, err := m.nodeClient.Update(node); err != nil {
			return err
		}
	}
	return nil
}
