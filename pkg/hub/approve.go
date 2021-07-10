package hub

import (
	"context"
	"embed"
	"fmt"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/ghodss/yaml"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clientcmdapiv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	"k8s.io/klog/v2"
	ocmclusterv1 "open-cluster-management.io/api/cluster/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/yangsoon/ocm-register/pkg/common"
)

const (
	clusterLabel = "open-cluster-management.io/cluster-name"
)

//go:embed resource
var f embed.FS

type Cluster struct {
	common.Args
}

func NewHubCluster(schema *runtime.Scheme, config *rest.Config) (*Cluster, error) {
	args := common.Args{
		Schema: schema,
	}
	err := args.SetConfig(config)
	if err != nil {
		return nil, err
	}
	err = args.SetClient()
	if err != nil {
		return nil, err
	}
	return &Cluster{
		args,
	}, nil
}

func (c *Cluster) GetSpokeClusterKubeConfig(ctx context.Context, name string, ns string) (*rest.Config, string, error) {
	// name: bootstrap-hub-kubeconfig
	// ns: default

	var clusterName string
	secret := new(corev1.Secret)
	err := c.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, secret)
	if err != nil {
		return nil, clusterName, err
	}
	configData := secret.Data["kubeconfig"]
	spokeCmdV1Config := new(clientcmdapiv1.Config)
	err = yaml.Unmarshal(configData, spokeCmdV1Config)
	if err != nil {
		return nil, clusterName, nil
	}
	clusterName = spokeCmdV1Config.CurrentContext

	kubeConfigGetter := func() (*clientcmdapi.Config, error) {
		newData, err := yaml.Marshal(spokeCmdV1Config)
		if err != nil {
			return nil, err
		}
		spokeCmdConfig, err := clientcmd.Load(newData)
		if err != nil {
			return nil, err
		}
		return spokeCmdConfig, nil
	}

	spokeConfig, err := clientcmd.BuildConfigFromKubeconfigGetter("", kubeConfigGetter)
	if err != nil {
		return nil, clusterName, err
	}
	return spokeConfig, clusterName, nil
}

func (c *Cluster) GetHubClusterKubeConfig(ctx context.Context) (*clientcmdapiv1.Config, error) {
	configMap := new(corev1.ConfigMap)
	err := c.Client.Get(ctx, client.ObjectKey{Name: "cluster-info", Namespace: "kube-public"}, configMap)
	if err != nil {
		return nil, err
	}
	cofigMapData := configMap.Data["kubeconfig"]
	kubeConfig := new(clientcmdapiv1.Config)
	err = yaml.Unmarshal([]byte(cofigMapData), kubeConfig)
	if err != nil {
		return nil, err
	}
	token, err := c.GetHubUserToken(ctx)
	if err != nil {
		return nil, err
	}

	if len(kubeConfig.Clusters) != 1 {
		return nil, err
	}

	kubeConfig.Clusters[0].Name = common.HubClusterName
	kubeConfig.Contexts = []clientcmdapiv1.NamedContext{
		{
			Name: "bootstrap",
			Context: clientcmdapiv1.Context{
				Cluster:   "hub",
				AuthInfo:  "bootstrap",
				Namespace: "default",
			},
		},
	}
	kubeConfig.CurrentContext = "bootstrap"
	kubeConfig.AuthInfos = []clientcmdapiv1.NamedAuthInfo{
		{
			Name: "bootstrap",
			AuthInfo: clientcmdapiv1.AuthInfo{
				Token: token,
			},
		},
	}
	return kubeConfig, nil
}

func (c *Cluster) GetHubUserToken(ctx context.Context) (string, error) {
	var token string
	var secretName string
	files := []string{
		"resource/bootstrap_cluster_role.yaml",
		"resource/bootstrap_sa_cluster_role_binding.yaml",
		"resource/bootstrap_sa.yaml",
	}

	err := common.ApplyK8sResource(ctx, f, c.Client, files)
	if err != nil {
		return token, err
	}

	saKey := client.ObjectKey{Name: common.BootstrapSAName, Namespace: common.OpenClusterManagementNamespace}
	serviceAccount := new(corev1.ServiceAccount)
	secret := new(corev1.Secret)

	err = wait.PollImmediate(2*time.Second, 20*time.Second, func() (bool, error) {
		err = c.Client.Get(ctx, saKey, serviceAccount)
		if err != nil {
			klog.Info("get sa error", "err", err)
			return false, err
		}
		for _, objectRef := range serviceAccount.Secrets {
			if strings.HasPrefix(objectRef.Name, saKey.Name) {
				secretName = objectRef.Name
				return true, nil
			}
		}
		klog.InfoS("fail to find secret token", "len(Secrets)", len(serviceAccount.Secrets))
		return false, nil
	})

	if err != nil {
		klog.InfoS("Fail to get secret", err)
		return token, err
	}

	secretKey := client.ObjectKey{Name: secretName, Namespace: common.OpenClusterManagementNamespace}

	err = wait.PollImmediate(2*time.Second, 20*time.Second, func() (bool, error) {
		err = c.Client.Get(ctx, secretKey, secret)
		if err != nil {
			klog.Error(err)
			return false, err
		}
		if len(secret.Data["token"]) == 0 {
			return false, nil
		}
		token = string(secret.Data["token"])
		return true, nil
	})
	if err != nil {
		return token, err
	}
	klog.InfoS("secret", "type", secret.Type)
	return token, nil
}

