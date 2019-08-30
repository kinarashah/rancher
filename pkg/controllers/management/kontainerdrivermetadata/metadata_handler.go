package kontainerdrivermetadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/rancher/norman/types/convert"
	"github.com/rancher/rancher/pkg/catalog/git"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/rancher/rancher/pkg/settings"

	"github.com/rancher/rancher/pkg/ticker"
	v3 "github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/sirupsen/logrus"

	"github.com/rancher/types/config"
)

type MetadataController struct {
	SystemImagesLister   v3.RKEK8sSystemImageLister
	SystemImages         v3.RKEK8sSystemImageInterface
	ServiceOptionsLister v3.RKEK8sServiceOptionLister
	ServiceOptions       v3.RKEK8sServiceOptionInterface
	AddonsLister         v3.RKEAddonLister
	Addons               v3.RKEAddonInterface
	ctx                  context.Context
}

type Data struct {
	K8sVersionServiceOptions  map[string]v3.KubernetesServicesOptions
	K8sVersionRKESystemImages map[string]v3.RKESystemImages
	K8sVersionedTemplates     map[string]map[string]string

	K8sVersionInfo            map[string]v3.K8sVersionInfo
	RancherDefaultK8sVersions map[string]string

	K8sVersionWindowsServiceOptions map[string]v3.KubernetesServicesOptions
}

type TickerData struct {
	cancelFunc context.CancelFunc
	interval   time.Duration
}

type URL struct {
	path       string
	branch     string
	latestHash string
	isGit      bool
}

const (
	rkeMetadataURL  = "rke-metadata-url"
	refreshInterval = "refresh-interval-minutes"
)

var (
	httpClient = &http.Client{
		Timeout: time.Second * 30,
	}
	dataPath   = filepath.Join("./management-state", "driver-metadata", "rke")
	tickerData *TickerData
	prevPath   string
)

func Register(ctx context.Context, management *config.ManagementContext) {
	mgmt := management.Management

	m := &MetadataController{
		SystemImagesLister:   mgmt.RKEK8sSystemImages("").Controller().Lister(),
		SystemImages:         mgmt.RKEK8sSystemImages(""),
		ServiceOptionsLister: mgmt.RKEK8sServiceOptions("").Controller().Lister(),
		ServiceOptions:       mgmt.RKEK8sServiceOptions(""),
		AddonsLister:         mgmt.RKEAddons("").Controller().Lister(),
		Addons:               mgmt.RKEAddons(""),
		ctx:                  ctx,
	}

	mgmt.Settings("").AddHandler(ctx, "rke-metadata-handler", m.sync)
}

func (m *MetadataController) sync(key string, setting *v3.Setting) (runtime.Object, error) {
	if setting == nil || (setting.Name != rkeMetadataURL && setting.Name != refreshInterval) {
		return nil, nil
	}

	settingValues, err := GetSettingValues()
	if err != nil {
		return nil, err
	}

	interval, err := parseTime(settingValues[refreshInterval])
	if err != nil {
		if err.Error() != "refresh disabled" {
			return nil, err
		}
	}

	// handle this later ..
	if tickerData == nil {
		cctx, cancel := context.WithCancel(m.ctx)
		tickerData = &TickerData{cancelFunc: cancel, interval: interval}
		go m.startTicker(cctx, tickerData)
		logrus.Infof("driverMetadata initialized successfully")
		return setting, nil
	}

	// don't sync if time is set to negative/zero
	if interval == 0 && tickerData.interval != 0 {
		logrus.Debugf("driverMetadata: canceled counter")
		tickerData.cancelFunc()
		tickerData.interval = 0
		return setting, nil
	}

	logrus.Infof("setting values %v", settingValues)
	url, err := parseURL(settingValues)
	if err != nil {
		logrus.Infof("returning from here %v", err)
		return nil, err
	}

	//// don't sync if url or hash hasn't changed
	//if !toSync(url) {
	//	return setting, nil
	//}

	if err := m.NRefresh(url, false); err != nil {
		return nil, err
	}

	// update ticker if required
	if tickerData.interval != interval {
		tickerData.cancelFunc()

		logrus.Infof("driverMetadata: starting new counter every %v", interval)
		cctx, cancel := context.WithCancel(m.ctx)
		tickerData.interval = interval
		tickerData.cancelFunc = cancel

		go m.startTicker(cctx, tickerData)
	}

	return setting, nil
}

