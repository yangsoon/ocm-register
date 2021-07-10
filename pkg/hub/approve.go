package hub

import (
	"context"
	"embed"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"strings"
	"time"

	"github.com/ghodss/yaml"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clientcmdapiv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/yangsoon/ocm-register/pkg/common"
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

func (c *Cluster) GetSpokeClusterKubeConfig(ctx context.Context, name string, ns string) (*rest.Config, string, error){
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
	return spokeConfig, clusterName, nil
}

func (c *Cluster) GetHubClusterKubeConfig(ctx context.Context) (*clientcmdapiv1.Config, error) {
	configMap := new(corev1.ConfigMap)
	err := c.Client.Get(ctx, client.ObjectKey{Name: "cluster-info", Namespace: "kube-public"},configMap)
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

func (c *Cluster) ApproveSpokeClusterCSR(clusterName string) error {
	return nil
}