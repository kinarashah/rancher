package nodes

import (
	"github.com/rancher/apiserver/pkg/types"
	schema2 "github.com/rancher/steve/pkg/schema"
	steve "github.com/rancher/steve/pkg/server"
	"github.com/rancher/wrangler/pkg/data"
	"github.com/rancher/wrangler/pkg/data/convert"
	"github.com/sirupsen/logrus"
)

func Register(server *steve.Server) {
	server.SchemaFactory.AddTemplate(schema2.Template{
		Group: "management.cattle.io",
		Kind:  "Node",
		Formatter: func(request *types.APIRequest, resource *types.RawResource) {
			d := resource.APIObject.Data()
			conditions := convert.ToMapSlice(data.GetValueN(resource.APIObject.Data(), "status", "conditions"))
			logrus.Infof("node value %v", conditions)
			internalConditions := convert.ToMapSlice(data.GetValueN(resource.APIObject.Data(), "status", "internalNodeStatus", "conditions"))
			logrus.Infof("node internal value %v", internalConditions)
			//conditions = append(conditions, conditions...)
			for _, cond := range internalConditions {
				cond["lastUpdateTime"] = cond["lastHeartbeatTime"]
				conditions = append(conditions, cond)
			}
			d.SetNested(conditions, "status", "conditions")

			logrus.Infof("node after append %v", resource.APIObject.Data())
		},
	})
}
