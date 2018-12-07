package mapper

import (
	"fmt"
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/convert"
	"github.com/rancher/norman/types/values"
	"strings"
)

type TestMapper struct {
}

func (s TestMapper) FromInternal(data map[string]interface{}) {
	config, field, value := getKeys(convert.ToMapInterface(data["data"]))
	//logrus.Infof("config %s field %s value %s", config, field, value)

	if config != "" {
		values.PutValue(data, value, config, field)

		//ans, _ := json.Marshal(data)
		//logrus.Infof("now internal here %v", string(ans))
	}

	delete(data, "data")
}

func (s TestMapper) ToInternal(data map[string]interface{}) error {
	//ans, _ := json.Marshal(data)
	//logrus.Infof("entered here %v", string(ans))

	updateData(data)
	//ans, _ := json.Marshal(data)
	//logrus.Infof("now here %v", string(ans))
	return nil
}

func (s TestMapper) ModifySchema(schema *types.Schema, schemas *types.Schemas) error {
	return nil
}

func updateData(data map[string]interface{}) {
	stringData := map[string]string{}
	for key, val := range data {
		if strings.HasSuffix(key, "Config") {
			for key2, val := range convert.ToMapInterface(val) {
				stringData[fmt.Sprintf("%s-%s",key,key2)] = convert.ToString(val)
				values.PutValue(data, stringData, "stringData")
				delete(data, key)
				return
			}
		}
	}
}

func getKeys(data map[string]interface{}) (string, string, string) {
	//logrus.Infof("data %v", data)
	for key, val := range data {
		splitKeys := strings.Split(key, "-")
		//logrus.Infof("keys %v", splitKeys)
		if len(splitKeys) != 2 {
			continue
		}
		if strings.HasSuffix(splitKeys[0], "Config"){
			return splitKeys[0], splitKeys[1], convert.ToString(val)
		}
	}
	return "", "", ""
}