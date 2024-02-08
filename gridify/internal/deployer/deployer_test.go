package deployer

import (
	"context"
	"errors"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/graphql"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/workloads"
	"github.com/threefoldtech/tfgrid-sdk-go/gridify/internal/mocks"
)

const deploymentName = "test"
const repoURL = "https://github.com/threefoldtech/tfgrid-sdk-go/gridify.git"
const projectName = "gridify"

func TestDeploy(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	filter := buildNodeFilter(Eco)
	network := buildNetwork(projectName, deploymentName, 1)
	deployment := buildDeployment(Eco, network.Name, projectName, repoURL, deploymentName, 1)
	vmIP := "10.10.10.10/24"
	gateway1 := buildGateway("http://10.10.10.10:80", projectName, deploymentName, 1)
	gateway2 := buildGateway("http://10.10.10.10:8080", projectName, deploymentName, 1)

	clientMock := mocks.NewMockTFPluginClientInterface(ctrl)

	deployer, err := NewDeployer(clientMock, repoURL, log.Logger.Level(zerolog.Disabled))
	assert.NoError(t, err)

	t.Run("error listing contracts of a project", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(graphql.Contracts{}, errors.New("error"))

		_, err := deployer.Deploy(context.Background(), Eco, []uint{80}, deploymentName)
		assert.Error(t, err)
	})
	t.Run("deployment for same project already exists", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(graphql.Contracts{NameContracts: []graphql.Contract{{ContractID: "10"}}}, nil)

		_, err := deployer.Deploy(context.Background(), Eco, []uint{80}, deploymentName)
		assert.Error(t, err)
	})
	t.Run("error finding available nodes", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(graphql.Contracts{}, nil)

		clientMock.
			EXPECT().
			GetAvailableNode(gomock.Any(), filter, uint64(Eco.Storage)).
			Return(uint32(0), errors.New("error"))

		clientMock.
			EXPECT().
			GetGridNetwork().
			Return("dev")

		_, err := deployer.Deploy(context.Background(), Eco, []uint{80}, deploymentName)
		assert.Error(t, err)
	})
	t.Run("network deployment failed", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(graphql.Contracts{}, nil)

		clientMock.
			EXPECT().
			GetAvailableNode(gomock.Any(), filter, uint64(Eco.Storage)).
			Return(uint32(1), nil)

		clientMock.
			EXPECT().
			DeployNetwork(gomock.Any(), &network).
			Return(errors.New("error"))

		_, err := deployer.Deploy(context.Background(), Eco, []uint{80}, deploymentName)
		assert.Error(t, err)
	})
	t.Run("vm deployment failed", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(graphql.Contracts{}, nil)

		clientMock.
			EXPECT().
			GetAvailableNode(gomock.Any(), filter, uint64(Eco.Storage)).
			Return(uint32(1), nil)

		clientMock.
			EXPECT().
			DeployNetwork(gomock.Any(), &network).
			Return(nil)

		clientMock.
			EXPECT().
			DeployDeployment(gomock.Any(), &deployment).
			Return(errors.New("error"))

		_, err := deployer.Deploy(context.Background(), Eco, []uint{80}, deploymentName)
		assert.Error(t, err)
	})
	t.Run("loading vm failed", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(graphql.Contracts{}, nil)

		clientMock.
			EXPECT().
			GetAvailableNode(gomock.Any(), filter, uint64(Eco.Storage)).
			Return(uint32(1), nil)

		clientMock.
			EXPECT().
			DeployNetwork(gomock.Any(), &network).
			Return(nil)

		clientMock.
			EXPECT().
			DeployDeployment(gomock.Any(), &deployment).
			Return(nil)

		clientMock.
			EXPECT().
			LoadVMFromGrid(context.Background(), gomock.Any(), deployment.Name, deployment.Name).
			Return(workloads.VM{}, errors.New("error"))

		_, err := deployer.Deploy(context.Background(), Eco, []uint{80}, deploymentName)
		assert.Error(t, err)
	})
	t.Run("gateway deployment failed", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(graphql.Contracts{}, nil)

		clientMock.
			EXPECT().
			GetAvailableNode(gomock.Any(), filter, uint64(Eco.Storage)).
			Return(uint32(1), nil)

		clientMock.
			EXPECT().
			DeployNetwork(gomock.Any(), &network).
			Return(nil)

		clientMock.
			EXPECT().
			DeployDeployment(gomock.Any(), &deployment).
			Return(nil)

		clientMock.
			EXPECT().
			LoadVMFromGrid(context.Background(), gomock.Any(), deployment.Name, deployment.Name).
			Return(workloads.VM{ComputedIP: vmIP}, nil)

		clientMock.
			EXPECT().
			DeployGatewayName(gomock.Any(), &gateway1).
			Return(errors.New("error"))

		_, err := deployer.Deploy(context.Background(), Eco, []uint{80}, deploymentName)
		assert.Error(t, err)
	})
	t.Run("loading gateway failed", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(graphql.Contracts{}, nil)

		clientMock.
			EXPECT().
			GetAvailableNode(gomock.Any(), filter, uint64(Eco.Storage)).
			Return(uint32(1), nil)

		clientMock.
			EXPECT().
			DeployNetwork(gomock.Any(), &network).
			Return(nil)

		clientMock.
			EXPECT().
			DeployDeployment(gomock.Any(), &deployment).
			Return(nil)

		clientMock.
			EXPECT().
			LoadVMFromGrid(context.Background(), gomock.Any(), deployment.Name, deployment.Name).
			Return(workloads.VM{ComputedIP: vmIP}, nil)

		clientMock.
			EXPECT().
			DeployGatewayName(gomock.Any(), &gateway1).
			Return(nil)

		clientMock.
			EXPECT().
			LoadGatewayNameFromGrid(context.Background(), gomock.Any(), gateway1.Name, gateway1.Name).
			Return(workloads.GatewayNameProxy{}, errors.New("error"))

		_, err := deployer.Deploy(context.Background(), Eco, []uint{80}, deploymentName)
		assert.Error(t, err)
	})
	t.Run("deploying using one port", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(graphql.Contracts{}, nil)

		clientMock.
			EXPECT().
			GetAvailableNode(gomock.Any(), filter, uint64(Eco.Storage)).
			Return(uint32(1), nil)

		clientMock.
			EXPECT().
			DeployNetwork(gomock.Any(), &network).
			Return(nil)

		clientMock.
			EXPECT().
			DeployDeployment(gomock.Any(), &deployment).
			Return(nil)

		clientMock.
			EXPECT().
			LoadVMFromGrid(context.Background(), gomock.Any(), deployment.Name, deployment.Name).
			Return(workloads.VM{ComputedIP: vmIP}, nil)

		clientMock.
			EXPECT().
			DeployGatewayName(gomock.Any(), &gateway1).
			Return(nil)

		clientMock.
			EXPECT().
			LoadGatewayNameFromGrid(context.Background(), gomock.Any(), gateway1.Name, gateway1.Name).
			Return(workloads.GatewayNameProxy{FQDN: "domain1"}, nil)

		fqdns, err := deployer.Deploy(context.Background(), Eco, []uint{80}, deploymentName)
		assert.NoError(t, err)
		assert.Equal(t, fqdns, map[uint]string{80: "domain1"})
	})
	t.Run("deploying using multiple ports", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(graphql.Contracts{}, nil)

		clientMock.
			EXPECT().
			GetAvailableNode(gomock.Any(), filter, uint64(Eco.Storage)).
			Return(uint32(1), nil)

		clientMock.
			EXPECT().
			DeployNetwork(gomock.Any(), &network).
			Return(nil)

		clientMock.
			EXPECT().
			DeployDeployment(gomock.Any(), &deployment).
			Return(nil)

		clientMock.
			EXPECT().
			LoadVMFromGrid(context.Background(), gomock.Any(), deployment.Name, deployment.Name).
			Return(workloads.VM{ComputedIP: vmIP}, nil)

		clientMock.
			EXPECT().
			DeployGatewayName(gomock.Any(), &gateway1).
			Return(nil)

		clientMock.
			EXPECT().
			LoadGatewayNameFromGrid(context.Background(), gomock.Any(), gateway1.Name, gateway1.Name).
			Return(workloads.GatewayNameProxy{FQDN: "domain1"}, nil)

		clientMock.
			EXPECT().
			DeployGatewayName(gomock.Any(), &gateway2).
			Return(nil)

		clientMock.
			EXPECT().
			LoadGatewayNameFromGrid(context.Background(), gomock.Any(), gateway2.Name, gateway2.Name).
			Return(workloads.GatewayNameProxy{FQDN: "domain2"}, nil)

		fqdns, err := deployer.Deploy(context.Background(), Eco, []uint{80, 8080}, deploymentName)
		assert.NoError(t, err)

		assert.Equal(t, fqdns, map[uint]string{80: "domain1", 8080: "domain2"})
	})
}