func (m *MetadataController) initAndStartTicker() error {
	//urlData, err := getURLSetting()
	//if err != nil {
	//	return err
	//}
	//// ignore error and proceed because init
	//url, _ := generateURL(urlData)
	//if err := m.Refresh(url, true); err != nil {
	//	return err
	//}
	//interval, err := getTimeSetting()
	//if err != nil {
	//	if err.Error() == "refresh disabled" {
	//		return nil
	//	}
	//	return err
	//}
	//cctx, cancel := context.WithCancel(m.ctx)
	//tickerData = &TickerData{cancelFunc: cancel, interval: interval}
	//go m.startTicker(cctx, tickerData)
	return nil
}

func (m *MetadataController) startTicker(ctx context.Context, tickerData *TickerData) {
	checkInterval := tickerData.interval
	for range ticker.Context(ctx, checkInterval) {
		logrus.Infof("driverMetadata: checking rke-metadata-url every %v", checkInterval)
		url, err := GetURLSettingValue()
		if err != nil {
			logrus.Errorf("driverMetadata: error getting settings %v", err)
		}
		if err := m.Refresh(url, false); err != nil {
			logrus.Errorf("driverMetadata failed to refresh %v", err)
		}
	}
}

func (m *MetadataController) NRefresh(url *URL, init bool) error {
	data, err := NloadData(url)
	return nil
	if err != nil {
		if init {
			logrus.Errorf("error loading rke data, using stored defaults %v", err)
			if err := m.createOrUpdateMetadataDefaults(); err != nil {
				return err
			}
			return nil
		}
		return err
	}

	logrus.Debug("driverMetadata: refresh data")
	if err := m.createOrUpdateMetadata(data); err != nil {
		return err
	}
	return nil
}

func (m *MetadataController) Refresh(url string, init bool) error {
	//data, err := loadData(url)
	//if err != nil {
	//	if init {
	//		logrus.Errorf("error loading rke data, using stored defaults %v", err)
	//		if err := m.createOrUpdateMetadataDefaults(); err != nil {
	//			return err
	//		}
	//		return nil
	//	}
	//	return err
	//}
	//
	//logrus.Debug("driverMetadata: refresh data")
	//if err := m.createOrUpdateMetadata(data); err != nil {
	//	return err
	//}
	return nil
}

func (m *MetadataController) refresh() error {
	url, err := GetURLSettingValue()
	if err != nil {
		return err
	}
	if err := m.Refresh(url, false); err != nil {
		return fmt.Errorf("driverMetadata failed to refresh %v", err)
	}
	return nil
}

func getURLSetting() (map[string]interface{}, error) {
	urlData := map[string]interface{}{}
	//if err := json.Unmarshal([]byte(settings.RkeMetadataURL.Get()), &urlData); err != nil {
	//	return nil, fmt.Errorf("unmarshal err %v", err)
	//}
	//if _, ok := urlData["url"]; !ok {
	//	return nil, fmt.Errorf("url not present in settings %s", settings.RkeMetadataURL.Get())
	//}
	return urlData, nil
}

func generateURL(urlData map[string]interface{}) (string, error) {
	branch, ok := urlData["branch"]
	if !ok {
		return convert.ToString(urlData["url"]), nil
	}
	latestURL, err := generateURLHelper(convert.ToString(convert.ToString(urlData["url"])), convert.ToString(branch))
	if err != nil {
		return "", err
	}
	return latestURL, nil
}

