package mapper

import (
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/values"
)

type IngressBackend struct{}

func (i IngressBackend) FromInternal(data map[string]interface{}) {
	return
}

func (i IngressBackend) ToInternal(data map[string]interface{}) error {
	if _, ok := data["backend"]; !ok && data["defaultBackend"] != nil {
		data["backend"] = map[string]interface{}{
			"servicePort": values.GetValueN(data, "defaultBackend", "targetPort"),
			"serviceName": values.GetValueN(data, "defaultBackend", "serviceId"),
		}
	}
	return nil
}

func (i IngressBackend) ModifySchema(schema *types.Schema, schemas *types.Schemas) error {
	return nil
}

type IngressPath struct{}

func (i IngressPath) FromInternal(data map[string]interface{}) {
	return
}

func (i IngressPath) ToInternal(data map[string]interface{}) error {
	if values.GetValueN(data, "pathType") == nil {
		values.PutValue(data, "ImplementationSpecific", "pathType")
	}
	return nil
}

func (i IngressPath) ModifySchema(schema *types.Schema, schemas *types.Schemas) error {
	return nil
}
