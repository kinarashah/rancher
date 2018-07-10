package kubectl

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"

	"fmt"

	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	tmpDir = "./management-state/tmp"
)

func Apply(yaml []byte, kubeConfig *clientcmdapi.Config) ([]byte, error) {
	kubeConfigFile, err := tempFile("kubeconfig-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(kubeConfigFile.Name())

	yamlFile, err := tempFile("yaml-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(yamlFile.Name())

	if err := ioutil.WriteFile(yamlFile.Name(), yaml, 0600); err != nil {
		return nil, err
	}

	if err := clientcmd.WriteToFile(*kubeConfig, kubeConfigFile.Name()); err != nil {
		return nil, err
	}

	cmd := exec.Command("kubectl",
		"--kubeconfig",
		kubeConfigFile.Name(),
		"apply",
		"-f",
		yamlFile.Name())
	return runWithHTTP2(cmd)
}

func Drain(kubeConfig *clientcmdapi.Config, args []string) ([]byte, error) {
	kubeConfigFile, err := tempFile("kubeconfig-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(kubeConfigFile.Name())

	if err := clientcmd.WriteToFile(*kubeConfig, kubeConfigFile.Name()); err != nil {
		return nil, err
	}

	cmd := exec.Command("kubectl",
		"--kubeconfig",
		kubeConfigFile.Name(),
		"drain")

	cmd.Args = append(cmd.Args, args...)

	ans, _ := json.Marshal(cmd)
	logrus.Info("Command %s", string(ans))
	var newEnv []string
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "DISABLE_HTTP2") {
			continue
		}
		newEnv = append(newEnv, env)
	}
	cmd.Env = newEnv
	_, err = cmd.CombinedOutput()
	if err != nil {
		logrus.Info("inside drain")
		logrus.Info(fmt.Sprint(err))
		logrus.Info(cmd.Stderr)
	}
	return cmd.CombinedOutput()
}

func convertSelectorToString(selector *metav1.LabelSelector) string {
	if selector == nil {
		logrus.Info("selector is nil!")
	}
	ans := metav1.FormatLabelSelector(selector)
	// logrus.Info("convertSelectorToString %s", ans)
	return ans
}

func tempFile(prefix string) (*os.File, error) {
	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		if err = os.MkdirAll(tmpDir, 0755); err != nil {
			return nil, err
		}
	}

	f, err := ioutil.TempFile(tmpDir, prefix)
	if err != nil {
		return nil, err
	}

	return f, f.Close()
}

func ApplyWithNamespace(yaml []byte, namespace string, kubeConfig *clientcmdapi.Config) ([]byte, error) {
	kubeConfigFile, err := tempFile("kubeconfig-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(kubeConfigFile.Name())

	yamlFile, err := tempFile("yaml-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(yamlFile.Name())

	if err := ioutil.WriteFile(yamlFile.Name(), yaml, 0600); err != nil {
		return nil, err
	}

	if err := clientcmd.WriteToFile(*kubeConfig, kubeConfigFile.Name()); err != nil {
		return nil, err
	}

	cmd := exec.Command("kubectl",
		"--kubeconfig",
		kubeConfigFile.Name(),
		"-n",
		namespace,
		"apply",
		"-f",
		yamlFile.Name())
	return runWithHTTP2(cmd)
}

func runWithHTTP2(cmd *exec.Cmd) ([]byte, error) {
	var newEnv []string
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "DISABLE_HTTP2") {
			continue
		}
		newEnv = append(newEnv, env)
	}
	cmd.Env = newEnv
	return cmd.CombinedOutput()
}
