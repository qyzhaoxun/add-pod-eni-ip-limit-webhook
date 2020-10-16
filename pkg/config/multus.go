package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/golang/glog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	TKECNIConfCM  = "tke-cni-agent-conf"
	MultusCNIConf = "00-multus.conf"
	TKERouteENI   = "tke-route-eni"
	TKEDirectENI  = "tke-direct-eni"
	TKEBridge     = "tke-bridge"
	Other         = "other"
)

type NetConf struct {
	DefaultDelegates string `json:"defaultDelegates"`
}

func GetDefaultCNIFromMultus(clienset kubernetes.Interface) (string, error) {
	var defaultCNI string
	err := wait.PollImmediateInfinite(time.Second*3, func() (done bool, err error) {
		cm, err := clienset.CoreV1().ConfigMaps(metav1.NamespaceSystem).Get(TKECNIConfCM, metav1.GetOptions{})
		if err != nil {
			time.Sleep(1 * time.Minute)
			cm, err = clienset.CoreV1().ConfigMaps(metav1.NamespaceSystem).Get(TKECNIConfCM, metav1.GetOptions{})
			if err != nil {
				glog.Warningf("Failed to get cm %s/%s", metav1.NamespaceSystem, TKECNIConfCM)
				return false, err
			}
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
				defaultCNI = TKERouteENI
			} else if strings.Contains(netConf.DefaultDelegates, TKEDirectENI) {
				defaultCNI = TKEDirectENI
			} else if strings.Contains(netConf.DefaultDelegates, TKEBridge) {
				defaultCNI = TKEBridge
			} else {
				defaultCNI = Other
				glog.Warningf("No default cni included in cm.")
			}
			return true, nil
		}
		return false, fmt.Errorf("no %s key found in cm %s/%s", MultusCNIConf, metav1.NamespaceSystem, TKECNIConfCM)
	})
	return defaultCNI, err
}
