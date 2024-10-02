// Package integration for integration tests
package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/deployer"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/workloads"
)

func TestBatchVMDeployment(t *testing.T) {
	tfPluginClient, err := setup()
	if err != nil {
		t.Skipf("plugin creation failed: %v", err)
	}

	publicKey, privateKey, err := GenerateSSHKeyPair()
	require.NoError(t, err)

	nodes, err := deployer.FilterNodes(
		context.Background(),
		tfPluginClient,
		generateNodeFilter(),
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

	network1, err := generateBasicNetwork([]uint32{nodeID1})
	if err != nil {
		t.Skipf("network1 creation failed: %v", err)
	}
	network2, err := generateBasicNetwork([]uint32{nodeID2})
	if err != nil {
		t.Skipf("network2 creation failed: %v", err)
	}
	vm1, err := generateBasicVM("vm", nodeID1, network1.Name, publicKey)
	if err != nil {
		t.Skipf("vm1 creation failed: %v", err)
	}
	vm2, err := generateBasicVM("vm", nodeID2, network2.Name, publicKey)
	if err != nil {
		t.Skipf("vm2 creation failed: %v", err)
	}

	err = tfPluginClient.NetworkDeployer.BatchDeploy(context.Background(), []*workloads.ZNet{&network1, &network2})
	require.NoError(t, err)

	t.Cleanup(func() {
		err = tfPluginClient.NetworkDeployer.Cancel(context.Background(), &network1)
		require.NoError(t, err)

		err = tfPluginClient.NetworkDeployer.Cancel(context.Background(), &network2)
		require.NoError(t, err)
	})

	dl1 := workloads.NewDeployment(fmt.Sprintf("dl1_%s", generateRandString(10)), nodeID1, "", nil, network1.Name, nil, nil, []workloads.VM{vm1}, nil, nil)
	dl2 := workloads.NewDeployment(fmt.Sprintf("dl2_%s", generateRandString(10)), nodeID2, "", nil, network2.Name, nil, nil, []workloads.VM{vm2}, nil, nil)
	err = tfPluginClient.DeploymentDeployer.BatchDeploy(context.Background(), []*workloads.Deployment{&dl1, &dl2})
	require.NoError(t, err)

	t.Cleanup(func() {
		err = tfPluginClient.DeploymentDeployer.Cancel(context.Background(), &dl1)
		require.NoError(t, err)

		err = tfPluginClient.DeploymentDeployer.Cancel(context.Background(), &dl2)
		require.NoError(t, err)
	})

	v1, err := tfPluginClient.State.LoadVMFromGrid(context.Background(), nodeID1, vm1.Name, dl1.Name)
	require.NoError(t, err)
	require.NotEmpty(t, v1.PlanetaryIP)

	output, err := RemoteRun("root", v1.PlanetaryIP, "ls /", privateKey)
	require.NoError(t, err)
	require.Contains(t, output, "root")

	v2, err := tfPluginClient.State.LoadVMFromGrid(context.Background(), nodeID2, vm2.Name, dl2.Name)
	require.NoError(t, err)
	require.NotEmpty(t, v2.PlanetaryIP)

	output, err = RemoteRun("root", v2.PlanetaryIP, "ls /", privateKey)
	require.NoError(t, err)
	require.Contains(t, output, "root")
}
