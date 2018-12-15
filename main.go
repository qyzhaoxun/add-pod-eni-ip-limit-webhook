package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/golang/glog"
	"k8s.io/api/admission/v1beta1"
	appsv1 "k8s.io/api/apps/v1beta2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	//[{"op": "add", "path": "/spec/containers/0/resources", "value": {"requests":{"tke.cloud.tencent.com/underlay-ip-count":1},"limits":{"tke.cloud.tencent.com/underlay-ip-count":1}}}]
	addUnderlayIPRequestPatch string = `
[
  {
    "op": "add",
    "path": "/spec/containers/0/resources",
    "value": {
      "requests": {
        "tke.cloud.tencent.com/underlay-ip-count": 1
      },
      "limits": {
        "tke.cloud.tencent.com/underlay-ip-count": 1
      }
    }
  }
]
`
	StaticIPConfigAnnotation = "tke.cloud.tencent.com/enable-static-ip"
	StaticIPListAnnotation   = "tke.cloud.tencent.com/static-ip-list"
	CNINetworksAnnotation    = "tke.cloud.tencent.com/networks"
	TkeEniCNI                = "tke-eni-eni"
)

// Config contains the server (the webhook) cert and key.
type Config struct {
	CertFile string
	KeyFile  string
}

func (c *Config) addFlags() {
	flag.StringVar(&c.CertFile, "tls-cert-file", c.CertFile, ""+
		"File containing the default x509 Certificate for HTTPS. (CA cert, if any, concatenated "+
		"after server cert).")
	flag.StringVar(&c.KeyFile, "tls-private-key-file", c.KeyFile, ""+
		"File containing the default x509 private key matching --tls-cert-file.")
}

func toAdmissionResponse(err error) *v1beta1.AdmissionResponse {
	return &v1beta1.AdmissionResponse{
		Result: &metav1.Status{
			Message: err.Error(),
		},
	}
}

// mutate pods using tke-eni-cni.
func mutatePods(ar v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	glog.V(2).Info("mutating pods")
	podResource := metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	if ar.Request.Resource != podResource {
		glog.Errorf("expect resource to be %s", podResource)
		return nil
	}

	raw := ar.Request.Object.Raw
	pod := corev1.Pod{}
	deserializer := codecs.UniversalDeserializer()
	if _, _, err := deserializer.Decode(raw, nil, &pod); err != nil {
		glog.Error(err)
		return toAdmissionResponse(err)
	}
	reviewResponse := v1beta1.AdmissionResponse{}
	reviewResponse.Allowed = true
	networks, ok := pod.Annotations[CNINetworksAnnotation]
	if ok && strings.Contains(networks, TkeEniCNI) {
		reviewResponse.Patch = []byte(addUnderlayIPRequestPatch)
		pt := v1beta1.PatchTypeJSONPatch
		reviewResponse.PatchType = &pt
	}
	return &reviewResponse
}

// deny statefulsets with static ip but cni not using tke-eni-cni.
func admitStatefulSets(ar v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	glog.V(2).Info("admitting statefulsets")
	statefulSetResource := metav1.GroupVersionResource{Group: "apps", Version: "v1beta1", Resource: "statefulsets"}
	if ar.Request.Resource != statefulSetResource {
		glog.Errorf("expect resource to be %s", statefulSetResource)
		return nil
	}

	raw := ar.Request.Object.Raw
	statefulset := appsv1.StatefulSet{}
	deserializer := codecs.UniversalDeserializer()
	if _, _, err := deserializer.Decode(raw, nil, &statefulset); err != nil {
		glog.Error(err)
		return toAdmissionResponse(err)
	}
	reviewResponse := v1beta1.AdmissionResponse{}
	reviewResponse.Allowed = true
	_, ok1 := statefulset.Annotations[StaticIPConfigAnnotation]
	_, ok2 := statefulset.Annotations[StaticIPListAnnotation]
	if ok1 || ok2 {
		networks, ok := statefulset.Spec.Template.Annotations[CNINetworksAnnotation]
		if !ok || !strings.Contains(networks, TkeEniCNI) {
			reviewResponse.Allowed = false
			reviewResponse.Result = &metav1.Status{
				Reason: "the statefulset not using tke-eni-cni",
			}
		}
	}
	return &reviewResponse
}

type admitFunc func(v1beta1.AdmissionReview) *v1beta1.AdmissionResponse

func serve(w http.ResponseWriter, r *http.Request, admit admitFunc) {
	var body []byte
	if r.Body != nil {
		if data, err := ioutil.ReadAll(r.Body); err == nil {
			body = data
		}
	}

	// verify the content type is accurate
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		glog.Errorf("contentType=%s, expect application/json", contentType)
		return
	}

	glog.V(2).Info(fmt.Sprintf("handling request: %v", body))
	var reviewResponse *v1beta1.AdmissionResponse
	ar := v1beta1.AdmissionReview{}
	deserializer := codecs.UniversalDeserializer()
	if _, _, err := deserializer.Decode(body, nil, &ar); err != nil {
		glog.Error(err)
		reviewResponse = toAdmissionResponse(err)
	} else {
		reviewResponse = admit(ar)
	}
	glog.V(2).Info(fmt.Sprintf("sending response: %v", reviewResponse))

	response := v1beta1.AdmissionReview{}
	if reviewResponse != nil {
		response.Response = reviewResponse
		response.Response.UID = ar.Request.UID
	}
	// reset the Object and OldObject, they are not needed in a response.
	ar.Request.Object = runtime.RawExtension{}
	ar.Request.OldObject = runtime.RawExtension{}

	resp, err := json.Marshal(response)
	if err != nil {
		glog.Error(err)
	}
	if _, err := w.Write(resp); err != nil {
		glog.Error(err)
	}
}

func serveMutatePods(w http.ResponseWriter, r *http.Request) {
	serve(w, r, mutatePods)
}

func serveStatefulSets(w http.ResponseWriter, r *http.Request) {
	serve(w, r, admitStatefulSets)
}

func main() {
	var config Config
	config.addFlags()
	flag.Parse()

	http.HandleFunc("/mutating-pods", serveMutatePods)
	http.HandleFunc("/statefulsets", serveStatefulSets)
	clientset := getClient()
	server := &http.Server{
		Addr:      ":443",
		TLSConfig: configTLS(config, clientset),
	}
	server.ListenAndServeTLS("", "")
}
