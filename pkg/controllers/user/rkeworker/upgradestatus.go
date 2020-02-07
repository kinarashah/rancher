package rkeworker

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/rancher/types/apis/core/v1"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	lastAppliedKey = "lastAppliedToken"
	currentKey     = "currentToken"
	nodeKey        = "nodes"
)

type Store struct {
	ns           string
	cfgMapLister v1.ConfigMapLister
	cfgMaps      v1.ConfigMapInterface
}

type UpgradeStatus struct {
	lastApplied string
	current     string
	nodes       map[string]string
}

func NewStore(cfgMapLister v1.ConfigMapLister, cfgMapInterface v1.ConfigMapInterface, ns string) *Store {
	return &Store{
		cfgMapLister: cfgMapLister,
		cfgMaps:      cfgMapInterface,
		ns:           ns,
	}
}

func (s *Store) Load() (*UpgradeStatus, error) {
	cfg, err := s.get()
	if err != nil {
		return nil, err
	}

	us := &UpgradeStatus{}

	lastApplied, ok := cfg.Data[lastAppliedKey]
	if !ok {
		return us, nil
	}
	us.lastApplied = lastApplied

	current, ok := cfg.Data[currentKey]
	if !ok {
		return us, nil
	}
	us.current = current

	nodes := map[string]map[string]string{}

	if val, ok := cfg.Data[nodeKey]; ok {
		if err := json.Unmarshal([]byte(val), &nodes); err != nil {
			return nil, err
		}

		if val, ok := nodes[us.current]; ok {
			us.nodes = val
		}
	}
	if us.nodes == nil {
		us.nodes = map[string]string{}
	}

	logrus.Infof("Load() %s %v", s.ns, *us)
	return us, nil
}

func (s *Store) Save(status *UpgradeStatus) error {
	logrus.Infof("Save() %s %v", s.ns, status)

	data := map[string]string{}

	current := status.current

	if current != "" {
		nodes := map[string]map[string]string{}
		nodes[current] = status.nodes

		val, err := json.Marshal(nodes)
		if err != nil {
			return err
		}

		data[nodeKey] = string(val)
		data[currentKey] = current
	}

	data[lastAppliedKey] = status.lastApplied

	if err := s.set(data); err != nil {
		return nil
	}

	return nil
}

func (s *Store) set(data map[string]string) error {
	logrus.Infof("set() %s %v", s.ns, data)
	cfg, err := s.get()
	if err != nil {
		return err
	}

	if !reflect.DeepEqual(cfg.Data, data) {
		cfgCopy := cfg.DeepCopy()
		cfgCopy.Data = data

		logrus.Infof("actually updating!")
		if _, err := s.cfgMaps.Update(cfgCopy); err != nil {
			return err
		}
	}

	return nil
}

func (s *Store) get() (*corev1.ConfigMap, error) {
	cfg, err := s.cfgMapLister.Get(s.ns, UpgradeCfgName)
	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, err
		}

		cfg, err = s.cfgMaps.GetNamespaced(s.ns, UpgradeCfgName, metav1.GetOptions{})
		if err != nil {
			if !errors.IsNotFound(err) {
				return nil, err
			}
			cfg = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      UpgradeCfgName,
					Namespace: s.ns,
				},
			}

			cfg, err = s.cfgMaps.Create(cfg)
			if err != nil && !errors.IsAlreadyExists(err) {
				return nil, fmt.Errorf("error creating upgrade status configmap for cluster [%s]: [%v] %+v", s.ns, err, cfg)
			}
		}
	}

	logrus.Infof("get() %s %v", s.ns, cfg)
	return cfg, nil
}
