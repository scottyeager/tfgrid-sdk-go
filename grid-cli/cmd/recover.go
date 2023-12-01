// Package cmd for parsing command line arguments
package cmd

import (
	"encoding/json"
	"bufio"
	"os"
	"fmt"
	"strings"
	"strconv"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	command "github.com/threefoldtech/tfgrid-sdk-go/grid-cli/internal/cmd"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-cli/internal/config"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/deployer"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/workloads"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/graphql"
)

var recoverVMCmd = &cobra.Command{
	Use:   "recover",
	Short: "Recover a full VM",
	Args:  cobra.ExactArgs(0),
	Run: func(cmd *cobra.Command, args []string) {
		// Load existing mnemonics/network
		cfg, err := config.GetUserConfig()
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		// Set up a scanner to read input from user
		scanner := bufio.NewReader(os.Stdin)

		t, err := deployer.NewTFPluginClient(cfg.Mnemonics, "sr25519", cfg.Network, "", "", "", 30, false)
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		fmt.Println("\nListing contracts for twin...")

		contracts, err := t.ContractsGetter.ListContractsByTwinID([]string{"Created"})
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		if len(contracts.NodeContracts) == 0 {
			fmt.Print("No contracts found on this twin. Exiting.")
			os.Exit(0)
		}

		for _, c := range contracts.NodeContracts {
			var data map[string]string
			err = json.Unmarshal([]byte(c.DeploymentData), &data)
			if err != nil {
				log.Fatal().Err(err).Send()
			}
			if data["type"] == "vm" {
				fmt.Printf("Contract ID: %v Node ID: %v Deployment data: %v\n", c.ContractID, c.NodeID, c.DeploymentData)
			}
		}

		fmt.Print("\nPlease enter the contract ID for the VM you'd like to recover: ")

		contractinput, err := scanner.ReadString('\n')
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		contractinput = strings.TrimRight(contractinput, "\n")

		var contract graphql.Contract

		for _, c := range contracts.NodeContracts {
			if c.ContractID == contractinput {	
				contract = c
			}
		}

		nodeID := contract.NodeID

		contractID, err := strconv.Atoi(contract.ContractID)
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		// We gotta populate the state, otherwise can't retrieve deployment later
		t.State.CurrentNodeDeployments[nodeID] = append(t.State.CurrentNodeDeployments[nodeID], uint64(contractID))

		// Convert json string depoyment data into a map
		var deploymentdata map[string]string

		err = json.Unmarshal([]byte(contract.DeploymentData), &deploymentdata)
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		name := deploymentdata["name"]
		projectname := deploymentdata["projectName"]

		fmt.Println("Retrieving deployment data...")

		// Now we query the workload data from Zos and convert it into our local workload type
		_, zosdeployment, err := t.State.GetWorkloadInDeployment(nodeID, "", name)
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		deployment, err := workloads.NewDeploymentFromZosDeployment(zosdeployment, nodeID)
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		s, err := json.MarshalIndent(deployment, "", "\t")
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		fmt.Println("The retrieved deployment:\n" + string(s))

		fmt.Printf("Please review the information above and make sure this is the deployment you want to recover.\nTo continue, enter the name of the deployment (%v): ", deployment.Name)

		nameinput, err := scanner.ReadString('\n')
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		nameinput = strings.TrimRight(nameinput, "\n")

		if nameinput != deployment.Name {
			fmt.Print("\nInput does not match deployment name. Exiting.")
			os.Exit(0)
		}

		// Load the network into state. This is required to properly deploy later

		// In case we're just trying to recover disks with no VM (special case of failure during previous recovery process), we can't reference the network name. So instead just look for a network contract on the same node and use it

		if deployment.NetworkName == "" {
			for _, c := range contracts.NodeContracts {
				var data map[string]string
				err = json.Unmarshal([]byte(c.DeploymentData), &data)
				if err != nil {
					log.Fatal().Err(err).Send()
				}

				if data["type"] == "network" && c.NodeID == nodeID {
					deployment.NetworkName = data["name"]
				}
			}
		}

		networkContractIDs, err := t.ContractsGetter.GetNodeContractsByTypeAndName(projectname, workloads.NetworkType, deployment.NetworkName)
		if err != nil {
			log.Fatal().Err(err).Send()
		}
	
		for node, contractID := range networkContractIDs {
			t.State.CurrentNodeDeployments[node] = append(t.State.CurrentNodeDeployments[node], contractID)
		}

		_, err = t.State.LoadNetworkFromGrid(deployment.NetworkName)
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		// Now we perform the recovery. If there's a VM, detach it and reattach the disk(s) to a new VM. If no VM, just attach disks
		if len(deployment.Vms) == 1 {
			// Save the existing VM for reuse later
			vm := deployment.Vms[0]
			sshkey := vm.EnvVars["SSH_KEY"]
			fmt.Println("\nExisting ssh key: " + sshkey)

			fmt.Print("If you want to use a different public SSH key, enter it now, or leave empty to reuse the existing key: ")

			newsshkey, err := scanner.ReadString('\n')
			if err != nil {
				log.Fatal().Err(err).Send()
			}
			newsshkey = strings.TrimRight(newsshkey, "\n")

			if len(newsshkey) > 0 {
				vm.EnvVars["SSH_KEY"] = newsshkey
			}

			// First we want to detach the existing VM and leave its disk in a floating state
			deployment.Vms = make([]workloads.VM, 0)

			fmt.Println("Detaching disk(s) from existing VM...")

			err = t.DeploymentDeployer.Deploy(cmd.Context(), &deployment)
			if err != nil {
				log.Fatal().Err(err).Send()
			}

			fmt.Println("Finished with detachment.")

			detachonly, err := cmd.Flags().GetBool("detach")
			if err != nil {
				log.Fatal().Err(err).Send()
			} else if detachonly {
				fmt.Println("Flag --detach given, aborting after detachment")
				os.Exit(0)
			}

			// Now we make a new VM with the old VMs disks attached as mounts
			// First, make a new disk with the same size as old one, for rootfs
			olddisk := deployment.Disks[0]
			newdisk := workloads.Disk{Name: "disk" + strconv.Itoa(len(deployment.Disks) + 1), SizeGB: olddisk.SizeGB}

			vm.Mounts = []workloads.Mount{{DiskName: newdisk.Name, MountPoint: "/"}, {DiskName: olddisk.Name, MountPoint: "/mnt"}}

			for i, disk := range deployment.Disks {
				vm.Mounts = append(vm.Mounts, workloads.Mount{DiskName: disk.Name, MountPoint: "/mnt/" + strconv.Itoa(i)})
			}

			// Update the name so zos doesn't think this is an upgrade (?)
			vm.Name = vm.Name + "recoverered"
			deployment.Vms = []workloads.VM{vm}
			deployment.Disks = append(deployment.Disks, newdisk)

		// VM is already gone, but we still have a disk
		} else if len(deployment.Vms) == 0 && len(deployment.Disks) > 0 {
			fmt.Print("\nNo VM found on this deployment. Deploying a new VM with these disk(s) attached.\nPlease enter SSH key for new VM: ")

			newsshkey, err := scanner.ReadString('\n')
			if err != nil {
				log.Fatal().Err(err).Send()
			}
			newsshkey = strings.TrimRight(newsshkey, "\n")

			if len(newsshkey) == 0 {
				fmt.Print("\nSSH key empty. Exiting.")
				os.Exit(0)
			}

			newdisk := workloads.Disk{
				Name:   "disk" + strconv.Itoa(len(deployment.Disks) + 1),
				SizeGB: 15,
			}
			mounts := []workloads.Mount{{DiskName: newdisk.Name, MountPoint: "/"}}

			for i, disk := range deployment.Disks {
				mounts = append(mounts, workloads.Mount{DiskName: disk.Name, MountPoint: "/mnt/" + strconv.Itoa(i)})
			}

			// Since we lost the info about users previous VM, allow them to add public IPs. Wireguard access should still work
			fmt.Print("\nWould you like to add a public IP to this VM? Note that IPv4 will only work if the original VM had one reserved.\nWrite 4 for IPv4, 6 for IPv6, or leave empty for none: ")

			pubip, err := scanner.ReadString('\n')
			if err != nil {
				log.Fatal().Err(err).Send()
			}

			ipv4, ipv6  := false, false
			if strings.Contains(pubip, "4") {
				ipv4 = true
			} else if strings.Contains(pubip, "6") {
				ipv6 = true
			}

			vm := workloads.VM{
				Name:       "vm",
				Flist:      "https://hub.grid.tf/tf-official-vms/ubuntu-22.04.flist",
				CPU:        1,
				Planetary:  true,
				PublicIP:   ipv4,
				PublicIP6:  ipv6,
				Memory:     1024,
				EnvVars: map[string]string{
					"SSH_KEY": newsshkey,
				},
				Mounts: mounts,
				NetworkName: deployment.NetworkName,
			}
			deployment.Vms = []workloads.VM{vm}
			deployment.Disks = append(deployment.Disks, newdisk)
		} else {
			fmt.Println("No VMs or disks found in deployment. Nothing to recover. Exiting.")
			os.Exit(0)
		}

		fmt.Println("Deploying new VM...")
		err = t.DeploymentDeployer.Deploy(cmd.Context(), &deployment)
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		fmt.Println("Finished with redeployment. New VM info:")

		// Try to fetch the result
		resVM, err := command.GetVM(t, projectname, name)
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		s, err = json.MarshalIndent(resVM, "", "\t")
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		fmt.Println(string(s))
	},
}

func init() {
	rootCmd.AddCommand(recoverVMCmd)
	recoverVMCmd.Flags().Bool("detach", false, "Stop after detaching the disk(s). This can be used to simulate abrupt failure during recovery for testing purposes. Probably not useful otherwise.")
}
