package util

import (
	"fmt"
	log "github.com/cihub/seelog"
	"github.com/cloudflare/cfssl/cli"
	"github.com/cloudflare/cfssl/cli/genkey"
	"github.com/cloudflare/cfssl/cli/sign"
	"github.com/cloudflare/cfssl/csr"
	"github.com/cloudflare/cfssl/initca"
	"github.com/cloudflare/cfssl/signer"
	"io/ioutil"
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	WebhookName                            = "add-pod-eni-ip-limit-webhook"
	ServiceName                            = "add-pod-eni-ip-limit-webhook"
	NamespaceKubeSystem                    = "kube-system"
	SecretName                             = "eni-ip-webhook-certs"
	Path                                   = "/add-pod-eni-ip-limit"
	POD_ENI_IP_LIMIT_WEBHOOK_MUTATING_NAME = "add-pod-eni-ip-limit-webhook.tke.cloud.tencent.com"
	TKE_ENI_IP_NS_LABEL_KEY                = "not-add-pod-eni-ip-limit"
)

// CertConfig contains the server (the webhook) cert and key.
type CertConfig struct {
	Cert string
	Key  string
}

func GenCrt(kubeClient kubernetes.Interface, incluster bool) (*CertConfig, error) {
	namespace := os.Getenv("NAMESPACE")
	caCert, webhookKey, webhookCert, err := genCrt(namespace)
	if err != nil {
		return nil, err
	}
	err = genSecret(namespace, webhookKey, webhookCert, kubeClient, incluster)
	if err != nil {
		return nil, err
	}
	err = genMutatingWebhookConfiguration(namespace, caCert, kubeClient, incluster)
	if err != nil {
		return nil, err
	}
	if !incluster {
		namespace = NamespaceKubeSystem
	}
	secret, err := kubeClient.CoreV1().Secrets(namespace).Get(SecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Warnf("user may delete the secret. we now use the crt data we generated instead of the data in secret.")
		} else {
			log.Errorf("failed to get secret: %s", err.Error())
		}
	}
	// compare the data
	crtDataFromSecret := string(secret.Data["tls.crt"])
	keyDataFromSecret := string(secret.Data["tls.key"])
	if crtDataFromSecret == string(webhookCert) && keyDataFromSecret == string(webhookKey) {
		log.Infof("success to get secret")
	} else {
		log.Warnf("the data in secret is not the crt and key we built")
	}

	resultCrt := CertConfig{
		Cert: string(webhookCert),
		Key:  string(webhookKey),
	}

	return &resultCrt, nil
}

func genCrt(namespace string) (CACert []byte, WebhookKey []byte, WebhookSert []byte, err error) {
	//1. Create Ca Cert and Ca key
	caCert, caKey, err := newCa(namespace)
	if err != nil {
		return nil, nil, nil, err
	}

	//2. write the Ca Cert and Ca Key to tmp file
	caCertFile, caKeyFile, err := writeTmpFile(caCert, caKey)
	defer os.Remove(caCertFile.Name())
	defer os.Remove(caKeyFile.Name())
	if err != nil {
		return nil, nil, nil, err
	}

	//3. Create Ca config and write it to the file.
	caConfigFile, err := createCaConfig()
	defer os.Remove(caConfigFile.Name())

	//4. create webhook csr
	webhookCSR, webhookKey, err := createWebhookCSR(namespace)
	if err != nil {
		log.Errorf("Create webhook Cert failed %s", err.Error())
		return nil, nil, nil, err
	}

	//5. sign crt
	webhookCert, err := signCertForWebhook(caCertFile, caKeyFile, caConfigFile, webhookCSR)
	if err != nil {
		return nil, nil, nil, err
	}
	return caCert, webhookKey, webhookCert, nil
}

func signCertForWebhook(caCertFile, caKeyFile, caConfigFile *os.File, webhookCSR []byte) ([]byte, error) {
	//Create Signer
	signConfig := cli.Config{
		CAFile:     caCertFile.Name(),
		CAKeyFile:  caKeyFile.Name(),
		ConfigFile: caConfigFile.Name(),
		Profile:    "webhook",
	}
	webhookSigner, err := sign.SignerFromConfig(signConfig)
	if err != nil {
		log.Errorf("SignerFromConfig Webhook failed %s", err.Error())
		return nil, err
	}
	signRequest := signer.SignRequest{
		Request:   string(webhookCSR),
		Profile:   "webhook",
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(175300 * time.Hour),
	}
	// Use Signer to sign cert
	webhookCert, err := webhookSigner.Sign(signRequest)
	if err != nil {
		log.Errorf("Sign Webhook failed %s", err.Error())
		return nil, err
	}
	return webhookCert, nil
}

