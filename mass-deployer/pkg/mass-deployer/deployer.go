package deployer

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/sethvargo/go-retry"

	"github.com/rs/zerolog/log"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/deployer"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/workloads"
	"github.com/threefoldtech/zos/pkg/gridtypes"
)

const (
	DefaultMaxRetries         = 5
	maxGoroutinesToFetchState = 100
)

func RunDeployer(ctx context.Context, cfg Config, output string, debug bool) error {
	passedGroups := map[string][]*workloads.Deployment{}
	failedGroups := map[string]string{}

	tfPluginClient, err := setup(cfg, debug)
	if err != nil {
		return fmt.Errorf("failed to create deployer: %v", err)
	}

	deploymentStart := time.Now()

	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = DefaultMaxRetries
	}

	for _, nodeGroup := range cfg.NodeGroups {
		log.Info().Str("Node group", nodeGroup.Name).Msg("Running deployment")
		var groupDeployments groupDeploymentsInfo
		trial := 1

		if err := retry.Do(ctx, retry.WithMaxRetries(cfg.MaxRetries, retry.NewConstant(1*time.Nanosecond)), func(ctx context.Context) error {
			if trial != 1 {
				log.Info().Str("Node group", nodeGroup.Name).Int("Deployment trial", trial).Msg("Retrying to deploy")
			}

			if err := deployNodeGroup(ctx, tfPluginClient, &groupDeployments, nodeGroup, cfg.Vms, cfg.SSHKeys); err != nil {
				trial++
				log.Debug().Err(err).Str("Node group", nodeGroup.Name).Msg("failed to deploy")
				return retry.RetryableError(err)
			}

			log.Info().Str("Node group", nodeGroup.Name).Msg("Done deploying")
			passedGroups[nodeGroup.Name] = groupDeployments.vmDeployments

			return nil
		}); err != nil {

			failedGroups[nodeGroup.Name] = err.Error()
			err := tfPluginClient.CancelByProjectName(nodeGroup.Name)
			if err != nil {
				log.Debug().Err(err).Send()
			}
		}
	}

	endTime := time.Since(deploymentStart)

	asJson := filepath.Ext(output) == ".json"
	var loadedGroups map[string][]vmOutput

	if len(passedGroups) > 0 {
		log.Info().Msg("Loading deployments")

		var failed map[string]string

		groupsDeploymentInfo := getDeploymentsInfoFromDeploymentsData(passedGroups)
		fmt.Printf("state: %+v\n", tfPluginClient.State)
		loadedGroups, failed = getNodeGroupsInfo(ctx, tfPluginClient, groupsDeploymentInfo, cfg.MaxRetries, asJson)

		for nodeGroup, err := range failed {
			failedGroups[nodeGroup] = err
		}
	}

	outputBytes, err := parseDeploymentOutput(loadedGroups, failedGroups, asJson)
	if err != nil {
		return err
	}

	fmt.Println(string(outputBytes))
	log.Info().Msgf("Deployment took %s", endTime)

	return os.WriteFile(output, outputBytes, 0644)
}

func deployNodeGroup(ctx context.Context, tfPluginClient deployer.TFPluginClient, groupDeployments *groupDeploymentsInfo, nodeGroup NodesGroup, vms []Vms, sshKeys map[string]string) error {
	log.Info().Str("Node group", nodeGroup.Name).Msg("Filter nodes")
	nodesIDs, err := filterNodes(ctx, tfPluginClient, nodeGroup)
	if err != nil {
		return err
	}
	log.Debug().Ints("nodes IDs", nodesIDs).Send()

	if groupDeployments.networkDeployments == nil {
		log.Debug().Str("Node group", nodeGroup.Name).Msg("Parsing vms group")
		*groupDeployments = parseVMsGroup(vms, nodeGroup.Name, nodesIDs, sshKeys)
	} else {
		log.Debug().Str("Node group", nodeGroup.Name).Msg("Updating vms group")
		updateFailedDeployments(ctx, tfPluginClient, nodesIDs, groupDeployments)
	}

	log.Info().Str("Node group", nodeGroup.Name).Msg("Starting mass deployment")
	return massDeploy(ctx, tfPluginClient, groupDeployments)
}

func parseVMsGroup(vms []Vms, nodeGroup string, nodesIDs []int, sshKeys map[string]string) groupDeploymentsInfo {
	vmsOfNodeGroup := []Vms{}
	for _, vm := range vms {
		if vm.NodeGroup == nodeGroup {
			vmsOfNodeGroup = append(vmsOfNodeGroup, vm)
		}
	}

	log.Debug().Str("Node group", nodeGroup).Msg("Build deployments")
	return buildDeployments(vmsOfNodeGroup, nodeGroup, nodesIDs, sshKeys)
}

func massDeploy(ctx context.Context, tfPluginClient deployer.TFPluginClient, deployments *groupDeploymentsInfo) error {
	// deploy only contracts that need to be deployed
	networks, vms := getNotDeployedDeployments(tfPluginClient, deployments)
	var multiErr error

	log.Debug().Msg(fmt.Sprintf("Deploying %d networks, this may to take a while", len(deployments.networkDeployments)))
	if err := tfPluginClient.NetworkDeployer.BatchDeploy(ctx, networks, false); err != nil {
		multiErr = multierror.Append(multiErr, err)
	}

	log.Debug().Msg(fmt.Sprintf("Deploying %d virtual machines, this may to take a while", len(deployments.vmDeployments)))
	if err := tfPluginClient.DeploymentDeployer.BatchDeploy(ctx, vms); err != nil {
		multiErr = multierror.Append(multiErr, err)
	}

	return multiErr
}

