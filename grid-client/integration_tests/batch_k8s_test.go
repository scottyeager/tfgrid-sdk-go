package integration

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/deployer"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/workloads"
)

func TestBatchK8sDeployment(t *testing.T) {
	tfPluginClient, err := setup()
	if err != nil {
		t.Skipf("plugin creation failed: %v", err)
	}

	publicKey, privateKey, err := GenerateSSHKeyPair()
	require.NoError(t, err)

	nodes, err := deployer.FilterNodes(
		context.Background(),
		tfPluginClient,
		generateNodeFilter(WithFreeMRU(2*minMemory)),
		nil,
		nil,
		nil,
		2,
	)
	if err != nil {
		t.Skipf("no available nodes found: %v", err)
	}

	nodeID1 := uint32(nodes[0].NodeID)
	nodeID2 := uint32(nodes[1].NodeID)

	network := generateBasicNetwork([]uint32{nodeID1, nodeID2})

	err = tfPluginClient.NetworkDeployer.Deploy(context.Background(), &network)
	require.NoError(t, err)

	t.Cleanup(func() {
		err = tfPluginClient.NetworkDeployer.Cancel(context.Background(), &network)
		require.NoError(t, err)
	})

	flist := "https://hub.grid.tf/tf-official-apps/threefoldtech-k3s-latest.flist"

	master1 := workloads.K8sNode{
		Name:      generateRandString(10),
		Node:      nodeID1,
		DiskSize:  1,
		Planetary: true,
		Flist:     flist,
		CPU:       minCPU,
		Memory:    int(minMemory) * 1024,
	}

	master2 := workloads.K8sNode{
		Name:      generateRandString(10),
		Node:      nodeID2,
		DiskSize:  1,
		Planetary: true,
		Flist:     flist,
		CPU:       minCPU,
		Memory:    int(minMemory) * 1024,
	}

	workerNodeData1 := workloads.K8sNode{
		Name:      generateRandString(10),
		Node:      nodeID1,
		DiskSize:  1,
		Planetary: true,
		Flist:     flist,
		CPU:       minCPU,
		Memory:    int(minMemory) * 1024,
	}

	workerNodeData2 := workloads.K8sNode{
		Name:      generateRandString(10),
		Node:      nodeID2,
		DiskSize:  1,
		Planetary: true,
		Flist:     flist,
		CPU:       minCPU,
		Memory:    int(minMemory) * 1024,
	}

	k8sCluster1 := workloads.K8sCluster{
		Master:      &master1,
		Workers:     []workloads.K8sNode{workerNodeData1},
		Token:       "tokens",
		SSHKey:      publicKey,
		NetworkName: network.Name,
	}

	k8sCluster2 := workloads.K8sCluster{
		Master:      &master2,
		Workers:     []workloads.K8sNode{workerNodeData2},
		Token:       "tokens",
		SSHKey:      publicKey,
		NetworkName: network.Name,
	}

	err = tfPluginClient.K8sDeployer.BatchDeploy(context.Background(), []*workloads.K8sCluster{&k8sCluster1, &k8sCluster2})
	require.NoError(t, err)

	t.Cleanup(func() {
		err = tfPluginClient.K8sDeployer.Cancel(context.Background(), &k8sCluster1)
		require.NoError(t, err)

		err = tfPluginClient.K8sDeployer.Cancel(context.Background(), &k8sCluster2)
		require.NoError(t, err)
	})

	// cluster 1
	k1, err := tfPluginClient.State.LoadK8sFromGrid(context.Background(), []uint32{nodeID1}, k8sCluster1.Master.Name)
	require.NoError(t, err)

	// check workers count
	require.Equal(t, len(k1.Workers), 1)

	// Check that master is reachable
	require.NotEmpty(t, k1.Master.PlanetaryIP)
	require.NotEmpty(t, k1.Master.IP)
	require.NotEqual(t, k1.Master.IP, k1.Workers[0].IP)

	require.True(t, CheckConnection(k1.Workers[0].PlanetaryIP, "22"))

	// ssh to master node
	require.NoError(t, requireNodesAreReady(len(k1.Workers)+1, k1.Master.PlanetaryIP, privateKey))

	// cluster 2
	k2, err := tfPluginClient.State.LoadK8sFromGrid(context.Background(), []uint32{nodeID2}, k8sCluster2.Master.Name)
	require.NoError(t, err)

	// check workers count
	require.Equal(t, len(k2.Workers), 1)

	// Check that master is reachable
	require.NotEmpty(t, k1.Master.PlanetaryIP)
	require.NotEmpty(t, k1.Master.IP)
	require.NotEqual(t, k1.Master.IP, k2.Workers[0].IP)

	require.True(t, CheckConnection(k2.Workers[0].PlanetaryIP, "22"))

	// ssh to master node
	require.NoError(t, requireNodesAreReady(len(k2.Workers)+1, k2.Master.PlanetaryIP, privateKey))

	// different ips generated
	require.Equal(t, len(slices.Compact[[]string, string]([]string{k1.Master.IP, k2.Master.IP, k1.Workers[0].IP, k2.Workers[0].IP})), 4)

}
