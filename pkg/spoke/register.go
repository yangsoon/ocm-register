package spoke

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"github.com/Masterminds/sprig"
	"github.com/ghodss/yaml"
	"github.com/yangsoon/ocm-register/pkg/common"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	clientcmdapiv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	"k8s.io/klog/v2"
	ocmapiv1 "open-cluster-management.io/api/operator/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
	"text/template"
)

type Cluster struct {
	Name string
	Args common.Args
	HubInfo
}

type HubInfo struct {
	KubeConfig *clientcmdapiv1.Config
	APIServer string
}

//go:embed resource
var f embed.FS

func getHubAPIServer(hubConfig *clientcmdapiv1.Config) (string, error) {
	clusters := hubConfig.Clusters
	if len(clusters) != 1 {
		return "", fmt.Errorf("can not find the cluster in the cluster-info")
	}
	cluster := clusters[0].Cluster
	return cluster.Server, nil
}

func NewSpokeCluster(name string, schema *runtime.Scheme, config *rest.Config, hubConfig *clientcmdapiv1.Config) (*Cluster, error) {
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

	apiserver, err := getHubAPIServer(hubConfig)
	if err != nil {
		return nil, err
	}
	return &Cluster{
		Name: name,
		Args:args,
		HubInfo: HubInfo{
			KubeConfig: hubConfig,
			APIServer: apiserver,
		},
	}, nil
}

func (c *Cluster) InitSpokeClusterEnv(ctx context.Context) error {
	files := []string{
		"resource/namespace_agent.yaml",
		"resource/namespace.yaml",
		"resource/cluster_role.yaml",
		"resource/cluster_role_binding.yaml",
		"resource/klusterlets.crd.yaml",
		"resource/service_account.yaml",
	}
	// 1. apply ns rbac crd
	err := common.ApplyK8sResource(ctx, f, c.Args.Client, files)
	if err != nil {
		return err
	}

	// 2. render secret contains hub kubeconfig
	hubConfigSecret := "resource/bootstrap_hub_kubeconfig.yaml"
	err = applyHubKubeConfig(ctx, c.Args.Client, hubConfigSecret, c.HubInfo.KubeConfig)
	if err != nil {
		return err
	}

	// 3. apply deployment
	opreatorFile := []string{"resource/operator.yaml"}
	err = common.ApplyK8sResource(ctx, f, c.Args.Client, opreatorFile)
	if err != nil {
		return err
	}

	// 4. apply klusterlet
	klusterFile := "resource/klusterlets.cr.yaml"
	err = applyKlusterlet(ctx, c.Args.Client, klusterFile, c)
	if err != nil {
		return err
	}
	return nil
}

func applyHubKubeConfig(ctx context.Context, client client.Client, file string, kubeConfig *clientcmdapiv1.Config) error {
	path := strings.Split(file, "/")
	templateName := path[len(path)-1]
	t, err := template.New(templateName).Funcs(sprig.TxtFuncMap()).ParseFS(f, file)
	if err != nil {
		klog.Error(err, "Fail to get Template from file", "name", file)
		return err
	}

	kubeConfigData, err := yaml.Marshal(kubeConfig)
	if err != nil {
		klog.Error(err)
	}

	var buf bytes.Buffer
	err = t.Execute(&buf, string(kubeConfigData))
	if err != nil {
		klog.Error(err)
		return err
	}

	kubeConfigSecret := new(corev1.Secret)
	err = yaml.Unmarshal(buf.Bytes(), kubeConfigSecret)
	if err != nil {
		klog.Error(err)
		return err
	}

	err = client.Create(ctx, kubeConfigSecret)
	if err != nil {
		if kerrors.IsAlreadyExists(err) {
			klog.InfoS("Resource already exists", "object", klog.KObj(kubeConfigSecret))
			return nil
		}
		klog.Error(err)
		return err
	}
	return nil
}

func applyKlusterlet(ctx context.Context, client client.Client, file string, cluster *Cluster) error {
	t, err := template.ParseFS(f, file)
	if err != nil {
		klog.Error(err, "Fail to get Template from file", "name", file)
		return err
	}

	var buf bytes.Buffer
	err = t.Execute(&buf, cluster)
	if err != nil {
		klog.Error(err, "Fail to render klusterlet")
		return err
	}

	klusterlet := new(ocmapiv1.Klusterlet)
	err = yaml.Unmarshal(buf.Bytes(), klusterlet)
	if err != nil {
		klog.Error(err, "Fail to Unmarshal Klusterlet")
		return err
	}

	err = client.Create(ctx, klusterlet)
	if err != nil {
		if kerrors.IsAlreadyExists(err) {
			klog.InfoS("Resource already exists", "object", klog.KObj(klusterlet))
			return nil
		}
		klog.Error(err, "Fail to create klusterlet")
		return err
	}
	return nil
}


