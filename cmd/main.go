package main

import (
	"context"
	"flag"
	"os"

	"k8s.io/klog/v2"

	"github.com/yangsoon/ocm-register/pkg/common"
	"github.com/yangsoon/ocm-register/pkg/hub"
	"github.com/yangsoon/ocm-register/pkg/spoke"
)

func main() {
	var spokeKubeSecretName string
	var spokeKubeSecretNS string

	flag.StringVar(&spokeKubeSecretName, "name", "bootstrap-hub-kubeconfig", "secret name which store the spoke-cluster kubeconfig")
	flag.StringVar(&spokeKubeSecretNS, "namespace", "default", "secret namespce")
	flag.Parse()

	ctx := context.Background()

	hubCluster, err := hub.NewHubCluster(common.Scheme, nil)
	if err != nil {
		klog.ErrorS(err, "Fail to connect hub cluster")
		os.Exit(1)
	}

	spokeConfig, clusterName, err := hubCluster.GetSpokeClusterKubeConfig(ctx, "bootstrap-hub-kubeconfig", "default")
	if err != nil || spokeConfig == nil {
		klog.ErrorS(err, "Fail to get spoke cluster kubeconfig")
		os.Exit(1)
	}

	klog.Info("Prepare the User token for spoke-cluster")
	hubKubeConfig, err := hubCluster.GetHubClusterKubeConfig(ctx)
	if err != nil {
		klog.ErrorS(err, "Fail to get hub kubeconfig")
		os.Exit(1)
	}

	klog.InfoS("Prepare the Env for spoke-cluster....", "name", clusterName)

	spokeCluster, err := spoke.NewSpokeCluster(clusterName, common.Scheme, spokeConfig, hubKubeConfig)
	if err != nil {
		klog.ErrorS(err, "Fail to connect spoke cluster")
		os.Exit(1)
	}

	err = spokeCluster.InitSpokeClusterEnv(ctx)
	if err != nil {
		klog.Error(err, "Fail to init spoke env")
		os.Exit(1)
	}

	klog.Info("Waiting for spoke-cluster register request")
	ready, err := hubCluster.Wait4SpokeClusterReady(ctx, clusterName)
	if err != nil || !ready {
		klog.Error(err, "Fail to waiting for register request")
		os.Exit(1)
	}

	klog.Info("Approve spoke cluster csr")
	err = hubCluster.RegisterSpokeCluster(ctx, spokeCluster.Name)
	if err != nil {
		klog.Error(err, "Fail to approve spoke cluster")
		os.Exit(1)
	}
	os.Exit(0)
}
