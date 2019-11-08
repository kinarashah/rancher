package alert

import (
	"encoding/json"
	"github.com/rancher/norman/types/convert"
	"github.com/sirupsen/logrus"
	"io/ioutil"

	"github.com/pkg/errors"
	"github.com/rancher/norman/httperror"
	"github.com/rancher/norman/types"
	"github.com/rancher/rancher/pkg/notifiers"
	"github.com/rancher/rancher/pkg/ref"
	"github.com/rancher/types/apis/management.cattle.io/v3"
	client "github.com/rancher/types/client/management/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const testSMTPTitle = "Alert From Rancher: SMTP configuration validated"

func NotifierCollectionFormatter(apiContext *types.APIContext, collection *types.GenericCollection) {
	collection.AddAction(apiContext, "send")
}

func NotifierFormatter(apiContext *types.APIContext, resource *types.RawResource) {
	resource.AddAction(apiContext, "send")
}

func (h *Handler) NotifierActionHandler(actionName string, action *types.Action, apiContext *types.APIContext) error {
	switch actionName {
	case "send":
		return h.testNotifier(actionName, action, apiContext)
	}

	return httperror.NewAPIError(httperror.InvalidAction, "invalid action: "+actionName)

}

func (h *Handler) testNotifier(actionName string, action *types.Action, apiContext *types.APIContext) error {
	data, err := ioutil.ReadAll(apiContext.Request.Body)
	if err != nil {
		return errors.Wrap(err, "reading request body error")
	}
	datamp, err := convert.EncodeToMap(data)
	logrus.Infof("data %v", datamp)

	input := &struct {
		Message string
		v3.NotifierSpec
	}{}

	clientNotifier := &struct {
		client.NotifierSpec
	}{}

	if err = json.Unmarshal(data, input); err != nil {
		return errors.Wrap(err, "unmarshaling input error")
	}

	if err = json.Unmarshal(data, clientNotifier); err != nil {
		return errors.Wrap(err, "unmarshaling input error client")
	}
	notifier := &v3.Notifier{
		Spec: input.NotifierSpec,
	}
	ans, _ := json.Marshal(input)
	logrus.Infof("input %v", string(ans))

	ans, _ = json.Marshal(clientNotifier)
	logrus.Infof("client %v", string(ans))


	msg := input.Message
	ns, id := ref.Parse(apiContext.ID)
	//logrus.Infof("kinara clusterName %s", convert.ToString(convert.ToMapInterface(data)["clusterId"]))
	if apiContext.ID != "" {
		notifier, err = h.Notifiers.GetNamespaced(ns, id, metav1.GetOptions{})
		if err != nil {
			return err
		}
	}
	notifierMessage := &notifiers.Message{
		Content: msg,
	}
	if notifier.Spec.SMTPConfig != nil {
		notifierMessage.Title = testSMTPTitle
	}
	logrus.Infof("kinara cluster name %s", clientNotifier.ClusterID)
	dialer, err := h.DialerFactory.ClusterDialer(clientNotifier.ClusterID)
	if err != nil {
		logrus.Infof("kinara error getting dialer? %v", err)
	}
	return notifiers.SendMessage(notifier, "", notifierMessage, dialer)
}
