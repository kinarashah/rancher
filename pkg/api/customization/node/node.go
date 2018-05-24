package node

import (
	"github.com/rancher/norman/types"
	"github.com/rancher/norman/types/convert"
	"github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/client/management/v3"
	managementschema "github.com/rancher/types/apis/management.cattle.io/v3/schema"
	"github.com/rancher/norman/api/access"
	"github.com/rancher/norman/httperror"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"github.com/rancher/norman/types/values"
	"fmt"
	"github.com/rancher/rancher/utils"
)

// Formatter for Node
func Formatter(apiContext *types.APIContext, resource *types.RawResource) {
	etcd := convert.ToBool(resource.Values[client.NodeFieldEtcd])
	cp := convert.ToBool(resource.Values[client.NodeFieldControlPlane])
	worker := convert.ToBool(resource.Values[client.NodeFieldWorker])
	if !etcd && !cp && !worker {
		resource.Values[client.NodeFieldWorker] = true
	}

	// add nodeConfig link
	if err := apiContext.AccessControl.CanDo(v3.NodeDriverGroupVersionKind.Group, v3.NodeDriverResource.Name, "update", apiContext, resource.Values, apiContext.Schema); err == nil {
		resource.Links["nodeConfig"] = apiContext.URLBuilder.Link("nodeConfig", resource)
	}

	// remove link
	nodeTemplateID := resource.Values["nodeTemplateId"]
	customConfig := resource.Values["customConfig"]
	if nodeTemplateID == nil {
		delete(resource.Links, "nodeConfig")
	}

	if nodeTemplateID == nil && customConfig == nil {
		delete(resource.Links, "remove")
	}

	resource.AddAction(apiContext, "cordon")
}

type ActionWrapper struct {}

func (a ActionWrapper) ActionHandler(actionName string, action *types.Action, apiContext *types.APIContext) error {
	switch actionName {
	case "cordon":
		var node map[string]interface{}
		if err := access.ByID(apiContext, apiContext.Version, apiContext.Type, apiContext.ID, &node); err != nil {
			return httperror.NewAPIError(httperror.InvalidReference, "Error accessing node")
		}
		schema := apiContext.Schemas.Schema(&managementschema.Version, client.NodeType)
		unschedulable := convert.ToBool(values.GetValueN(node, "unschedulable"))
		if unschedulable {
			return httperror.NewAPIError(httperror.InvalidAction, fmt.Sprintf("Node %s already cordoned", apiContext.ID))
		}
		values.PutValue(node, true, "unschedulable")
		updated, err := schema.Store.Update(apiContext, schema, node, apiContext.ID)
		if err != nil && apierrors.IsNotFound(err) {
			return httperror.NewAPIError(httperror.ServerError, fmt.Sprintf("Error updating node %s by %s : %s", apiContext.ID, actionName, err.Error()))
		}
		utils.Myprint(updated, "updated .. ")
	}
	return nil
}