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
	"github.com/threefoldtech/tfgrid-sdk-go/grid-cli/internal/config"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/deployer"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/graphql"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/workloads"
	"github.com/threefoldtech/zosbase/pkg/gridtypes/zos"
)

var repairGatewayCmd = &cobra.Command{
	Use:   "repair-gateway",
	Short: "Repair a broken gateway by canceling orphaned contracts and redeploying",
	Long: `Repair a gateway whose node lost its deployment data.
This command cancels orphaned contracts on the broken node and redeploys
the network and gateway workloads fresh, while leaving healthy nodes untouched.
Supports both FQDN and Name gateway types.`,
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

		t, err := deployer.NewTFPluginClient(mnemonics, deployer.WithNetwork(network), deployer.WithRMBTimeout(300))
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		// Phase 2: Contract Discovery — find gateway contracts
		contractFlag, _ := cmd.Flags().GetUint64("contract")

		contracts, err := t.ContractsGetter.ListContractsByTwinID([]string{"Created"})
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		if len(contracts.NodeContracts) == 0 {
			fmt.Println("No contracts found on this twin. Exiting.")
			os.Exit(0)
		}

		var gwContract graphql.Contract
		if contractFlag != 0 {
			contractStr := strconv.FormatUint(contractFlag, 10)
			found := false
			for _, c := range contracts.NodeContracts {
				if c.ContractID == contractStr {
					gwContract = c
					found = true
					break
				}
			}
			if !found {
				log.Fatal().Msgf("Contract %d not found among active contracts for this twin.", contractFlag)
			}
			fmt.Printf("Using contract %s on node %d\n", gwContract.ContractID, gwContract.NodeID)
		} else {
			fmt.Println("\nListing gateway contracts for twin...")

			gwContracts := []graphql.Contract{}
			for _, c := range contracts.NodeContracts {
				data, _ := workloads.ParseDeploymentData(c.DeploymentData)
				if data.Type == workloads.GatewayFQDNType || data.Type == workloads.GatewayNameType {
					gwContracts = append(gwContracts, c)
					fmt.Printf("Contract ID: %v  Node ID: %v  Type: %v  Data: %v\n",
						c.ContractID, c.NodeID, data.Type, c.DeploymentData)
				}
			}

			if len(gwContracts) == 0 {
				fmt.Println("No gateway contracts found. Exiting.")
				os.Exit(0)
			}

			fmt.Print("\nPlease enter the contract ID for the broken gateway: ")
			contractInput, err := scanner.ReadString('\n')
			if err != nil {
				log.Fatal().Err(err).Send()
			}
			contractInput = strings.TrimSpace(contractInput)

			found := false
			for _, c := range gwContracts {
				if c.ContractID == contractInput {
					gwContract = c
					found = true
					break
				}
			}

			if !found {
				log.Fatal().Msg("Invalid contract ID. Please select a contract ID from the list above.")
			}
		}

		// Phase 3: Identify broken node & related contracts
		brokenNodeID := gwContract.NodeID
		gwContractID, err := strconv.ParseUint(gwContract.ContractID, 10, 64)
		if err != nil {
			log.Fatal().Err(err).Send()
		}

		gwData, err := workloads.ParseDeploymentData(gwContract.DeploymentData)
		if err != nil {
			log.Fatal().Err(err).Msgf("Failed to parse gateway contract deployment data")
		}

		gatewayName := gwData.Name
		projectName := gwData.ProjectName
		gatewayType := gwData.Type

		if gatewayType != workloads.GatewayFQDNType && gatewayType != workloads.GatewayNameType {
			log.Fatal().Msgf("Unsupported gateway type: %s. Expected '%s' or '%s'.",
				gatewayType, workloads.GatewayFQDNType, workloads.GatewayNameType)
		}

		fmt.Printf("\nBroken gateway: name=%s type=%s project=%s node=%d\n", gatewayName, gatewayType, projectName, brokenNodeID)

		// Find the network contract on the broken node
		var networkContractID uint64
		var networkName string
		for _, c := range contracts.NodeContracts {
			if c.NodeID != brokenNodeID {
				continue
			}
			data, parseErr := workloads.ParseDeploymentData(c.DeploymentData)
			if parseErr != nil {
				continue
			}
			if data.Type == workloads.NetworkType {
				cID, parseErr := strconv.ParseUint(c.ContractID, 10, 64)
				if parseErr != nil {
					continue
				}
				networkContractID = cID
				networkName = data.Name
				break
			}
		}

		if networkContractID == 0 || networkName == "" {
			log.Fatal().Msg("Could not find a network contract on the broken node.")
		}

		fmt.Printf("Found network contract %d (network: %s) on broken node %d\n", networkContractID, networkName, brokenNodeID)

		// Find all network contracts across all nodes for this network name
		networkContractIDs, err := t.ContractsGetter.GetNodeContractsByTypeAndName(projectName, workloads.NetworkType, networkName)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to find network contracts")
		}

		fmt.Printf("Network '%s' spans %d node(s):", networkName, len(networkContractIDs))
		for node, cID := range networkContractIDs {
			marker := ""
			if node == brokenNodeID {
				marker = " (BROKEN)"
			}
			fmt.Printf("  node %d contract %d%s", node, cID, marker)
		}
		fmt.Println()

		// Phase 4: Load network from healthy nodes only
		// Register only healthy nodes' network contracts in State
		for node, cID := range networkContractIDs {
			if node == brokenNodeID {
				continue // skip broken node — contacting it would fail
			}
			t.State.CurrentNodeDeployments[node] = append(t.State.CurrentNodeDeployments[node], cID)
		}

		znet, err := t.State.LoadNetworkFromGrid(ctx, networkName)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to load network from healthy nodes")
		}

		fmt.Printf("Network loaded from %d healthy node(s). IP range: %s\n", len(znet.Nodes), znet.IPRange.String())

		// Phase 5: Auto-discover VM backend from healthy nodes
		fmt.Println("\nSearching for VMs on healthy network nodes...")
		var discoveredBackend string
		for node := range networkContractIDs {
			if node == brokenNodeID {
				continue
			}
			// Find VM contracts on this node
			for _, c := range contracts.NodeContracts {
				if c.NodeID != node {
					continue
				}
				data, parseErr := workloads.ParseDeploymentData(c.DeploymentData)
				if parseErr != nil || data.Type != workloads.VMType {
					continue
				}

				vmContractID, parseErr := strconv.ParseUint(c.ContractID, 10, 64)
				if parseErr != nil {
					continue
				}

				// Register this VM contract so we can load the deployment
				t.State.CurrentNodeDeployments[node] = append(t.State.CurrentNodeDeployments[node], vmContractID)

				deployment, loadErr := t.State.LoadDeploymentFromGrid(ctx, node, data.Name)
				if loadErr != nil {
					fmt.Printf("  Could not load deployment %s on node %d: %v\n", data.Name, node, loadErr)
					continue
				}

				for _, vm := range deployment.Vms {
					if vm.IP != "" {
						fmt.Printf("  Found VM '%s' on node %d with network IP: %s\n", vm.Name, node, vm.IP)
						if discoveredBackend == "" {
							discoveredBackend = fmt.Sprintf("http://%s:80", vm.IP)
						}
					}
				}
			}
		}

		// Phase 6: Collect gateway parameters
		fmt.Println("\n=== GATEWAY REPAIR PARAMETERS ===")
		fmt.Printf("Gateway name: %s\n", gatewayName)
		fmt.Printf("Gateway type: %s\n", gatewayType)
		fmt.Printf("Network: %s\n", networkName)
		fmt.Printf("Broken node: %d\n", brokenNodeID)

		// FQDN (only for FQDN type)
		var fqdn string
		if gatewayType == workloads.GatewayFQDNType {
			fmt.Print("\nEnter FQDN (e.g. cloud.example.com): ")
			fqdnInput, err := scanner.ReadString('\n')
			if err != nil {
				log.Fatal().Err(err).Send()
			}
			fqdn = strings.TrimSpace(fqdnInput)
			if fqdn == "" {
				log.Fatal().Msg("FQDN is required for FQDN gateway type.")
			}
		}

		// Backend URL
		if discoveredBackend != "" {
			fmt.Printf("Auto-detected backend: %s\n", discoveredBackend)
			fmt.Printf("Enter backend URL (or press Enter to use %s): ", discoveredBackend)
		} else {
			fmt.Print("Enter backend URL (e.g. http://10.20.2.2:80): ")
		}
		backendInput, err := scanner.ReadString('\n')
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		backendURL := strings.TrimSpace(backendInput)
		if backendURL == "" {
			if discoveredBackend != "" {
				backendURL = discoveredBackend
			} else {
				log.Fatal().Msg("Backend URL is required.")
			}
		}

		// TLS passthrough
		fmt.Print("Enable TLS passthrough? (yes/no) [no]: ")
		tlsInput, err := scanner.ReadString('\n')
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		tlsInput = strings.TrimSpace(strings.ToLower(tlsInput))
		tlsPassthrough := tlsInput == "yes" || tlsInput == "y"

		// Confirmation
		fmt.Println("\n=== REPAIR SUMMARY ===")
		fmt.Printf("Gateway: %s (%s)\n", gatewayName, gatewayType)
		if gatewayType == workloads.GatewayFQDNType {
			fmt.Printf("FQDN: %s\n", fqdn)
		}
		fmt.Printf("Backend: %s\n", backendURL)
		fmt.Printf("TLS Passthrough: %v\n", tlsPassthrough)
		fmt.Printf("Network: %s\n", networkName)
		fmt.Printf("Broken node: %d\n", brokenNodeID)
		fmt.Printf("Contracts to cancel: gateway=%d, network=%d\n", gwContractID, networkContractID)
		fmt.Println("\nThis will:")
		fmt.Println("  1. Cancel orphaned gateway and network contracts on the broken node")
		fmt.Println("  2. Redeploy the network (updating healthy nodes' peer lists)")
		fmt.Printf("  3. Deploy a fresh %s gateway on the broken node\n", gatewayType)

		fmt.Printf("\nTo confirm, type 'yes': ")
		confirmInput, err := scanner.ReadString('\n')
		if err != nil {
			log.Fatal().Err(err).Send()
		}
		if strings.TrimSpace(strings.ToLower(confirmInput)) != "yes" {
			fmt.Println("Aborted.")
			os.Exit(0)
		}

		// Phase 7: Cancel broken contracts
		fmt.Println("\nCanceling orphaned contracts on broken node...")
		err = t.BatchCancelContract([]uint64{gwContractID, networkContractID})
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to cancel contracts. You may need to cancel them manually.")
		}
		fmt.Printf("Canceled contracts: %d, %d\n", gwContractID, networkContractID)

		// Phase 8: Redeploy network
		fmt.Println("\nRedeploying network...")

		// Add the broken node back to the network's node list
		hasBrokenNode := false
		for _, n := range znet.Nodes {
			if n == brokenNodeID {
				hasBrokenNode = true
				break
			}
		}
		if !hasBrokenNode {
			znet.Nodes = append(znet.Nodes, brokenNodeID)
		}

		// Remove broken node's stale deployment ID (it was canceled)
		delete(znet.NodeDeploymentID, brokenNodeID)

		// Generate a new mycelium key for the broken node
		if znet.MyceliumKeys == nil {
			znet.MyceliumKeys = make(map[uint32][]byte)
		}
		myceliumKey, err := workloads.RandomMyceliumKey()
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to generate mycelium key")
		}
		znet.MyceliumKeys[brokenNodeID] = myceliumKey

		// Clear stale WG key/port for the broken node so they get regenerated
		delete(znet.Keys, brokenNodeID)
		delete(znet.WGPort, brokenNodeID)
		delete(znet.NodesIPRange, brokenNodeID)

		err = t.NetworkDeployer.Deploy(ctx, &znet)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to redeploy network")
		}

		fmt.Println("Network redeployed successfully.")
		fmt.Printf("Network node deployments: %v\n", znet.NodeDeploymentID)

		// Phase 9: Redeploy gateway
		backends := []zos.Backend{zos.Backend(backendURL)}

		if gatewayType == workloads.GatewayFQDNType {
			fmt.Println("\nDeploying FQDN gateway...")

			gw := workloads.GatewayFQDNProxy{
				NodeID:           brokenNodeID,
				Name:             gatewayName,
				FQDN:             fqdn,
				Backends:         backends,
				TLSPassthrough:   tlsPassthrough,
				Network:          networkName,
				SolutionType:     projectName,
				NodeDeploymentID: map[uint32]uint64{},
			}

			err = t.GatewayFQDNDeployer.Deploy(ctx, &gw)
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to deploy FQDN gateway")
			}

			// Phase 10: Verification
			fmt.Println("\n=== REPAIR COMPLETE ===")
			fmt.Printf("Gateway contract ID: %d\n", gw.ContractID)
			fmt.Printf("FQDN: %s\n", fqdn)
			fmt.Printf("Backend: %s\n", backendURL)
			fmt.Printf("Network: %s\n", networkName)
			fmt.Printf("Node: %d\n", brokenNodeID)

			fmt.Println("\nNetwork deployment IDs:")
			for node, cID := range znet.NodeDeploymentID {
				fmt.Printf("  Node %d: contract %d\n", node, cID)
			}

			gwInfo, _ := json.MarshalIndent(gw, "", "\t")
			fmt.Println("\nGateway details:\n" + string(gwInfo))

			fmt.Println("\nGateway repair completed successfully!")
			fmt.Println("Please verify that your DNS record for", fqdn, "points to the gateway node.")
		} else {
			fmt.Println("\nDeploying Name gateway...")

			gw := workloads.GatewayNameProxy{
				NodeID:           brokenNodeID,
				Name:             gatewayName,
				Backends:         backends,
				TLSPassthrough:   tlsPassthrough,
				Network:          networkName,
				SolutionType:     projectName,
				NodeDeploymentID: map[uint32]uint64{},
			}

			err = t.GatewayNameDeployer.Deploy(ctx, &gw)
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to deploy Name gateway")
			}

			// Phase 10: Verification
			fmt.Println("\n=== REPAIR COMPLETE ===")
			fmt.Printf("Gateway contract ID: %d\n", gw.ContractID)
			fmt.Printf("FQDN: %s\n", gw.FQDN)
			fmt.Printf("Backend: %s\n", backendURL)
			fmt.Printf("Network: %s\n", networkName)
			fmt.Printf("Node: %d\n", brokenNodeID)

			fmt.Println("\nNetwork deployment IDs:")
			for node, cID := range znet.NodeDeploymentID {
				fmt.Printf("  Node %d: contract %d\n", node, cID)
			}

			gwInfo, _ := json.MarshalIndent(gw, "", "\t")
			fmt.Println("\nGateway details:\n" + string(gwInfo))

			fmt.Println("\nGateway repair completed successfully!")
			fmt.Println("Please verify that your DNS record for", gw.FQDN, "points to the gateway node.")
		}
	},
}

func init() {
	rootCmd.AddCommand(repairGatewayCmd)
	repairGatewayCmd.Flags().Uint64("contract", 0,
		"Specify gateway contract ID directly, skipping interactive listing.")
}
