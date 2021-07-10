package main

import (
	"context"
	"os"

	"k8s.io/klog/v2"

	"github.com/yangsoon/ocm-register/pkg/common"
	"github.com/yangsoon/ocm-register/pkg/hub"
	"github.com/yangsoon/ocm-register/pkg/spoke"
)

func main() {
	ctx := context.Background()

	hubCluster, err := hub.NewHubCluster(common.Scheme, nil)
	if err != nil {
		klog.ErrorS(err, "Fail to connect hub cluster")
		os.Exit(1)
	}
	spokeConfig, clusterName, err := hubCluster.GetSpokeClusterKubeConfig(ctx, "bootstrap-hub-kubeconfig", "default")
	if err != nil {
		klog.ErrorS(err, "Fail to get spoke cluster kubeconfig")
		os.Exit(1)
	}
	if spokeConfig == nil {
		klog.Info("Fail to gen spoke cluster rest config")
		os.Exit(1)
	}

	klog.Info("Prepare the User token for Cluster....")
	hubKubeConfig, err := hubCluster.GetHubClusterKubeConfig(ctx)
	if err != nil {
		klog.ErrorS(err, "Fail to get hub kubeconfig")
		os.Exit(1)
	}

	klog.Info("Prepare the Env for Cluster....")
	spokeCluster, err := spoke.NewSpokeCluster(clusterName,common.Scheme, spokeConfig, hubKubeConfig)
	err = spokeCluster.InitSpokeClusterEnv(ctx)
	if err != nil{
		klog.Error(err, "Fail to init spoke env")
		os.Exit(1)
	}
}