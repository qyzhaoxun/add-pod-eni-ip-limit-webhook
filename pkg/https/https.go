package https

import (
	"encoding/json"
	"fmt"
	"github.com/qyzhaoxun/add-pod-eni-ip-limit-webhook/pkg/config"
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
	TKEDirectENI          = "tke-direct-eni"
	CNINetworksAnnotation = "tke.cloud.tencent.com/networks"

	PatchOPType                                  = "replace"
	UnderlayResourceJsonPath                     = "/spec/containers/0/resources"
	UnderlayIPResource       corev1.ResourceName = "tke.cloud.tencent.com/eni-ip"
	UnderlayENIResource      corev1.ResourceName = "tke.cloud.tencent.com/direct-eni"
)

const (
	DirectResource ResourceType = "tke-direct-eni"
	RouteResource  ResourceType = "tke-route-eni"
)

type ResourceType string

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

func getPatchData(resourceType corev1.ResourceName, res corev1.ResourceRequirements) ([]byte, error) {
	if res.Limits == nil {
		res.Limits = make(corev1.ResourceList)
	}
	res.Limits[resourceType] = *resource.NewQuantity(1, resource.DecimalSI)
	replaceBytes, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}

	things := make([]ThingSpec, 1)
	things[0].Op = PatchOPType
	things[0].Path = UnderlayResourceJsonPath
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

func NewHttpsServer(defaultCNI string) HttpsServer {
	return &httpsSvr{defaultCNI: defaultCNI}
}

type httpsSvr struct {
	defaultCNI string
}

// mutate pods using tke-route-eni.
func (s *httpsSvr) mutatePods(ar v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
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
	if pod.OwnerReferences != nil && len(pod.OwnerReferences) > 0 {
		glog.V(2).Infof("mutating pod of %s %s in namespace %s", pod.OwnerReferences[0].Kind, pod.OwnerReferences[0].Name, ar.Request.Namespace)
	} else {
		glog.V(2).Infof("mutating pod %s/%s", ar.Request.Namespace, ar.Request.Name)
	}
	reviewResponse := v1beta1.AdmissionResponse{}
	reviewResponse.Allowed = true
	if pod.Spec.HostNetwork {
		if pod.OwnerReferences != nil && len(pod.OwnerReferences) > 0 {
			glog.V(2).Infof("pod of %s %s in namespace %s is HostNetwork, just return", pod.OwnerReferences[0].Kind, pod.OwnerReferences[0].Name, ar.Request.Namespace)
		} else {
			glog.V(2).Infof("pod %s/%s is HostNetwork, just return", ar.Request.Namespace, ar.Request.Name)
		}
		return &reviewResponse
	}

	var resourceToAdd ResourceType
	networks, ok := pod.Annotations[CNINetworksAnnotation]
	if ok {
		if strings.Contains(networks, TKEDirectENI) {
			resourceToAdd = DirectResource
		} else if strings.Contains(networks, TKERouteENI) {
			resourceToAdd = RouteResource
		}
	} else {
		if s.defaultCNI == config.TKEDirectENI {
			resourceToAdd = DirectResource
		} else if s.defaultCNI == config.TKERouteENI {
			resourceToAdd = RouteResource
		}
	}

	var pd []byte
	var err error

	switch resourceToAdd {
	case DirectResource:
		pd, err = getPatchData(UnderlayENIResource, pod.Spec.Containers[0].Resources)
		if err != nil {
			glog.Error(err)
			return toAdmissionResponse(err)
		}
	case RouteResource:
		pd, err = getPatchData(UnderlayIPResource, pod.Spec.Containers[0].Resources)
		if err != nil {
			glog.Error(err)
			return toAdmissionResponse(err)
		}
	default:
		glog.Infof("pod %s/%s doesn't contain annotation %s and default cni is %s. Nothing to patch",
			pod.Namespace, pod.Name, CNINetworksAnnotation, s.defaultCNI)
		return &reviewResponse
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