func TestDestroy(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	clientMock := mocks.NewMockTFPluginClientInterface(ctrl)

	deployer, err := NewDeployer(clientMock, repoURL, log.Logger.Level(zerolog.Disabled))
	assert.NoError(t, err)

	t.Run("cancel contracts failed", func(t *testing.T) {
		clientMock.
			EXPECT().
			CancelByProjectName(projectName).
			Return(errors.New("error"))

		err = deployer.Destroy()
		assert.Error(t, err)
	})
	t.Run("cancel contracts succeeded", func(t *testing.T) {
		clientMock.
			EXPECT().
			CancelByProjectName(projectName).
			Return(nil)

		err = deployer.Destroy()
		assert.NoError(t, err)
	})
}

func TestGet(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	contracts := graphql.Contracts{
		NameContracts: []graphql.Contract{{ContractID: "10"}, {ContractID: "11"}},
		NodeContracts: []graphql.Contract{{ContractID: "20", NodeID: 14, DeploymentData: `{"name":"test", "type":"Gateway Name"}`}, {ContractID: "21", NodeID: 14, DeploymentData: "{}"}},
	}

	gatewayWorkload := workloads.GatewayNameProxy{
		Backends: workloads.NewZosBackends([]string{`http://10.10.10.10:8080`}),
		FQDN:     "http://example.com",
	}
	badBackendGateway := workloads.GatewayNameProxy{
		Backends: workloads.NewZosBackends([]string{`"http://example.com`}),
	}

	clientMock := mocks.NewMockTFPluginClientInterface(ctrl)
	clientMock.
		EXPECT().
		SetState(uint32(14), []uint64{20}).
		AnyTimes()

	deployer, err := NewDeployer(clientMock, repoURL, log.Logger.Level(zerolog.Disabled))
	assert.NoError(t, err)
	t.Run("loading contracts failed", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(graphql.Contracts{}, errors.New("error"))

		_, err := deployer.Get(context.Background())
		assert.Error(t, err)
	})
	t.Run("no contracts found", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(graphql.Contracts{}, nil)

		_, err := deployer.Get(context.Background())
		assert.NoError(t, err)
	})
	t.Run("parsing deployment data failed", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(graphql.Contracts{NodeContracts: []graphql.Contract{{ContractID: "1"}}}, nil)

		_, err := deployer.Get(context.Background())
		assert.Error(t, err)
	})
	t.Run("parsing backend failed", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(contracts, nil)

		clientMock.
			EXPECT().
			LoadGatewayNameFromGrid(context.Background(), uint32(14), "test", "test").
			Return(badBackendGateway, nil)

		_, err := deployer.Get(context.Background())
		assert.Error(t, err)
	})
	t.Run("fetching succeeded", func(t *testing.T) {
		clientMock.
			EXPECT().
			ListContractsOfProjectName(projectName).
			Return(contracts, nil)

		clientMock.
			EXPECT().
			LoadGatewayNameFromGrid(context.Background(), uint32(14), "test", "test").
			Return(gatewayWorkload, nil)

		fqdns, err := deployer.Get(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, fqdns, map[string]string{"8080": "http://example.com"})
	})
}
