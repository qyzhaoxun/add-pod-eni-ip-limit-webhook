package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/golang/glog"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	TKECNIConfCM  = "tke-cni-agent-conf"
	MultusCNIConf = "00-multus.conf"
)

type NetConf struct {
	DefaultDelegates string `json:"defaultDelegates"`
}

func getDefaultCNIFromMultus(clienset kubernetes.Interface) (bool, error) {
	var defaultCNI bool
	err := wait.PollImmediateInfinite(time.Second*3, func() (done bool, err error) {
		cm, err := clienset.CoreV1().ConfigMaps(metav1.NamespaceSystem).Get(TKECNIConfCM, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				// consider tke-route-eni is default cni
				defaultCNI = true
				return true, nil
			}
			glog.Warningf("Failed to get cm %s/%s, will retry(%v)", metav1.NamespaceSystem, TKECNIConfCM, err)
			return false, nil
		}
		// get defaultDelegates from key 00-multus.conf
		str, ok := cm.Data[MultusCNIConf]
		if ok {
			var netConf NetConf
			bytes := []byte(str)
			err = json.Unmarshal(bytes, &netConf)
			if err != nil {
				return false, err
			}
			if strings.Contains(netConf.DefaultDelegates, TKERouteENI) {
				defaultCNI = true
			}
			glog.Infof("default cni is %s, set defaultCNI to %t", netConf.DefaultDelegates, defaultCNI)
			return true, nil
		} else {
			return false, fmt.Errorf("no %s key found in cm %s/%s", MultusCNIConf, metav1.NamespaceSystem, TKECNIConfCM)
		}
		return true, nil
	})
	return defaultCNI, err
}
