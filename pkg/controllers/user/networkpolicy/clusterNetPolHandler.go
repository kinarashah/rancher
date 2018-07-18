package networkpolicy

import (
	"fmt"

	"github.com/rancher/rancher/pkg/controllers/user/nodesyncer"
	"github.com/rancher/types/apis/core/v1"
	"github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type clusterNetPolHandler struct {
	cluster       *config.UserContext
	pnpLister     v3.ProjectNetworkPolicyLister
	podLister     v1.PodLister
	serviceLister v1.ServiceLister
	projClient    v3.ProjectInterface
	clusters      v3.ClusterInterface
	npmgr         *netpolMgr
}

func (ch *clusterNetPolHandler) Sync(key string, cluster *v3.Cluster) error {
	if cluster == nil || cluster.DeletionTimestamp != nil ||
		!v3.ClusterConditionReady.IsTrue(cluster) || cluster.Spec.EnableNetworkPolicy == nil ||
		cluster.Status.AppliedEnableNetworkPolicy == *cluster.Spec.EnableNetworkPolicy {
		return nil
	}

	cluster.Status.AppliedEnableNetworkPolicy = *cluster.Spec.EnableNetworkPolicy
	if _, err := ch.clusters.Update(cluster); err != nil {
		return err
	}

	if err := ch.refresh(cluster); err != nil {
		// reset if failure
		cluster.Status.AppliedEnableNetworkPolicy = !*cluster.Spec.EnableNetworkPolicy
		if _, err := ch.clusters.Update(cluster); err != nil {
			return err
		}
		return err
	}

	return nil
}

func (ch *clusterNetPolHandler) refresh(cluster *v3.Cluster) error {
	if cluster.Status.AppliedEnableNetworkPolicy {
		logrus.Debugf("clusterNetPolHandler: calling sync to create network policies")
		projects, err := ch.npmgr.projLister.List(cluster.Name, labels.NewSelector())
		if err != nil {
			return err
		}
		for _, project := range projects {
			ch.projClient.Controller().Enqueue(project.Namespace, project.Name)
		}

		pods, err := ch.podLister.List("", labels.NewSelector())
		if err != nil {
			return err
		}
		for _, pod := range pods {
			ch.cluster.Core.Pods("").Controller().Enqueue(pod.Namespace, pod.Name)
		}

		svcs, err := ch.serviceLister.List("", labels.NewSelector())
		if err != nil {
			return err
		}
		for _, svc := range svcs {
			ch.cluster.Core.Services("").Controller().Enqueue(svc.Namespace, svc.Name)
		}

		ch.cluster.Management.Management.Nodes(ch.cluster.ClusterName).Controller().Enqueue(
			cluster.ClusterName, fmt.Sprintf("%s/%s", ch.cluster.ClusterName, nodesyncer.AllNodeKey))

		// skip nssyncer, projectSyncer + nodehandler would result into handling nssyncer as well

	} else {
		logrus.Debugf("clusterNetPolHandler: deleting network policies")
		nps, err := ch.npmgr.npLister.List("", labels.NewSelector())
		if err != nil {
			return err
		}

		for _, np := range nps {
			ch.npmgr.delete(np.Namespace, np.Name)
		}

		pnps, err := ch.pnpLister.List("", labels.NewSelector())
		if err != nil {
			return err
		}

		for _, pnp := range pnps {
			ch.cluster.Management.Management.ProjectNetworkPolicies("").DeleteNamespaced(pnp.Namespace, pnp.Name, &metav1.DeleteOptions{})
		}
	}
	return nil
}
