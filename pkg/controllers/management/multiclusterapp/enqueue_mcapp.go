package multiclusterapp

import (
	"context"
	"github.com/rancher/rancher/pkg/namespace"
	"github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/rancher/types/config"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/runtime"
	pv3 "github.com/rancher/types/apis/project.cattle.io/v3"
)

const 	MultiClusterAppIDSelector = "mcapp"
type AppToMcappController struct {
	MultiClusterApps  v3.MultiClusterAppInterface
}

func StartAppToMCAppController(ctx context.Context, mgmt *config.ManagementContext) {
	a := AppToMcappController{
		MultiClusterApps: mgmt.Management.MultiClusterApps(""),
	}
	mgmt.Project.Apps("").AddHandler(ctx, "management-test-debug", a.sync)
}

func (a *AppToMcappController) sync(key string, app *pv3.App) (runtime.Object, error) {
	if app == nil || app.DeletionTimestamp != nil{
		return app, nil
	}
	logrus.Infof("mgmt sync for app %s", app.Name)
	if !pv3.AppConditionInstalled.IsTrue(app) {
		logrus.Infof("app installing %s", app.Name)
		return app, nil
	}
	if mcappName, ok := app.Labels[MultiClusterAppIDSelector]; ok {
		logrus.Infof("mgmt Enqueue mcapp %s", mcappName)
		a.MultiClusterApps.Controller().Enqueue(namespace.GlobalNamespace, mcappName)
	}
	return app, nil
}