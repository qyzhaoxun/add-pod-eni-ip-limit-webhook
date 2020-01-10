package main

import (
	"crypto/tls"
	"flag"
	"net/http"

	"github.com/qyzhaoxun/add-pod-eni-ip-limit-webhook/pkg/client"
	wenhookconfig "github.com/qyzhaoxun/add-pod-eni-ip-limit-webhook/pkg/config"
	"github.com/qyzhaoxun/add-pod-eni-ip-limit-webhook/pkg/https"

	"github.com/golang/glog"
)

var (
	version string
	config  Config
)

func configTLS(config Config) *tls.Config {
	sCert, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
	if err != nil {
		glog.Fatal(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{sCert},
		// TODO: uses mutual tls after we agree on what cert the apiserver should use.
		// ClientAuth:   tls.RequireAndVerifyClientCert,
	}
}

// Config contains the server (the webhook) cert and key.
type Config struct {
	CertFile   string
	KeyFile    string
	InCluster  bool
	Master     string
	KubeConfig string
	PresetMode bool
	DefaultCNI bool
}

func (c *Config) addFlags() {
	flag.StringVar(&c.CertFile, "tls-cert-file", c.CertFile, ""+
		"File containing the default x509 Certificate for HTTPS. (CA cert, if any, concatenated "+
		"after server cert).")
	flag.StringVar(&c.KeyFile, "tls-private-key-file", c.KeyFile, ""+
		"File containing the default x509 private key matching --tls-cert-file.")
	flag.BoolVar(&c.InCluster, "incluster", true, "Whether agent runs on incluster.")
	flag.StringVar(&c.Master, "master", c.Master, "The address of the Kubernetes API server (overrides any value in kubeconfig).")
	flag.StringVar(&c.KubeConfig, "kubeconfig", c.KubeConfig, "Path to kubeconfig file with authorization and master location information.")
	flag.BoolVar(&c.PresetMode, "preset-mode", c.PresetMode, "Whether webhook running on preset mode.")
	flag.BoolVar(&c.DefaultCNI, "default-cni", c.DefaultCNI, "Whether tke-route-eni is default-cni(need preset-mode=true).")
}

func init() {
	config.addFlags()
	flag.Parse()
}

func main() {
	flag.VisitAll(func(i *flag.Flag) {
		glog.V(2).Infof("FLAG: --%s=%q", i.Name, i.Value)
	})
	glog.V(2).Infof("Version: %+v", version)

	var defaultCNI bool
	if config.PresetMode {
		defaultCNI = config.DefaultCNI
	} else {
		cs, err := client.GetKubeClient(config.InCluster, config.Master, config.KubeConfig)
		if err != nil {
			glog.Fatalf("Failed to get kube client: %v", err)
		}
		defaultCNI, err = wenhookconfig.GetDefaultCNIFromMultus(cs)
		if err != nil {
			glog.Fatalf("Failed to determine whether %s is default cni, %v", https.TKERouteENI, err)
		}
	}

	glog.Infof("Whether %s is default cni: %t", https.TKERouteENI, defaultCNI)
	hs := https.NewHttpsServer(defaultCNI)
	http.HandleFunc("/add-pod-eni-ip-limit", hs.ServeHttps)
	server := &http.Server{
		Addr:      ":443",
		TLSConfig: configTLS(config),
	}
	server.ListenAndServeTLS("", "")
}
