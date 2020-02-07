package rkeworker

import (
	"fmt"

	"github.com/docker/docker/api/types"
	hash "github.com/mitchellh/hashstructure"
	"github.com/rancher/norman/types/convert"
	kd "github.com/rancher/rancher/pkg/controllers/management/kontainerdrivermetadata"
	"github.com/rancher/rancher/pkg/librke"
	"github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/runtime"
)

func (uh *upgradeHandler) Sync(key string, cluster *v3.Cluster) (runtime.Object, error) {
	if cluster == nil || cluster.DeletionTimestamp != nil {
		return cluster, nil
	}

	if uh.store.ns == "local" {
		return cluster, nil
	}

	logrus.Infof("ss %s %s", cluster.Name, uh.store.ns)

	if cluster.Name == "local" || cluster.Status.AppliedSpec.RancherKubernetesEngineConfig == nil {
		return cluster, nil
	}

	logrus.Infof("received sync for cluster [%s]", cluster.Name)

	status, err := uh.store.Load()
	if err != nil {
		return nil, err
	}

	if status.lastApplied == "" {
		logrus.Infof("lastApplied not present")

		initToken, err := uh.initToken(cluster)
		if err != nil {
			return nil, err
		}
		if initToken == "" {
			return cluster, nil
		}

		logrus.Infof("saving lastApplied %s", initToken)

		status.lastApplied = initToken

		if err := uh.store.Save(status); err != nil {
			return nil, err
		}

		return cluster, nil
	}

	clusterHash, err := uh.getClusterHash(cluster)
	if err != nil {
		return nil, fmt.Errorf("error getting cluster hash for [%s]: %v", cluster.Name, err)
	}

	if clusterHash == status.lastApplied {
		logrus.Infof("hash is same lastApplied %s clusterHash %s", status.lastApplied, clusterHash)
		return cluster, nil
	}

	currHash := status.current

	if currHash != clusterHash {
		logrus.Infof("saving currentToken %s", clusterHash)
		status.current = clusterHash

		if err := uh.store.Save(status); err != nil {
			return nil, err
		}
		return cluster, nil
	}

	if currHash != "" {
		logrus.Infof("enqueue cfg for cluster %s", cluster.Name)
		uh.cfgMaps.Controller().Enqueue(cluster.Name, UpgradeCfgName)
	}

	return cluster, nil
}

func (uh *upgradeHandler) getClusterHash(cluster *v3.Cluster) (string, error) {
	// check for node OS, if windowsPreferredCluster, we should also check for windows, else just empty would get default values

	return "upgr110", nil
	osType := ""
	svcOptions, err := uh.getServiceOptions(cluster.Spec.RancherKubernetesEngineConfig.Version, osType)
	if err != nil {
		return "", err
	}

	hostDockerInfo := types.Info{OSType: osType}
	clusterPlan, err := librke.New().GenerateClusterPlan(uh.ctx, cluster.Status.AppliedSpec.RancherKubernetesEngineConfig, hostDockerInfo, svcOptions)
	if err != nil {
		return "", err
	}

	clusterHash, err := ClusterHash(clusterPlan.Processes)
	if err != nil {
		return "", err
	}

	return clusterHash, nil
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
