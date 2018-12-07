package nodetemplate

import (
	"encoding/json"
	"fmt"
	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/convert"
	"github.com/rancher/rancher/pkg/configfield"
	"github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/labels"
	"github.com/rancher/types/apis/core/v1"
	"strings"
)

type Store struct {
	types.Store
	NodePoolLister v3.NodePoolLister
	CredLister v1.SecretLister
}

func (s *Store) Delete(apiContext *types.APIContext, schema *types.Schema, id string) (map[string]interface{}, error) {
	pools, err := s.NodePoolLister.List("", labels.Everything())
	if err != nil {
		return nil, err
	}
	for _, pool := range pools {
		if pool.Spec.NodeTemplateName == id {
			return nil, httperror.NewAPIError(httperror.MethodNotAllowed, "Template is in use by a node pool.")
		}
	}
	return s.Store.Delete(apiContext, schema, id)
}

func (s *Store) Create(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}) (map[string]interface{}, error) {
	ans, _ := json.Marshal(data)
	logrus.Infof("nodetemplate create %s", string(ans))

	driver := configfield.GetDriver(data)
	if driver == "" {
		id := convert.ToString(data["credentialId"])
		if id == "" {
			return nil, httperror.NewAPIError(httperror.MissingRequired, "a Config field or credentialId must be set")
		} else {
			split := strings.Split(id, ":")
			cred, err := s.CredLister.Get("kinara", split[1])
			if err != nil {
				return nil, err
			}
			driver = getDriverName(cred.Data)
			data[fmt.Sprintf("%s%s", driver, "Config")] = map[string]interface{}{}
		}
	}

	if data != nil {
		data["driver"] = driver
	}

	return s.Store.Create(apiContext, schema, data)
}

func getDriverName(data map[string][]byte) string {
	for key := range data {
		splitKey := strings.Split(key, "-")
		if len(splitKey) == 2 {
			if strings.HasSuffix(splitKey[0], "Config") {
				return strings.TrimSuffix(splitKey[0], "credentialConfig")
			}
		}
	}
	return ""
}