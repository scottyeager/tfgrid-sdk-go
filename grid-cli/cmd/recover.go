// Package cmd for parsing command line arguments
package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	bip39 "github.com/cosmos/go-bip39"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	command "github.com/threefoldtech/tfgrid-sdk-go/grid-cli/internal/cmd"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-cli/internal/config"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/deployer"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/graphql"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/workloads"
)

var resetVMCmd = &cobra.Command{
	Use:   "reset",
	Short: "Reset a VM while preserving data disks",
	Long: `Reset a VM that uses ephemeral rootfs (not disk-based root).
This removes the existing VM but keeps data disks and volumes intact, then creates
a new VM with the same disks attached. Use this to reinstall the OS
without losing data stored on attached disks and volumes.`,
	Args: cobra.ExactArgs(0),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := cmd.Context()
		scanner := bufio.NewReader(os.Stdin)

		// Phase 1: Authentication & Setup
		mnemonics, network := "", ""
		cfg, err := config.GetUserConfig()
		if err != nil {
			fmt.Println("No config file found on disk. To store your seed phrase and preferred network, use the login command\n\nEnter your mnemonic seed phrase: ")

			mnemonics, err = scanner.ReadString('\n')
			if err != nil {
				log.Fatal().Err(err).Send()
			}
			mnemonics = strings.TrimSpace(mnemonics)
			if !bip39.IsMnemonicValid(mnemonics) {
				log.Fatal().Msg("failed to validate mnemonics")
			}

			fmt.Print("Please enter grid network (main,test): ")
			network, err = scanner.ReadString('\n')
			if err != nil {
				log.Fatal().Err(err).Send()
			}
			network = strings.TrimSpace(network)

			if network != "dev" && network != "qa" && network != "test" && network != "main" {
				log.Fatal().Msg("invalid grid network, must be one of: dev, test, qa and main")
			}
		} else {
			mnemonics, network = cfg.Mnemonics, cfg.Network
		}

		t, err := deployer.NewTFPluginClient(mnemonics, deployer.WithNetwork(network), deployer.WithRMBTimeout(100))
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		// Phase 2: Contract Selection
		fmt.Println("\nListing VM contracts for twin...")

		contracts, err := t.ContractsGetter.ListContractsByTwinID([]string{"Created"})
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		if len(contracts.NodeContracts) == 0 {
			fmt.Println("No contracts found on this twin. Exiting.")
			os.Exit(0)
		}

		vmContracts := []graphql.Contract{}
		for _, c := range contracts.NodeContracts {
			data, _ := workloads.ParseDeploymentData(c.DeploymentData)
			if data.Type == "vm" {
				vmContracts = append(vmContracts, c)
				fmt.Printf("Contract ID: %v Node ID: %v Deployment data: %v\n", c.ContractID, c.NodeID, c.DeploymentData)
			}
		}

		if len(vmContracts) == 0 {
			fmt.Println("No VM contracts found. Exiting.")
			os.Exit(0)
		}

		fmt.Print("\nPlease enter the contract ID for the VM you'd like to reset: ")

		contractInput, err := scanner.ReadString('\n')
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		contractInput = strings.TrimSpace(contractInput)

		var contract graphql.Contract
		found := false
		for _, c := range vmContracts {
			if c.ContractID == contractInput {
				contract = c
				found = true
				break
			}
		}

		if !found {
			log.Fatal().Msg("Invalid contract ID. Please select a contract ID from the list above.")
		}

		// Phase 3: Deployment Retrieval & Validation
		nodeID := contract.NodeID

		contractID, err := strconv.Atoi(contract.ContractID)
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		t.State.CurrentNodeDeployments[nodeID] = append(t.State.CurrentNodeDeployments[nodeID], uint64(contractID))

		data, err := workloads.ParseDeploymentData(contract.DeploymentData)
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		name := data.Name
		projectName := data.ProjectName

		fmt.Println("Retrieving deployment data...")

		_, zosDeployment, err := t.State.GetWorkloadInDeployment(ctx, nodeID, "", name)
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		deployment, err := workloads.NewDeploymentFromZosDeployment(zosDeployment, nodeID)
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		s, err := json.MarshalIndent(deployment, "", "\t")
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		fmt.Println("The retrieved deployment:\n" + string(s))

		// Critical validation: Must be exactly one VM
		if len(deployment.Vms) != 1 {
			log.Fatal().Msgf("Reset requires exactly one VM in the deployment. Found %d VMs.", len(deployment.Vms))
		}

		vm := deployment.Vms[0]

		// Critical validation: Must use ephemeral rootfs
		if vm.RootfsSizeMB == 0 {
			log.Fatal().Msg("This VM uses a disk as root filesystem. Reset only works for VMs with ephemeral rootfs (RootfsSizeMB > 0).")
		}

		// Identify mounted storage names (can be disks or volumes)
		mountedStorageNames := make(map[string]bool)
		for _, mount := range vm.Mounts {
			mountedStorageNames[mount.Name] = true
		}

		if len(mountedStorageNames) == 0 {
			log.Fatal().Msg("No mounted storage found. Reset is only useful when there are disks/volumes to preserve.")
		}

		// Collect data disks
		var dataDisks []workloads.Disk
		for _, disk := range deployment.Disks {
			if mountedStorageNames[disk.Name] {
				dataDisks = append(dataDisks, disk)
			}
		}

		// Collect data volumes
		var dataVolumes []workloads.Volume
		for _, volume := range deployment.Volumes {
			if mountedStorageNames[volume.Name] {
				dataVolumes = append(dataVolumes, volume)
			}
		}

		if len(dataDisks) == 0 && len(dataVolumes) == 0 {
			log.Fatal().Msg("No matching disks or volumes found. Cannot proceed with reset.")
		}

		// Phase 4: Network Loading
		if deployment.NetworkName == "" {
			for _, c := range contracts.NodeContracts {
				var netData map[string]string
				err = json.Unmarshal([]byte(c.DeploymentData), &netData)
				if err != nil {
					continue
				}

				if netData["type"] == "network" && c.NodeID == nodeID {
					deployment.NetworkName = netData["name"]
					break
				}
			}
		}

		networkContractIDs, err := t.ContractsGetter.GetNodeContractsByTypeAndName(projectName, workloads.NetworkType, deployment.NetworkName)
		if err != nil {
			// Try older naming scheme for compatibility
			networkContractIDs, err = t.ContractsGetter.GetNodeContractsByTypeAndName("FullVM", workloads.NetworkType, deployment.NetworkName)
			if err != nil {
				log.Fatal().Err(err).Send()
			}
		}

		for node, cID := range networkContractIDs {
			t.State.CurrentNodeDeployments[node] = append(t.State.CurrentNodeDeployments[node], cID)
		}

		_, err = t.State.LoadNetworkFromGrid(ctx, deployment.NetworkName)
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		// Phase 5: User Confirmation
		fmt.Println("\n=== RESET SUMMARY ===")
		fmt.Printf("VM Name: %s\n", vm.Name)
		fmt.Printf("Node ID: %d\n", deployment.NodeID)
		fmt.Printf("RootFS Size: %d MB (ephemeral)\n", vm.RootfsSizeMB)
		fmt.Printf("CPU: %d, Memory: %d MB\n", vm.CPU, vm.MemoryMB)
		fmt.Println("Storage to preserve:")
		for i, disk := range dataDisks {
			mountPoint := ""
			for _, m := range vm.Mounts {
				if m.Name == disk.Name {
					mountPoint = m.MountPoint
					break
				}
			}
			fmt.Printf("  %d. [Disk] %s (%d GB) -> %s\n", i+1, disk.Name, disk.SizeGB, mountPoint)
		}
		for i, volume := range dataVolumes {
			mountPoint := ""
			for _, m := range vm.Mounts {
				if m.Name == volume.Name {
					mountPoint = m.MountPoint
					break
				}
			}
			fmt.Printf("  %d. [Volume] %s (%d GB) -> %s\n", len(dataDisks)+i+1, volume.Name, volume.SizeGB, mountPoint)
		}

		fmt.Println("\nWARNING: This will DESTROY the current VM and create a new one.")
		fmt.Println("Storage will be preserved and reattached.")

		fmt.Printf("\nTo confirm, type the deployment name (%s): ", deployment.Name)

		nameInput, err := scanner.ReadString('\n')
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		nameInput = strings.TrimSpace(nameInput)

		if nameInput != deployment.Name {
			fmt.Println("\nInput does not match deployment name. Exiting.")
			os.Exit(0)
		}

		// Phase 6: Collect New VM Settings
		// SSH Key
		sshKey := vm.EnvVars["SSH_KEY"]
		truncatedKey := sshKey
		if len(truncatedKey) > 60 {
			truncatedKey = truncatedKey[:60] + "..."
		}
		fmt.Printf("\nCurrent SSH key: %s\n", truncatedKey)
		fmt.Print("Enter new SSH key (or press Enter to keep existing): ")

		newSSHKey, err := scanner.ReadString('\n')
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		newSSHKey = strings.TrimSpace(newSSHKey)

		if len(newSSHKey) > 0 {
			sshKey = newSSHKey
		}

		// VM Specs
		cpu := vm.CPU
		memoryMB := vm.MemoryMB
		rootfsSizeMB := vm.RootfsSizeMB

		fmt.Printf("\nCurrent specs - CPU: %d, Memory: %d MB, RootFS: %d MB\n", cpu, memoryMB, rootfsSizeMB)
		fmt.Print("Keep current specs? (yes/no) [yes]: ")

		keepSpecs, err := scanner.ReadString('\n')
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		keepSpecs = strings.TrimSpace(strings.ToLower(keepSpecs))

		if keepSpecs == "no" || keepSpecs == "n" {
			fmt.Printf("Enter CPU cores [%d]: ", cpu)
			cpuInput, _ := scanner.ReadString('\n')
			cpuInput = strings.TrimSpace(cpuInput)
			if cpuInput != "" {
				cpuVal, err := strconv.Atoi(cpuInput)
				if err != nil || cpuVal < 1 || cpuVal > 32 {
					log.Fatal().Msg("Invalid CPU value. Must be between 1 and 32.")
				}
				cpu = uint8(cpuVal)
			}

			fmt.Printf("Enter Memory in MB [%d]: ", memoryMB)
			memInput, _ := scanner.ReadString('\n')
			memInput = strings.TrimSpace(memInput)
			if memInput != "" {
				memVal, err := strconv.ParseUint(memInput, 10, 64)
				if err != nil || memVal < 250 {
					log.Fatal().Msg("Invalid memory value. Must be at least 250 MB.")
				}
				memoryMB = memVal
			}

			fmt.Printf("Enter RootFS size in MB [%d]: ", rootfsSizeMB)
			rootfsInput, _ := scanner.ReadString('\n')
			rootfsInput = strings.TrimSpace(rootfsInput)
			if rootfsInput != "" {
				rootfsVal, err := strconv.ParseUint(rootfsInput, 10, 64)
				if err != nil {
					log.Fatal().Msg("Invalid rootfs size value.")
				}
				rootfsSizeMB = rootfsVal
			}
		}

		// Flist/OS selection
		flist := vm.Flist
		fmt.Printf("\nCurrent flist: %s\n", flist)
		fmt.Println("Select OS option:")
		fmt.Println("  1. Keep current flist")
		fmt.Println("  2. Ubuntu 22.04")
		fmt.Println("  3. Ubuntu 24.04")
		fmt.Println("  4. Enter custom flist URL")
		fmt.Print("Choice [1]: ")

		flistChoice, err := scanner.ReadString('\n')
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		flistChoice = strings.TrimSpace(flistChoice)

		switch flistChoice {
		case "2":
			flist = "https://hub.grid.tf/tf-official-vms/ubuntu-22.04.flist"
		case "3":
			flist = "https://hub.grid.tf/tf-official-vms/ubuntu-24.04.flist"
		case "4":
			fmt.Print("Enter custom flist URL: ")
			customFlist, _ := scanner.ReadString('\n')
			customFlist = strings.TrimSpace(customFlist)
			if customFlist != "" {
				flist = customFlist
			}
		}

		// Network/IP settings
		publicIP := vm.PublicIP
		publicIP6 := vm.PublicIP6
		planetary := vm.Planetary

		fmt.Printf("\nCurrent IP settings - PublicIPv4: %v, PublicIPv6: %v, Planetary: %v\n", publicIP, publicIP6, planetary)
		fmt.Print("Keep current IP settings? (yes/no) [yes]: ")

		keepIP, err := scanner.ReadString('\n')
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		keepIP = strings.TrimSpace(strings.ToLower(keepIP))

		if keepIP == "no" || keepIP == "n" {
			fmt.Print("Enable Public IPv4? (yes/no) [no]: ")
			ipv4Input, _ := scanner.ReadString('\n')
			ipv4Input = strings.TrimSpace(strings.ToLower(ipv4Input))
			publicIP = ipv4Input == "yes" || ipv4Input == "y"

			fmt.Print("Enable Public IPv6? (yes/no) [no]: ")
			ipv6Input, _ := scanner.ReadString('\n')
			ipv6Input = strings.TrimSpace(strings.ToLower(ipv6Input))
			publicIP6 = ipv6Input == "yes" || ipv6Input == "y"

			fmt.Print("Enable Planetary network? (yes/no) [yes]: ")
			planetaryInput, _ := scanner.ReadString('\n')
			planetaryInput = strings.TrimSpace(strings.ToLower(planetaryInput))
			planetary = planetaryInput != "no" && planetaryInput != "n"
		}

		// Phase 7: Detach VM (First Deployment Update)
		// Save original mounts for later reattachment
		originalMounts := vm.Mounts

		// Remove VM, keep only data disks and volumes
		deployment.Vms = []workloads.VM{}
		deployment.Disks = dataDisks
		deployment.Volumes = dataVolumes

		fmt.Println("\nDetaching VM and preserving storage...")

		err = t.DeploymentDeployer.Deploy(ctx, &deployment)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to detach VM. The disks should still be safe.")
		}

		fmt.Println("VM detached successfully. Storage preserved.")

		// Check for --detach-only flag
		detachOnly, err := cmd.Flags().GetBool("detach-only")
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		if detachOnly {
			fmt.Println("\nFlag --detach-only set. Stopping after VM detachment.")
			fmt.Println("Storage is now in a floating state. Run reset again without --detach-only to create a new VM.")
			os.Exit(0)
		}

		// Phase 8: Create New VM (Second Deployment Update)
		// Rebuild mounts preserving original mount points
		var mounts []workloads.Mount
		for _, disk := range dataDisks {
			mountPoint := "/mnt/" + disk.Name // Default fallback
			for _, m := range originalMounts {
				if m.Name == disk.Name {
					mountPoint = m.MountPoint
					break
				}
			}
			mounts = append(mounts, workloads.Mount{
				Name:       disk.Name,
				MountPoint: mountPoint,
			})
		}
		for _, volume := range dataVolumes {
			mountPoint := "/mnt/" + volume.Name // Default fallback
			for _, m := range originalMounts {
				if m.Name == volume.Name {
					mountPoint = m.MountPoint
					break
				}
			}
			mounts = append(mounts, workloads.Mount{
				Name:       volume.Name,
				MountPoint: mountPoint,
			})
		}

		// Create new VM with "reset" suffix to prevent ZOS upgrade detection
		newVM := workloads.VM{
			Name:         vm.Name + "reset",
			NodeID:       deployment.NodeID,
			NetworkName:  deployment.NetworkName,
			Flist:        flist,
			CPU:          cpu,
			MemoryMB:     memoryMB,
			RootfsSizeMB: rootfsSizeMB,
			PublicIP:     publicIP,
			PublicIP6:    publicIP6,
			Planetary:    planetary,
			Mounts:       mounts,
			EnvVars:      map[string]string{"SSH_KEY": sshKey},
		}

		deployment.Vms = []workloads.VM{newVM}

		fmt.Println("Creating new VM with preserved disks...")

		err = t.DeploymentDeployer.Deploy(ctx, &deployment)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to create new VM. Your data disks are preserved. You can retry the reset command or manually deploy a new VM.")
		}

		// Phase 9: Verification
		fmt.Println("VM reset complete. Fetching new VM details...")

		resVM, err := command.GetVM(ctx, t, name)
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		s, err = json.MarshalIndent(resVM, "", "\t")
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		fmt.Println("\nNew VM details:\n" + string(s))

		fmt.Println("\n=== STORAGE MOUNT SUMMARY ===")
		for _, mount := range mounts {
			fmt.Printf("  %s -> %s\n", mount.Name, mount.MountPoint)
		}
		fmt.Println("\nReset completed successfully!")
	},
}

func init() {
	rootCmd.AddCommand(resetVMCmd)
	resetVMCmd.Flags().Bool("detach-only", false,
		"Stop after detaching VM, leaving disks floating. Use this for testing or manual recovery.")
}