func (c *Cluster) RegisterSpokeCluster(ctx context.Context, clusterName string) error {

	// 1. approve csr
	listOpts := []client.ListOption{
		client.MatchingLabels{
			clusterLabel: clusterName,
		},
	}
	csrList := new(certificatesv1.CertificateSigningRequestList)
	err := c.Client.List(ctx, csrList, listOpts...)
	if err != nil {
		klog.InfoS("Fail to get csr")
		return err
	}

	if len(csrList.Items) != 1 {
		return fmt.Errorf("csr number of csrList is wrong except: 1, actual: %d", len(csrList.Items))
	}

	csrName := csrList.Items[0].Name

	csr := new(certificatesv1.CertificateSigningRequest)
	err = c.Client.Get(ctx, client.ObjectKey{Name: csrName}, csr)
	if err != nil {
		return err
	}

	approved, denied := checkCsrStatus(&csr.Status)
	if denied {
		fmt.Printf("CSR %s already denied\n", csr.Name)
		return nil
	}
	//if alreaady approved, then nothing to do
	if approved {
		fmt.Printf("CSR %s already approved\n", csr.Name)
		return nil
	}

	if csr.Status.Conditions == nil {
		csr.Status.Conditions = make([]certificatesv1.CertificateSigningRequestCondition, 0)
	}

	csr.Status.Conditions = append(csr.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Status:         corev1.ConditionTrue,
		Type:           certificatesv1.CertificateApproved,
		Reason:         fmt.Sprintf("%sApprove", "ocm-register"),
		Message:        fmt.Sprintf("This CSR was approved by %s certificate approve.", "ocm-register"),
		LastUpdateTime: metav1.Now(),
	})

	clientset, err := kubernetes.NewForConfig(c.KubeConfig)
	if err != nil {
		klog.Error(err)
		return err
	}

	signingRequest := clientset.CertificatesV1().CertificateSigningRequests()
	if _, err = signingRequest.UpdateApproval(ctx, csr.Name, csr, metav1.UpdateOptions{}); err != nil {
		return err
	}

	// 2. update managed cluster
	mc := new(ocmclusterv1.ManagedCluster)
	err = c.Client.Get(ctx, client.ObjectKey{Name: clusterName}, mc)
	if err != nil {
		klog.InfoS("Fail to get managedCluster", "obj", klog.KObj(mc))
		return err
	}

	if !mc.Spec.HubAcceptsClient {
		mc.Spec.HubAcceptsClient = true
		return c.Client.Update(ctx, mc)
	}
	return nil
}

func (c *Cluster) Wait4SpokeClusterReady(ctx context.Context, clusterName string) (bool, error) {
	listOpts := []client.ListOption{
		client.MatchingLabels{
			clusterLabel: clusterName,
		},
	}
	csrList := new(certificatesv1.CertificateSigningRequestList)
	mc := new(ocmclusterv1.ManagedCluster)

	err := wait.PollImmediate(30*time.Second, 10*time.Minute, func() (done bool, err error) {
		klog.Info(time.Now())
		err = c.Client.List(ctx, csrList, listOpts...)
		if err != nil {
			klog.InfoS("Fail to get csr")
			return false, err
		}
		if len(csrList.Items) < 1 {
			return false, nil
		}

		err = c.Client.Get(ctx, client.ObjectKey{Name: clusterName}, mc)
		if err != nil {
			return false, nil
		}
		return true, nil
	})

	if err != nil {
		return false, err
	}
	return true, nil
}

func checkCsrStatus(status *certificatesv1.CertificateSigningRequestStatus) (approved bool, denied bool) {
	for _, c := range status.Conditions {
		if c.Type == certificatesv1.CertificateApproved {
			approved = true
		}
		if c.Type == certificatesv1.CertificateDenied {
			denied = true
		}
	}
	return
}