func createWebhookCSR(namespace string) ([]byte, []byte, error) {
	webhookCertificateRequest := csr.CertificateRequest{
		CN: fmt.Sprintf("%s-.%s", WebhookName, namespace),
		Names: []csr.Name{
			{
				C:  "CN",
				ST: "GuangDong",
				L:  "ShenZhen",
				O:  "Tencent Technology (Shenzhen) Company Limited",
				OU: "TKE",
			},
		},
		KeyRequest: &csr.KeyRequest{
			A: "rsa",
			S: 2048,
		},
		Hosts: []string{
			"127.0.0.1",
			fmt.Sprintf("%s.%s", ServiceName, namespace),
			fmt.Sprintf("%s.%s.svc", ServiceName, namespace),
			fmt.Sprintf("%s.%s.svc.cluster", ServiceName, namespace),
			fmt.Sprintf("%s.%s.svc.cluster.local", ServiceName, namespace),
		},
	}
	generator := &csr.Generator{Validator: genkey.Validator}
	webhookCSR, webhookKey, err := generator.ProcessRequest(&webhookCertificateRequest)
	if err != nil {
		log.Errorf("ProcessRequest failed %s", err.Error())
		return nil, nil, err
	}
	return webhookCSR, webhookKey, nil
}

func newCa(namespace string) ([]byte, []byte, error) {
	caCertificateRequest := &csr.CertificateRequest{
		CN: fmt.Sprintf("%s.%s", ServiceName, namespace),
		Names: []csr.Name{
			{
				C:  "CN",
				ST: "GuangDong",
				L:  "ShenZhen",
				O:  "Tencent Technology (Shenzhen) Company Limited",
				OU: "TKE",
			},
		},
		KeyRequest: &csr.KeyRequest{
			A: "rsa",
			S: 2048,
		},
		CA: &csr.CAConfig{
			Expiry: "175300h",
		},
	}
	caCert, _, caKey, err := initca.New(caCertificateRequest)
	if err != nil {
		log.Errorf("InitCA Failed %s", err.Error())
		return nil, nil, err
	}
	return caCert, caKey, nil
}

func writeTmpFile(caCert, caKey []byte) (*os.File, *os.File, error) {
	caCertFile, err := ioutil.TempFile("", "ca.pem")
	if err != nil {
		log.Errorf("Tmp file err %s", err.Error())
		return nil, nil, err
	}

	if _, err := caCertFile.Write(caCert); err != nil {
		log.Errorf("Write CA Pem failed %s", err.Error())
		return nil, nil, err
	}
	caKeyFile, err := ioutil.TempFile("", "ca-key.pem")
	if err != nil {
		log.Errorf("Tmp file err %s", err.Error())
		return nil, nil, err
	}
	if _, err := caKeyFile.Write(caKey); err != nil {
		log.Errorf("Write CA Key Pem failed %s", err.Error())
		return nil, nil, err
	}
	return caCertFile, caKeyFile, nil
}

func createCaConfig() (*os.File, error) {
	caConfig := &CAConfig{
		Signing: &Signing{
			Profiles: map[string]*SigningProfile{
				"webhook": {
					ExpiryString: "175300h", // 20年
					Usage: []string{
						"signing",
						"key encipherment",
						"server auth",
						"client auth",
					},
				},
			},
			Default: &SigningProfile{
				ExpiryString: "175300h",
				Usage: []string{
					"signing",
					"key encipherment",
					"server auth",
					"client auth",
				},
			},
		},
	}
	caConfigFile, err := ioutil.TempFile("", "ca-config.json")
	if err != nil {
		log.Errorf("Create temp file for Ca Config failed %s", err.Error())
		return nil, err
	}
	if _, err := caConfigFile.WriteString(JsonWrapper(caConfig)); err != nil {
		log.Errorf("Write CA Config failed %s", err.Error())
		return nil, err
	}
	return caConfigFile, nil
}

func genSecret(namespace string, webhookKey, webhookCert []byte, kubeClient kubernetes.Interface, isInCluster bool) error {
	// 如果Namespace不是"tke-eni-ip-webhook"则集群属于托管集群。此时将secret建在用户集群的kube-system下
	if !isInCluster {
		namespace = NamespaceKubeSystem
	}
	secret := &v1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: v1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      SecretName,
		},
		StringData: map[string]string{
			"tls.key": string(webhookKey),
			"tls.crt": string(webhookCert),
		},
		Type: v1.SecretTypeOpaque,
	}
	currentSecret, secretErr := kubeClient.CoreV1().Secrets(namespace).Get(SecretName, metav1.GetOptions{})
	if secretErr != nil && !apierrors.IsNotFound(secretErr) { // secret 特殊错误返回
		return fmt.Errorf("Unexpected kubernetes client secret error. %s", secretErr.Error())
	} else if secretErr != nil && apierrors.IsNotFound(secretErr) {
		_, err := kubeClient.CoreV1().Secrets(namespace).Create(secret)
		if err != nil {
			return err
		}
	} else {
		secret.ObjectMeta.ResourceVersion = currentSecret.ResourceVersion
		_, err := kubeClient.CoreV1().Secrets(namespace).Update(secret)
		if err != nil {
			return err
		}
	}
	return nil
}