func buildDeployments(vms []Vms, nodeGroup string, nodesIDs []int, sshKeys map[string]string) groupDeploymentsInfo {
	var vmDeployments []*workloads.Deployment
	var networkDeployments []*workloads.ZNet
	var nodesIDsIdx int

	// here we loop over all groups of vms within the same node group, and for every group
	// we loop over all it's vms and create network and vm deployment for it
	// the nodesIDsIdx is a counter used to get nodeID to be able to distribute load over all nodes
	for _, vmGroup := range vms {
		envVars := vmGroup.EnvVars
		if envVars == nil {
			envVars = map[string]string{}
		}
		envVars["SSH_KEY"] = sshKeys[vmGroup.SSHKey]

		for i := 0; i < int(vmGroup.Count); i++ {
			nodeID := uint32(nodesIDs[nodesIDsIdx])
			nodesIDsIdx = (nodesIDsIdx + 1) % len(nodesIDs)

			vmName := fmt.Sprintf("%s%d", vmGroup.Name, i)
			disks, mounts := parseDisks(vmName, vmGroup.SSDDisks)

			network := workloads.ZNet{
				Name:        fmt.Sprintf("%s_network", vmName),
				Description: "network for mass deployment",
				Nodes:       []uint32{nodeID},
				IPRange: gridtypes.NewIPNet(net.IPNet{
					IP:   net.IPv4(10, 20, 0, 0),
					Mask: net.CIDRMask(16, 32),
				}),
				AddWGAccess:  false,
				SolutionType: nodeGroup,
			}

			if !vmGroup.PublicIP4 && !vmGroup.Planetary {
				log.Warn().Str("vms group", vmGroup.Name).Msg("Planetary and public IP options are false. Setting planetary IP to true")
				vmGroup.Planetary = true
			}

			vm := workloads.VM{
				Name:        vmName,
				NetworkName: network.Name,
				Flist:       vmGroup.Flist,
				CPU:         int(vmGroup.FreeCPU),
				Memory:      int(vmGroup.FreeMRU * 1024), // Memory is in MB
				PublicIP:    vmGroup.PublicIP4,
				PublicIP6:   vmGroup.PublicIP6,
				Planetary:   vmGroup.Planetary,
				RootfsSize:  int(vmGroup.RootSize * 1024), // RootSize is in MB
				Entrypoint:  vmGroup.Entrypoint,
				EnvVars:     envVars,
				Mounts:      mounts,
			}
			deployment := workloads.NewDeployment(vm.Name, nodeID, nodeGroup, nil, network.Name, disks, nil, []workloads.VM{vm}, nil)

			vmDeployments = append(vmDeployments, &deployment)
			networkDeployments = append(networkDeployments, &network)
		}
	}
	return groupDeploymentsInfo{vmDeployments: vmDeployments, networkDeployments: networkDeployments}
}

func getNotDeployedDeployments(tfPluginClient deployer.TFPluginClient, groupDeployments *groupDeploymentsInfo) ([]*workloads.ZNet, []*workloads.Deployment) {
	var failedVmDeployments []*workloads.Deployment
	var failedNetworkDeployments []*workloads.ZNet

	for i := range groupDeployments.networkDeployments {
		if len(groupDeployments.networkDeployments[i].NodeDeploymentID) == 0 {
			failedNetworkDeployments = append(failedNetworkDeployments, groupDeployments.networkDeployments[i])
		}

		if groupDeployments.vmDeployments[i].ContractID == 0 {
			failedVmDeployments = append(failedVmDeployments, groupDeployments.vmDeployments[i])
		}

	}

	return failedNetworkDeployments, failedVmDeployments
}

func parseDisks(name string, disks []Disk) (disksWorkloads []workloads.Disk, mountsWorkloads []workloads.Mount) {
	for i, disk := range disks {
		DiskWorkload := workloads.Disk{
			Name:   fmt.Sprintf("%s_disk%d", name, i),
			SizeGB: int(disk.Size),
		}

		disksWorkloads = append(disksWorkloads, DiskWorkload)
		mountsWorkloads = append(mountsWorkloads, workloads.Mount{DiskName: DiskWorkload.Name, MountPoint: disk.Mount})
	}
	return
}

func updateFailedDeployments(ctx context.Context, tfPluginClient deployer.TFPluginClient, nodesIDs []int, groupDeployments *groupDeploymentsInfo) {
	var contractsToBeCanceled []*workloads.ZNet
	for idx, network := range groupDeployments.networkDeployments {
		if groupDeployments.vmDeployments[idx].ContractID == 0 {
			contractsToBeCanceled = append(contractsToBeCanceled, network)
		}
	}

	err := tfPluginClient.NetworkDeployer.BatchCancel(ctx, contractsToBeCanceled)
	if err != nil {
		log.Debug().Err(err)
	}

	for idx, deployment := range groupDeployments.vmDeployments {
		if deployment.ContractID == 0 || len(groupDeployments.networkDeployments[idx].NodeDeploymentID) == 0 {
			nodeID := uint32(nodesIDs[idx%len(nodesIDs)])
			groupDeployments.vmDeployments[idx].NodeID = nodeID
			groupDeployments.networkDeployments[idx].Nodes = []uint32{nodeID}
		}
	}
}

func getDeploymentsInfoFromDeploymentsData(groupsInfo map[string][]*workloads.Deployment) map[string][]deploymentInfo {
	nodeGroupsDeploymentsInfo := make(map[string][]deploymentInfo)
	for nodeGroup, groupDeployments := range groupsInfo {
		deployments := []deploymentInfo{}
		for _, deployment := range groupDeployments {
			deployments = append(deployments, deploymentInfo{deployment.NodeID, deployment.Name})
		}
		nodeGroupsDeploymentsInfo[nodeGroup] = deployments
	}
	return nodeGroupsDeploymentsInfo
}
