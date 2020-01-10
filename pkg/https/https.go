package https

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"reflect"
	"strings"

	"github.com/qyzhaoxun/add-pod-eni-ip-limit-webhook/pkg/schema"

	"k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/golang/glog"
)

const (
	TKERouteENI           = "tke-route-eni"
	CNINetworksAnnotation = "tke.cloud.tencent.com/networks"

	PatchOPType        = "replace"
	UnderlayIPJsonPath = "/spec/containers/0/resources"
	UnderlayIPResource = "tke.cloud.tencent.com/eni-ip"
)

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

func getPatchData(res corev1.ResourceRequirements) ([]byte, error) {
	if res.Limits == nil {
		res.Limits = make(corev1.ResourceList)
	}
	res.Limits[UnderlayIPResource] = *resource.NewQuantity(1, resource.DecimalSI)
	replaceBytes, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}

	things := make([]ThingSpec, 1)
	things[0].Op = PatchOPType
	things[0].Path = UnderlayIPJsonPath
	things[0].Value = replaceBytes
	patchBytes, err := json.Marshal(things)
	if err != nil {
		return nil, err
	}
	return patchBytes, nil
}

type admitFunc func(v1beta1.AdmissionReview) *v1beta1.AdmissionResponse

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

type HttpsServer interface {
	ServeHttps(w http.ResponseWriter, r *http.Request)
}

func NewHttpsServer(defaultCNI bool) HttpsServer {
	return &httpsSvr{defaultCNI: defaultCNI}
}

type httpsSvr struct {
	defaultCNI bool
}

// mutate pods using tke-route-eni.
func (s *httpsSvr) mutatePods(ar v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	glog.V(2).Info("mutating pods")
	podResource := metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	if ar.Request.Resource != podResource {
		glog.Errorf("expect resource to be %s", podResource)
		return nil
	}

	raw := ar.Request.Object.Raw
	pod := corev1.Pod{}
	deserializer := schema.Codecs.UniversalDeserializer()
	if _, _, err := deserializer.Decode(raw, nil, &pod); err != nil {
		glog.Error(err)
		return toAdmissionResponse(err)
	}
	reviewResponse := v1beta1.AdmissionResponse{}
	reviewResponse.Allowed = true
	if pod.Spec.HostNetwork {
		glog.V(3).Infof("hostNetwork pod %s/%s, just return", pod.Namespace, pod.Name)
		return &reviewResponse
	}

	var toAdd bool
	networks, ok := pod.Annotations[CNINetworksAnnotation]
	if ok {
		if strings.Contains(networks, TKERouteENI) {
			toAdd = true
		}
	} else {
		if s.defaultCNI {
			toAdd = true
		}
	}
	if !toAdd {
		glog.V(3).Infof("not %s pod %s/%s, just return", TKERouteENI, pod.Namespace, pod.Name)
		return &reviewResponse
	}

	pd, err := getPatchData(pod.Spec.Containers[0].Resources)
	if err != nil {
		glog.Error(err)
		return toAdmissionResponse(err)
	}
	reviewResponse.Patch = pd
	pt := v1beta1.PatchTypeJSONPatch
	reviewResponse.PatchType = &pt
	return &reviewResponse
}

func (s *httpsSvr) serve(w http.ResponseWriter, r *http.Request, admit admitFunc) {
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

	glog.V(4).Info(fmt.Sprintf("handling request: %s", string(body)))
	var reviewResponse *v1beta1.AdmissionResponse
	ar := v1beta1.AdmissionReview{}
	deserializer := schema.Codecs.UniversalDeserializer()
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

func (s *httpsSvr) ServeHttps(w http.ResponseWriter, r *http.Request) {
	s.serve(w, r, s.mutatePods)
}