func genMutatingWebhookConfiguration(namespace string, caCert []byte, kubeClient kubernetes.Interface, isInCluster bool) error {
	var serviceReference admissionregistrationv1beta1.ServiceReference
	serviceReference.Namespace = namespace
	serviceReference.Name = ServiceName
	serviceReference.Path = StringPoint(Path)
	isVersion10, err := IsClusterVersion1_10(kubeClient)
	if err != nil {
		return err
	}
	var failurePolicyPoint *admissionregistrationv1beta1.FailurePolicyType
	if isVersion10 {
		failurePolicyPoint = FailurePolicyPoint(admissionregistrationv1beta1.Ignore)
	} else {
		failurePolicyPoint = FailurePolicyPoint(admissionregistrationv1beta1.Fail)
	}

	var namespaceSelector metav1.LabelSelector
	namespaceSelector.MatchExpressions = append(namespaceSelector.MatchExpressions,
		metav1.LabelSelectorRequirement{Key: TKE_ENI_IP_NS_LABEL_KEY, Operator: metav1.LabelSelectorOpDoesNotExist})

	var clientConfig admissionregistrationv1beta1.WebhookClientConfig
	if isInCluster {
		clientConfig = admissionregistrationv1beta1.WebhookClientConfig{
			Service:  &serviceReference,
			CABundle: caCert,
		}
	} else {
		clusterId := namespace
		clientConfig = admissionregistrationv1beta1.WebhookClientConfig{
			URL:      StringPoint(fmt.Sprintf("https://%s.%s.svc.cluster.local:%d%s", WebhookName, clusterId, 61679, Path)),
			CABundle: caCert,
		}
	}

	configuration := &admissionregistrationv1beta1.MutatingWebhookConfiguration{
		TypeMeta: metav1.TypeMeta{
			Kind:       "MutatingWebhookConfiguration",
			APIVersion: admissionregistrationv1beta1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: WebhookName,
		},
		Webhooks: []admissionregistrationv1beta1.MutatingWebhook{
			{
				Name:              POD_ENI_IP_LIMIT_WEBHOOK_MUTATING_NAME,
				FailurePolicy:     failurePolicyPoint,
				NamespaceSelector: &namespaceSelector,
				ClientConfig:      clientConfig,
				Rules: []admissionregistrationv1beta1.RuleWithOperations{
					{
						Operations: []admissionregistrationv1beta1.OperationType{admissionregistrationv1beta1.Create},
						Rule: admissionregistrationv1beta1.Rule{
							APIGroups:   []string{""},
							APIVersions: []string{"v1"},
							Resources:   []string{"pods"},
						},
					},
				},
			},
		},
	}

	currentConfiguration, mutatingWebhookErr := kubeClient.AdmissionregistrationV1beta1().MutatingWebhookConfigurations().Get(WebhookName, metav1.GetOptions{})
	if mutatingWebhookErr != nil && !apierrors.IsNotFound(mutatingWebhookErr) { // mutatingWebhookConfiguration 特殊错误返回
		return fmt.Errorf("Unexpected kubernetes client mutatingWebhookConfiguration error. %s", mutatingWebhookErr.Error())
	} else if mutatingWebhookErr != nil && apierrors.IsNotFound(mutatingWebhookErr) {
		_, err := kubeClient.AdmissionregistrationV1beta1().MutatingWebhookConfigurations().Create(configuration)
		if err != nil {
			return err
		}
	} else {
		configuration.ObjectMeta.ResourceVersion = currentConfiguration.ResourceVersion
		_, err := kubeClient.AdmissionregistrationV1beta1().MutatingWebhookConfigurations().Update(configuration)
		if err != nil {
			return err
		}
	}
	return nil
}

func IsClusterVersion1_10(kubeClient kubernetes.Interface) (bool, error) {
	return isClusterVersion(kubeClient, 1, 10)
}

func isClusterVersion(kubeClient kubernetes.Interface, majorV int, minorV int) (bool, error) {
	version, err := kubeClient.Discovery().ServerVersion()
	if err != nil {
		return false, err
	}
	valid := regexp.MustCompile("[0-9]")
	version.Minor = strings.Join(valid.FindAllString(version.Minor, -1), "")

	versionMajor, err := strconv.Atoi(version.Major)
	if err != nil {
		return false, err
	}

	versionMinor, err := strconv.Atoi(version.Minor)
	if err != nil {
		return false, err
	}
	if versionMajor == majorV && versionMinor == minorV {
		return true, nil
	}
	return false, nil
}

func StringPoint(s string) *string {
	return &s
}

func FailurePolicyPoint(scopeType admissionregistrationv1beta1.FailurePolicyType) *admissionregistrationv1beta1.FailurePolicyType {
	return &scopeType
}

type CAConfig struct {
	Signing *Signing `json:"signing"`
}

type Signing struct {
	Profiles map[string]*SigningProfile `json:"profiles"`
	Default  *SigningProfile            `json:"default"`
}

type SigningProfile struct {
	Usage        []string `json:"usages"`
	ExpiryString string   `json:"expiry"`
}
