package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"reflect"
	"strings"

	"github.com/golang/glog"
	"k8s.io/api/admission/v1beta1"
	appsv1 "k8s.io/api/apps/v1beta2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	StaticIPConfigAnnotation = "tke.cloud.tencent.com/enable-static-ip"
	StaticIPListAnnotation   = "tke.cloud.tencent.com/static-ip-list"
	CNINetworksAnnotation    = "tke.cloud.tencent.com/networks"
	TkeEniCNI                = "tke-route-eni"

	PatchOPType        = "replace"
	UnderlayIPJsonPath = "/spec/containers/0/resources"
	UnderlayIPResource = "tke.cloud.tencent.com/eni-ip"
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

type ThingSpec struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
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
		res := pod.Spec.Containers[0].Resources
		if res.Requests == nil {
			res.Requests = make(corev1.ResourceList)
		}
		res.Requests[UnderlayIPResource] = *resource.NewQuantity(1, resource.DecimalSI)
		if res.Limits == nil {
			res.Limits = make(corev1.ResourceList)
		}
		res.Limits[UnderlayIPResource] = *resource.NewQuantity(1, resource.DecimalSI)
		replaceBytes, err := json.Marshal(res)
		if err != nil {
			glog.Error(err)
			return toAdmissionResponse(err)
		}

		things := make([]ThingSpec, 1)
		things[0].Op = PatchOPType
		things[0].Path = UnderlayIPJsonPath
		things[0].Value = replaceBytes
		patchBytes, err := json.Marshal(things)
		if err != nil {
			glog.Error(err)
			return toAdmissionResponse(err)
		}

		reviewResponse.Patch = patchBytes
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

	glog.V(2).Info(fmt.Sprintf("handling request: %s", string(body)))
	var reviewResponse *v1beta1.AdmissionResponse
	ar := v1beta1.AdmissionReview{}
	deserializer := codecs.UniversalDeserializer()
	if _, _, err := deserializer.Decode(body, nil, &ar); err != nil {
		glog.Error(err)
		reviewResponse = toAdmissionResponse(err)
	} else {
		reviewResponse = admit(ar)
	}

	glog.V(2).Info(fmt.Sprintf("sending response: %s", formatResponse(reviewResponse)))
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

func valueToStringGenerated(v interface{}) string {
	rv := reflect.ValueOf(v)
	if rv.IsNil() {
		return "nil"
	}
	pv := reflect.Indirect(rv).Interface()
	return fmt.Sprintf("*%v", pv)
}

func formatResponse(this *v1beta1.AdmissionResponse) string {
	if this == nil {
		return "nil"
	}
	keysForAuditAnnotations := make([]string, 0, len(this.AuditAnnotations))
	for k := range this.AuditAnnotations {
		keysForAuditAnnotations = append(keysForAuditAnnotations, k)
	}
	mapStringForAuditAnnotations := "map[string]string{"
	for _, k := range keysForAuditAnnotations {
		mapStringForAuditAnnotations += fmt.Sprintf("%v: %v,", k, this.AuditAnnotations[k])
	}
	mapStringForAuditAnnotations += "}"
	s := strings.Join([]string{`&AdmissionResponse{`,
		`UID:` + fmt.Sprintf("%v", this.UID) + `,`,
		`Allowed:` + fmt.Sprintf("%v", this.Allowed) + `,`,
		`Result:` + strings.Replace(fmt.Sprintf("%v", this.Result), "Status", "k8s_io_apimachinery_pkg_apis_meta_v1.Status", 1) + `,`,
		`Patch:` + string(this.Patch) + `,`,
		`PatchType:` + valueToStringGenerated(this.PatchType) + `,`,
		`AuditAnnotations:` + mapStringForAuditAnnotations + `,`,
		`}`,
	}, "")
	return s
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
