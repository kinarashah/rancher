package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rancher/norman/types/convert"
	"github.com/rancher/rancher/pkg/namespace"
	v1 "github.com/rancher/types/apis/core/v1"
	client "github.com/rancher/types/client/management/v3"
	"github.com/rancher/types/config"
	"github.com/rancher/types/user"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1k8s "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type UserChan struct {
	UserId string
	WsId   string
}

type Handler struct {
	userMGR         user.Manager
	configMapLister v1.ConfigMapLister
	configMaps      v1.ConfigMapInterface
}

var (
	Done        = make(chan UserChan)
	mapLock     = sync.Mutex{}
	connections = map[string]*websocket.Conn{}
)

func KubeConfigTokenHander(ctx context.Context, mgmt *config.ScaledContext) *Handler {
	handler := &Handler{
		userMGR:         mgmt.UserManager,
		configMaps:      mgmt.Core.ConfigMaps(""),
		configMapLister: mgmt.Core.ConfigMaps("").Controller().Lister(),
	}
	return handler
}

func (t *Handler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	logrus.Infof("enter here!!! ")
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	wsConn, err := upgrader.Upgrade(rw, req, nil)
	if err != nil {
		logrus.Infof("kubeConfigHandler: error upgrading conn [%v]", err)
		return
	}
	defer wsConn.Close()

	logrus.Infof("now starting for loop")

	endChan := make(chan bool)

	wsId := ""

	// read msg from client, parse websocketId and store conn
	go func() {
		for {
			logrus.Infof("read channel")
			_, message, err := wsConn.ReadMessage()
			if err != nil {
				logrus.Infof("kubeConfigHandler: error reading msg: [%v]", err)
				break
			}

			logrus.Infof("kubeConfigHandler: received msg [%s]", message)

			data := map[string]string{}

			err = json.Unmarshal(message, &data)
			if err != nil {
				logrus.Infof("kubeConfigHandler: error unmarshaling msg [%v]", err)
				break
			}

			wsId = data["wsId"]

			if wsId == "" {
				continue
			}

			mapLock.Lock()
			connections[wsId] = wsConn
			mapLock.Unlock()

			if err := t.initialize(wsId); err != nil {
				err = wsConn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("InitializeErr %v", err)))
				if err != nil {
					logrus.Infof("kubeConfigHandler: error writing msg [%v]:", err)
					break
				}
			}
		}

		logrus.Infof("got close? key %s", wsId)
		endChan <- true
	}()

	// wait to get userId post login, generate token and send to client
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			userName, err := t.getUser(wsId)
			if err != nil {
				logrus.Debug("getUser: %v", err)
			}

			clusterId := ""

			name := "kubeconfig-" + userName
			if clusterId != "" {
				name = fmt.Sprintf("kubeconfig-%s.%s", userName, clusterId)
			}

			logrus.Infof("token name %s", name)

			token, err := t.userMGR.GetToken(clusterId, name, "Kubeconfig token", "kubeconfig", userName)
			if err != nil {
				logrus.Infof("error getting token %s %v", name, err)
				continue
			}

			mapLock.Lock()
			wsConn := connections[wsId]
			mapLock.Unlock()

			tokenData, err := convert.EncodeToMap(token)
			if err != nil {
				logrus.Infof("encoding err %v", err)
				continue
			}

			test := client.Token{
				Name:      token.ObjectMeta.Name,
				Token:     token.ObjectMeta.Name + ":" + token.Token,
				ExpiresAt: token.ExpiresAt,
			}

			logrus.Infof("bblll %#v", test, tokenData)

			tokenData["token"] = token.ObjectMeta.Name + ":" + token.Token

			ans, _ := json.Marshal(tokenData)

			err = wsConn.WriteMessage(websocket.TextMessage, ans)
			if err != nil {
				logrus.Println("write:", err)
				return
			}

			logrus.Infof("sent correctly???")

		//case x := <-Done:
		//	logrus.Infof("saml done response: %s", x.UserId)
		//
		//	userName := x.UserId
		//	clusterId := ""
		//
		//	name := "kubeconfig-" + userName
		//	if clusterId != "" {
		//		name = fmt.Sprintf("kubeconfig-%s.%s", userName, clusterId)
		//	}
		//
		//	_, err := t.userMGR.GetToken(clusterId, name, "Kubeconfig token", "kubeconfig", userName)
		//	if err != nil {
		//		//msg = fmt.Sprintf("error getting token %s %v", name, err)
		//		logrus.Infof("error getting token %s %v", name, err)
		//		continue
		//	}
		//
		//	mapLock.Lock()
		//	wsConn := connections[x.WsId]
		//	mapLock.Unlock()
		//
		//	err = wsConn.WriteMessage(websocket.TextMessage, []byte(x.UserId))
		//	if err != nil {
		//		logrus.Println("write:", err)
		//		return
		//	}
		//
		//	logrus.Infof("sent correctly???")

		case y := <-endChan:
			logrus.Infof("received end!!!!! %v", y)
			return

		}
	}

	mapLock.Lock()
	delete(connections, wsId)
	logrus.Infof("coming out of for loop, will now close websocket here!")
}

func (t *Handler) initialize(id string) error {
	// add retries
	cfgmap := &corev1.ConfigMap{
		ObjectMeta: v1k8s.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", "status", id),
			Namespace: namespace.GlobalNamespace,
		},
	}

	_, err := t.configMaps.Create(cfgmap)
	return err
}

func SetUser(id string, data map[string]string, cfgMapLister v1.ConfigMapLister, cfgMap v1.ConfigMapInterface) error {
	name := fmt.Sprintf("%s-%s", "status", id)
	obj, err := cfgMapLister.Get(namespace.GlobalNamespace, name)
	if err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
		obj, err = cfgMap.GetNamespaced(namespace.GlobalNamespace, name, metav1.GetOptions{})
		if err != nil {
			return err
		}

	}

	if reflect.DeepEqual(obj.Data, data) {
		return nil
	}

	objCopy := obj.DeepCopy()
	objCopy.Data = data

	_, err = cfgMap.Update(objCopy)
	return err
}

func (t *Handler) getUser(id string) (string, error) {
	name := fmt.Sprintf("%s-%s", "status", id)
	obj, err := t.configMapLister.Get(namespace.GlobalNamespace, name)
	if err != nil {
		return "", err
	}
	if len(obj.Data) == 0 {
		return "", fmt.Errorf("empty data")
	}

	return obj.Data["userId"], nil
}
