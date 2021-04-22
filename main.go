package main

import (
	"crypto/tls"
	"flag"
	log "github.com/cihub/seelog"
	"github.com/qyzhaoxun/add-pod-eni-ip-limit-webhook/pkg/client"
	wenhookconfig "github.com/qyzhaoxun/add-pod-eni-ip-limit-webhook/pkg/config"
	"github.com/qyzhaoxun/add-pod-eni-ip-limit-webhook/pkg/https"
	"github.com/qyzhaoxun/add-pod-eni-ip-limit-webhook/pkg/util"
	"github.com/qyzhaoxun/add-pod-eni-ip-limit-webhook/pkg/util/logger"
	"k8s.io/client-go/kubernetes"
	"net/http"
)

const (
	defaultLogFilePath = "/host/var/log/tke-route-eni/eni-webhook.log"
)

var (
	version string
	config  Config
)

func configTLS(config Config) *tls.Config {
	sCert, err := tls.X509KeyPair([]byte(config.Cert), []byte(config.Key))
	if err != nil {
		log.Error(err)
		return nil
	}
	return &tls.Config{
		Certificates: []tls.Certificate{sCert},
	}
}

// Config contains the server (the webhook) cert and key.
type Config struct {
	Cert       string
	Key        string
	InCluster  bool
	Master     string
	KubeConfig string
	DefaultCNI string
}

func (c *Config) addFlags() {
	flag.BoolVar(&c.InCluster, "incluster", true, "Whether agent runs on incluster.")
	flag.StringVar(&c.Master, "master", c.Master, "The address of the Kubernetes API server (overrides any value in kubeconfig).")
	flag.StringVar(&c.KubeConfig, "kubeconfig", c.KubeConfig, "Path to kubeconfig file with authorization and master location information.")
}

func init() {
	config.addFlags()
	flag.Parse()
}

func main() {
	logger.SetupLogger(logger.GetLogFileLocation(defaultLogFilePath))
	flag.VisitAll(func(i *flag.Flag) {
		log.Debugf("FLAG: --%s=%q", i.Name, i.Value)
	})
	log.Debugf("Version: %+v", version)

	var defaultCNI string
	var kubeClient kubernetes.Interface
	var err error

	kubeClient, err = client.GetKubeClient(config.InCluster, config.Master, config.KubeConfig)
	if err != nil {
		log.Errorf("Failed to get kube client: %v", err)
		return
	}
	defaultCNI, err = wenhookconfig.GetDefaultCNIFromMultus(kubeClient)
	if err != nil {
		log.Errorf("Failed to determine which is default cni: %s", err.Error())
		return
	}

	log.Infof("Default CNI is %s", defaultCNI)

	crtConfig, err := util.GenCrt(kubeClient, config.InCluster)
	if err != nil {
		log.Errorf("failed to generate crt and key: %s", err.Error())
		return
	}
	if crtConfig == nil {
		log.Errorf("failed to generate crt and key.")
		return
	}
	config.Cert = crtConfig.Cert
	config.Key = crtConfig.Key
	hs := https.NewHttpsServer(defaultCNI)
	http.HandleFunc(util.Path, hs.ServeHttps)
	tlsConfig := configTLS(config)
	if tlsConfig == nil {
		return
	}
	server := &http.Server{
		Addr:      ":61679",
		TLSConfig: tlsConfig,
	}
	server.ListenAndServeTLS("", "")
}