func parseURL(rkeData map[string]interface{}) (*URL, error) {
	url := &URL{}
	path, ok := rkeData["url"]
	if !ok {
		return nil, fmt.Errorf("url not present in settings %s", settings.RkeMetadataConfig.Get())
	}
	url.path = convert.ToString(path)
	branch, ok := rkeData["branch"]
	if !ok {
		return url, nil
	}
	url.branch = convert.ToString(branch)
	logrus.Infof("url branch %s %s", url.path, url.branch)
	latestHash, err := git.RemoteBranchHeadCommit(url.path, url.branch)
	if err != nil {
		return nil, fmt.Errorf("error getting latest commit %s %s %v", url.path, url.branch, err)
	}
	url.latestHash = latestHash
	url.isGit = true
	return url, nil
}

func NloadData(url *URL) (Data, error) {
	if url.isGit {
		logrus.Infof("entering here??")
		return getDataGit(url.path, url.branch)
	}
	return getDataHttp(url.path)
}

func parseTime(interval interface{}) (time.Duration, error) {
	mins := convert.ToString(interval)
	if strings.HasPrefix(mins, "-") || strings.HasPrefix(mins, "0") {
		return 0, fmt.Errorf("refresh disabled")
	}
	t := fmt.Sprintf("%sm", mins)
	checkInterval, err := time.ParseDuration(t)
	if err != nil {
		return 0, err
	}
	return checkInterval, nil
}

func GetURLSettingValue() (string, error) {
	//rkeData, err := getSettingValues()
	//if err != nil {
	//	return "", err
	//}
	//url, err := generateURL(urlData)
	//if err != nil {
	//	return "", err
	//}
	return "", nil
}

func generateURLHelper(url, branch string) (string, error) {
	latestCommit, err := git.RemoteBranchHeadCommit(url, branch)
	if err != nil {
		return "", err
	}
	split := strings.Split(strings.TrimSuffix(url, ".git"), "/")
	n := len(split) - 1
	if n < 1 {
		return "", fmt.Errorf("couldn't extract repo from %s", url)
	}
	repo := fmt.Sprintf("%s/%s", split[n-1], split[n])
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/data/data.json", repo, latestCommit), nil
}

func getDataHttp(url string) (Data, error) {
	var data Data
	resp, err := httpClient.Get(url)
	if err != nil {
		return data, fmt.Errorf("driverMetadata err %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return data, fmt.Errorf("driverMetadata statusCode %v", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return data, fmt.Errorf("read response body error %v", err)
	}

	if err := json.Unmarshal(body, &data); err != nil {
		return data, fmt.Errorf("driverMetadata %v", err)
	}
	return data, nil
}

func getDataGit(urlPath, branch string) (Data, error) {
	var data Data

	if _, err := os.Stat(dataPath); os.IsNotExist(err) {
		logrus.Infof("creating new dir %v", dataPath)
		if err := os.MkdirAll(dataPath, 0755); err != nil {
			return data, fmt.Errorf("error creating directory %v", err)
		}
	}

	logrus.Infof("starting")
	if err := git.Clone(dataPath, urlPath, branch); err != nil {
		return data, fmt.Errorf("error cloning repo %s %s: %v", urlPath, branch, err)
	}
	logrus.Infof("reading file??")
	body, err := ioutil.ReadFile(dataPath)
	if err != nil {
		return data, fmt.Errorf("error reading metadata file %v", err)
	}
	logrus.Infof("marshaling ...")
	if err := json.Unmarshal(body, &data); err != nil {
		return data, fmt.Errorf("error unmarshaling metadata contents %v", err)
	}
	logrus.Infof("returning? %v", data.K8sVersionInfo)
	return data, nil
}

func GetSettingValues() (map[string]interface{}, error) {
	urlData := map[string]interface{}{}
	if err := json.Unmarshal([]byte(settings.RkeMetadataConfig.Get()), &urlData); err != nil {
		return nil, fmt.Errorf("unmarshal err %v", err)
	}
	return urlData, nil
}

func runcmd(name string, arg ...string) error {
	cmd := exec.Command(name, arg...)
	//bufErr := &bytes.Buffer{}
	//cmd.Stderr = bufErr
	//if err := cmd.Run(); err != nil {
	//	return errors.Wrap(err, bufErr.String())
	//}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrap(err, string(output))
	}
	parts := strings.Split(string(output), "\t")
	logrus.Infof("output %s", parts)
	return nil
}
