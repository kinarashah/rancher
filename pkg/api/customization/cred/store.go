package cred

import (
	"encoding/json"
	"github.com/rancher/norman/store/transform"
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/convert"
	"github.com/sirupsen/logrus"
	"strings"
)

func Wrap(store types.Store) types.Store {
	return &transform.Store{
		Store:       store,
		Transformer: func(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}, opt *types.QueryOptions) (map[string]interface{}, error) {
			if configExists(data) {
					data["type"] = "cloudCredential"
					return data, nil
			}
			return nil, nil
		},
	}
}

type Store struct {
	types.Store
}

func (s *Store) Create(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}) (map[string]interface{}, error) {
	ans, _ := json.Marshal(data)
	logrus.Infof("create %s", string(ans))

	return s.Store.Create(apiContext, schema, data)
}

func (s *Store) Update(apiContext *types.APIContext, schema *types.Schema, data map[string]interface{}, id string) (map[string]interface{}, error) {

	return s.Store.Update(apiContext, schema, data, id)
}

func configExists(data map[string]interface{}) bool {
	for key, val := range data {
		if strings.HasSuffix(key, "Config") {
			if convert.ToString(val) != "" {
				return true
			}
		}
	}
	return false
}

