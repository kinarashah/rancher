package nslabels

import (
	"fmt"
	"strings"

	"github.com/rancher/rancher/utils"
	"github.com/rancher/types/apis/core/v1"
	typescorev1 "github.com/rancher/types/apis/core/v1"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ProjectIDFieldLabel  = "field.cattle.io/projectId"
	userSecretAnnotation = "secret.user.cattle.io/secret"
)

type namespaceHandler struct {
	secrets           v1.SecretInterface
	nsClient          typescorev1.NamespaceInterface
	managementSecrets v1.SecretInterface
}

func Register(cluster *config.UserContext) {
	logrus.Infof("Registering namespaceHandler for adding labels ")
	nsh := &namespaceHandler{
		secrets:           cluster.Core.Secrets(""),
		nsClient:          cluster.Core.Namespaces(""),
		managementSecrets: cluster.Management.Core.Secrets(""),
	}
	cluster.Core.Namespaces("").AddHandler("namespaceHandler", nsh.Sync)
}

func (nsh *namespaceHandler) Sync(key string, ns *corev1.Namespace) error {
	if ns == nil {
		return nil
	}
	logrus.Debugf("namespaceHandler: Sync: key=%v, ns=%+v", key, *ns)

	field, ok := ns.Annotations[ProjectIDFieldLabel]
	if !ok {
		return nil
	}

	splits := strings.Split(field, ":")
	if len(splits) != 2 {
		return nil
	}
	projectID := splits[1]
	logrus.Debugf("namespaceHandler: Sync: projectID=%v", projectID)

	if err := nsh.addProjectIDLabelToNamespace(ns, projectID); err != nil {
		logrus.Errorf("namespaceHandler: Sync: error adding project id label to namespace err=%v", err)
		return nil
	}

	return nil
}

func (nsh *namespaceHandler) addProjectIDLabelToNamespace(ns *corev1.Namespace, projectID string) error {
	if ns == nil {
		return fmt.Errorf("cannot add label to nil namespace")
	}
	if ns.Labels[ProjectIDFieldLabel] != projectID {
		nsh.updateProjectIDLabelToSecrets(ns.Labels[ProjectIDFieldLabel], projectID, ns.Name)
		logrus.Infof("namespaceHandler: addProjectIDLabelToNamespace: adding label %v=%v to namespace=%v", ProjectIDFieldLabel, projectID, ns.Name)

		// secrets, err := nsh.secrets.List(metav1.ListOptions{})
		// if err != nil {
		// 	return err
		// }
		// utils.Myprint(secrets, "secrets got ")
		// logrus.Infof("projectID %s", ns.Labels[ProjectIDFieldLabel])
		//
		// utils.Myprint(secrets, "got secrets")
		// logrus.Info("-----")
		// nssecrets, _ := nsh.secrets.List(metav1.ListOptions{})
		// utils.Myprint(nssecrets, "other secrets")
		nscopy := ns.DeepCopy()
		if nscopy.Labels == nil {
			nscopy.Labels = map[string]string{}
		}
		nscopy.Labels[ProjectIDFieldLabel] = projectID
		if _, err := nsh.nsClient.Update(nscopy); err != nil {
			return err
		}
	}

	return nil
}

func (nsh *namespaceHandler) updateProjectIDLabelToSecrets(projectIDFieldLabelValue, projectID string, namespace string) error {
	if projectIDFieldLabelValue == "" {
		return nil
	}
	logrus.Infof("namespace %s", namespace)
	secrets, err := nsh.managementSecrets.List(metav1.ListOptions{FieldSelector: fmt.Sprintf("metadata.namespace=%s", namespace)})
	if err != nil {
		return err
	}
	utils.Myprint(secrets, "secrets got ")
	for _, secret := range secrets.Items {
		if secret.Annotations[userSecretAnnotation] == "true" {
			utils.Myprint(secret, "deleting secret")
			if err := nsh.managementSecrets.DeleteNamespaced(namespace, secret.Name, &metav1.DeleteOptions{}); err != nil {
				return err
			}
		} else if secret.Annotations[ProjectIDFieldLabel] != "" {
			logrus.Infof("string %s", secret.Annotations[ProjectIDFieldLabel])
			logrus.Infof("old %s", projectIDFieldLabelValue)
			logrus.Infof("new %s", projectID)
			secret.Annotations[ProjectIDFieldLabel] = strings.Replace(secret.Annotations[ProjectIDFieldLabel], projectIDFieldLabelValue, projectID, 1)
			logrus.Infof("updated %s", secret.Annotations[ProjectIDFieldLabel])
			if _, err := nsh.managementSecrets.Update(&secret); err != nil {
				return err
			}
		}
	}
	return nil
}
