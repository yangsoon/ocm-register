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

	// 1. connect to hub-cluster, which job(ocm-register-assistant) was deployed to
	hubCluster, err := hub.NewHubCluster(common.Scheme, nil)
	if err != nil {
		klog.InfoS("Fail to create client connect to hub cluster")
		os.Exit(1)
	}

	// 2. get spoke-cluster's kubeconfig from Secret
	spokeConfig, clusterName, err := hubCluster.GetSpokeClusterKubeConfig(ctx, spokeKubeSecretName, spokeKubeSecretNS)
	if err != nil || spokeConfig == nil {
		klog.InfoS("Fail to get spoke-cluster kubeconfig", "err", err)
		os.Exit(1)
	}

	klog.Info("generate the token for spoke-cluster to connect hub-cluster")
	hubKubeConfig, err := hubCluster.GenerateHubClusterKubeConfig(ctx)
	if err != nil {
		klog.InfoS("Fail to generate the token for spoke-cluster", "err", err)
		os.Exit(1)
	}

	// 3. connect to spoke-cluster
	spokeCluster, err := spoke.NewSpokeCluster(clusterName, common.Scheme, spokeConfig, hubKubeConfig)
	if err != nil {
		klog.InfoS("Fail to connect spoke cluster", "err", err)
		os.Exit(1)
	}

	klog.InfoS("prepare the env for spoke-cluster", "name", clusterName)
	err = spokeCluster.InitSpokeClusterEnv(ctx)
	if err != nil {
		klog.InfoS("Fail to prepare the env for spoke-cluster", "err", err)
		os.Exit(1)
	}

	klog.Info("wait for spoke-cluster register request")
	ready, err := hubCluster.Wait4SpokeClusterReady(ctx, clusterName)
	if err != nil || !ready {
		klog.Error(err, "Fail to waiting for register request")
		os.Exit(1)
	}

	klog.Info("approve spoke cluster csr")
	err = hubCluster.RegisterSpokeCluster(ctx, spokeCluster.Name)
	if err != nil {
		klog.Error(err, "Fail to approve spoke cluster")
		os.Exit(1)
	}
	klog.InfoS("successfully register cluster", "name", clusterName)

	os.Exit(0)
}
