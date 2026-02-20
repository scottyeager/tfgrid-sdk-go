// Package cmd for parsing command line arguments
package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
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

// Terraform state JSON types for `terraform show -json` output
type tfShowOutput struct {
	Values *tfStateValues `json:"values"`
}

type tfStateValues struct {
	RootModule tfModule `json:"root_module"`
}

type tfModule struct {
	Resources    []tfResource `json:"resources"`
	ChildModules []tfModule   `json:"child_modules"`
}

type tfResource struct {
	Address string                 `json:"address"`
	Type    string                 `json:"type"`
	Name    string                 `json:"name"`
	Values  map[string]interface{} `json:"values"`
}

type tfGatewayInfo struct {
	Address        string
	ContractID     uint64
	NodeID         uint32
	Name           string
	GatewayType    string // workloads.GatewayFQDNType or workloads.GatewayNameType
	FQDN           string
	Backends       []string
	TLSPassthrough bool
	Network        string
	SolutionType   string
	NameContractID uint64
}

type tfNetworkInfo struct {
	Address          string
	Name             string
	Nodes            []uint32
	NodeDeploymentID map[uint32]uint64
}

// collectTFResources recursively collects all resources from terraform modules
func collectTFResources(mod tfModule) []tfResource {
	resources := make([]tfResource, 0, len(mod.Resources))
	resources = append(resources, mod.Resources...)
	for _, child := range mod.ChildModules {
		resources = append(resources, collectTFResources(child)...)
	}
	return resources
}

// tryLoadTerraformState attempts to run `terraform show -json` and extract gateway/network info
func tryLoadTerraformState() ([]tfGatewayInfo, []tfNetworkInfo, error) {
	out, err := exec.Command("terraform", "show", "-json").Output()
	if err != nil {
		return nil, nil, err
	}

	var state tfShowOutput
	if err := json.Unmarshal(out, &state); err != nil {
		return nil, nil, err
	}

	if state.Values == nil {
		return nil, nil, fmt.Errorf("no state values")
	}

	resources := collectTFResources(state.Values.RootModule)

	var gateways []tfGatewayInfo
	var networks []tfNetworkInfo

	for _, r := range resources {
		switch r.Type {
		case "grid_fqdn_proxy":
			if gw := extractTFGateway(r, workloads.GatewayFQDNType); gw != nil {
				gateways = append(gateways, *gw)
			}
		case "grid_name_proxy":
			if gw := extractTFGateway(r, workloads.GatewayNameType); gw != nil {
				gateways = append(gateways, *gw)
			}
		case "grid_network":
			if net := extractTFNetwork(r); net != nil {
				networks = append(networks, *net)
			}
		}
	}

	return gateways, networks, nil
}

func extractTFGateway(r tfResource, gwType string) *tfGatewayInfo {
	v := r.Values

	idStr, _ := v["id"].(string)
	contractID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || contractID == 0 {
		return nil
	}

	nodeFloat, _ := v["node"].(float64)
	name, _ := v["name"].(string)
	fqdn, _ := v["fqdn"].(string)
	tlsPassthrough, _ := v["tls_passthrough"].(bool)
	network, _ := v["network"].(string)
	solutionType, _ := v["solution_type"].(string)
	nameContractIDFloat, _ := v["name_contract_id"].(float64)

	var backends []string
	if bks, ok := v["backends"].([]interface{}); ok {
		for _, b := range bks {
			if s, ok := b.(string); ok {
				backends = append(backends, s)
			}
		}
	}

	return &tfGatewayInfo{
		Address:        r.Address,
		ContractID:     contractID,
		NodeID:         uint32(nodeFloat),
		Name:           name,
		GatewayType:    gwType,
		FQDN:           fqdn,
		Backends:       backends,
		TLSPassthrough: tlsPassthrough,
		Network:        network,
		SolutionType:   solutionType,
		NameContractID: uint64(nameContractIDFloat),
	}
}

func extractTFNetwork(r tfResource) *tfNetworkInfo {
	v := r.Values

	name, _ := v["name"].(string)

	var nodes []uint32
	if nodeList, ok := v["nodes"].([]interface{}); ok {
		for _, n := range nodeList {
			if nf, ok := n.(float64); ok {
				nodes = append(nodes, uint32(nf))
			}
		}
	}

	nodeDeploymentID := make(map[uint32]uint64)
	if ndid, ok := v["node_deployment_id"].(map[string]interface{}); ok {
		for nodeStr, cidVal := range ndid {
			nodeID, err := strconv.ParseUint(nodeStr, 10, 32)
			if err != nil {
				continue
			}
			var cid uint64
			switch c := cidVal.(type) {
			case float64:
				cid = uint64(c)
			case string:
				cid, _ = strconv.ParseUint(c, 10, 64)
			}
			if cid > 0 {
				nodeDeploymentID[uint32(nodeID)] = cid
			}
		}
	}

	return &tfNetworkInfo{
		Address:          r.Address,
		Name:             name,
		Nodes:            nodes,
		NodeDeploymentID: nodeDeploymentID,
	}
}

var repairGatewayCmd = &cobra.Command{
	Use:   "repair-gateway",
	Short: "Repair a broken gateway by canceling orphaned contracts and redeploying",
	Long: `Repair a gateway whose node lost its deployment data.
This command cancels orphaned contracts on the broken node and redeploys
the network and gateway workloads fresh, while leaving healthy nodes untouched.
Supports both FQDN and Name gateway types.

If run from a directory with Terraform state, gateway and network parameters
are auto-detected from the state, skipping most interactive prompts.`,
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

		contractFlag, _ := cmd.Flags().GetUint64("contract")
		autoApprove, _ := cmd.Flags().GetBool("yes")
		wgConfigOnly, _ := cmd.Flags().GetBool("wg-config")

		// These are the key variables we need to determine (from terraform or interactively)
		var (
			brokenNodeID      uint32
			gwContractID      uint64
			gatewayName       string
			projectName       string
			gatewayType       string
			networkContractID uint64
			nameContractID    uint64
			networkName       string
			networkContractIDs map[uint32]uint64 // node → contract for the network across all nodes

			// Pre-populated from terraform state (empty if not available)
			tfFQDN     string
			tfBackends []string
			tfTLS      bool
			fromTF     bool
		)

		// Try loading Terraform state
		tfGateways, tfNetworks, tfErr := tryLoadTerraformState()

		if tfErr == nil && len(tfGateways) > 0 {
			fmt.Printf("\nTerraform state detected with %d gateway(s):\n", len(tfGateways))
			for i, gw := range tfGateways {
				fmt.Printf("  %d. %s  type=%s  node=%d  contract=%d\n",
					i+1, gw.Address, gw.GatewayType, gw.NodeID, gw.ContractID)
			}

			var selectedGW *tfGatewayInfo

			if contractFlag != 0 {
				// --contract flag takes precedence — find matching terraform resource
				for i := range tfGateways {
					if tfGateways[i].ContractID == contractFlag {
						selectedGW = &tfGateways[i]
						break
					}
				}
				if selectedGW == nil {
					fmt.Printf("Contract %d not found in Terraform state, falling back to chain query.\n", contractFlag)
				}
			} else if len(tfGateways) == 1 {
				selectedGW = &tfGateways[0]
				contractFlag = selectedGW.ContractID
				fmt.Printf("\nAuto-selected: %s (contract %d)\n", selectedGW.Address, selectedGW.ContractID)
			} else {
				fmt.Print("\nSelect gateway to repair (number): ")
				input, _ := scanner.ReadString('\n')
				input = strings.TrimSpace(input)
				idx, parseErr := strconv.Atoi(input)
				if parseErr == nil && idx >= 1 && idx <= len(tfGateways) {
					selectedGW = &tfGateways[idx-1]
					contractFlag = selectedGW.ContractID
				} else {
					fmt.Println("Invalid selection, falling back to chain query.")
				}
			}

			if selectedGW != nil {
				fromTF = true
				brokenNodeID = selectedGW.NodeID
				gwContractID = selectedGW.ContractID
				gatewayName = selectedGW.Name
				projectName = selectedGW.SolutionType
				gatewayType = selectedGW.GatewayType
				nameContractID = selectedGW.NameContractID
				tfFQDN = selectedGW.FQDN
				tfBackends = selectedGW.Backends
				tfTLS = selectedGW.TLSPassthrough
				networkName = selectedGW.Network

				fmt.Printf("\nFrom Terraform state:\n")
				fmt.Printf("  Gateway: %s (%s) on node %d\n", gatewayName, gatewayType, brokenNodeID)
				if tfFQDN != "" {
					fmt.Printf("  FQDN: %s\n", tfFQDN)
				}
				if len(tfBackends) > 0 {
					fmt.Printf("  Backends: %v\n", tfBackends)
				}
				fmt.Printf("  Network: %s\n", networkName)

				// Find the matching network resource to get node_deployment_id
				for _, net := range tfNetworks {
					if net.Name == networkName {
						networkContractIDs = net.NodeDeploymentID

						// The broken node's network contract
						if cid, ok := net.NodeDeploymentID[brokenNodeID]; ok {
							networkContractID = cid
						}

						fmt.Printf("\nFrom Terraform network '%s' (%s):\n", net.Name, net.Address)
						for node, cid := range net.NodeDeploymentID {
							marker := ""
							if node == brokenNodeID {
								marker = " (BROKEN)"
							}
							fmt.Printf("  Node %d: contract %d%s\n", node, cid, marker)
						}
						break
					}
				}

				if networkContractID == 0 {
					fmt.Println("Warning: could not find network contract for broken node in Terraform state.")
					fmt.Println("Falling back to chain query for network info.")
					fromTF = false
				}
			}
		}

		// Fallback: interactive discovery from chain if terraform state didn't provide everything
		if !fromTF {
			contracts, err := t.ContractsGetter.ListContractsByTwinID([]string{"Created"})
			if err != nil {
				log.Fatal().Err(err).Send()
			}

			if len(contracts.NodeContracts) == 0 {
				fmt.Println("No contracts found on this twin. Exiting.")
				os.Exit(0)
			}

			// Phase 2: Contract Discovery
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
			brokenNodeID = gwContract.NodeID
			gwContractID, err = strconv.ParseUint(gwContract.ContractID, 10, 64)
			if err != nil {
				log.Fatal().Err(err).Send()
			}

			gwData, err := workloads.ParseDeploymentData(gwContract.DeploymentData)
			if err != nil {
				log.Fatal().Err(err).Msgf("Failed to parse gateway contract deployment data")
			}

			gatewayName = gwData.Name
			projectName = gwData.ProjectName
			gatewayType = gwData.Type

			fmt.Printf("\nBroken gateway: name=%s type=%s project=%s node=%d\n",
				gatewayName, gatewayType, projectName, brokenNodeID)

			// Find the name contract for GatewayNameProxy gateways
			if gatewayType == workloads.GatewayNameType {
				for _, nc := range contracts.NameContracts {
					if nc.Name == gatewayName {
						nameContractID, _ = strconv.ParseUint(nc.ContractID, 10, 64)
						break
					}
				}
			}

			// Find the network contract on the broken node
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

			fmt.Printf("Found network contract %d (network: %s) on broken node %d\n",
				networkContractID, networkName, brokenNodeID)

			// Find all network contracts across all nodes using already-loaded contracts
			// (avoids querying the broken node, which would fail with "deployment not found")
			networkContractIDs = make(map[uint32]uint64)
			for _, c := range contracts.NodeContracts {
				data, parseErr := workloads.ParseDeploymentData(c.DeploymentData)
				if parseErr != nil {
					continue
				}
				if data.Type == workloads.NetworkType && data.Name == networkName {
					cID, parseErr := strconv.ParseUint(c.ContractID, 10, 64)
					if parseErr != nil {
						continue
					}
					networkContractIDs[c.NodeID] = cID
				}
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

			// Auto-discover VM backend from healthy nodes
			fmt.Println("\nSearching for VMs on healthy network nodes...")
			for node := range networkContractIDs {
				if node == brokenNodeID {
					continue
				}
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

					t.State.CurrentNodeDeployments[node] = append(
						t.State.CurrentNodeDeployments[node], vmContractID)

					deployment, loadErr := t.State.LoadDeploymentFromGrid(ctx, node, data.Name)
					if loadErr != nil {
						fmt.Printf("  Could not load deployment %s on node %d: %v\n",
							data.Name, node, loadErr)
						continue
					}

					for _, vm := range deployment.Vms {
						if vm.IP != "" {
							fmt.Printf("  Found VM '%s' on node %d with IP: %s\n",
								vm.Name, node, vm.IP)
							if len(tfBackends) == 0 {
								tfBackends = []string{fmt.Sprintf("http://%s:80", vm.IP)}
							}
						}
					}
				}
			}
		}

		// Validate gateway type
		if gatewayType != workloads.GatewayFQDNType && gatewayType != workloads.GatewayNameType {
			log.Fatal().Msgf("Unsupported gateway type: %s. Expected '%s' or '%s'.",
				gatewayType, workloads.GatewayFQDNType, workloads.GatewayNameType)
		}

		// Phase 4: Load network from healthy nodes only
		// Register only healthy nodes' network contracts in State
		for node, cID := range networkContractIDs {
			if node == brokenNodeID {
				continue
			}
			t.State.CurrentNodeDeployments[node] = append(t.State.CurrentNodeDeployments[node], cID)
		}

		var znet workloads.ZNet
		for {
			znet, err = t.State.LoadNetworkFromGrid(ctx, networkName)
			if err == nil {
				break
			}

			// If a node is unreachable (timeout, connection error), remove it and retry
			re := regexp.MustCompile(`node (?:client: )?(\d+)`)
			matches := re.FindStringSubmatch(err.Error())
			if matches == nil {
				log.Fatal().Err(err).Msg("Failed to load network from healthy nodes")
			}

			failedNode, parseErr := strconv.ParseUint(matches[1], 10, 32)
			if parseErr != nil {
				log.Fatal().Err(err).Msg("Failed to load network from healthy nodes")
			}

			fmt.Printf("Node %d is unreachable, skipping: %v\n", failedNode, err)
			delete(t.State.CurrentNodeDeployments, uint32(failedNode))

			if len(t.State.CurrentNodeDeployments) == 0 {
				log.Fatal().Msg("No reachable network nodes remaining")
			}
		}

		fmt.Printf("Network loaded from %d healthy node(s). IP range: %s\n",
			len(znet.Nodes), znet.IPRange.String())

		if wgConfigOnly {
			if znet.AccessWGConfig == "" {
				log.Fatal().Msg("No WireGuard access configured on this network.")
			}
			fmt.Println(znet.AccessWGConfig)
			return
		}

		// Phase 6: Collect gateway parameters (pre-populated from terraform or VM discovery)
		fmt.Println("\n=== GATEWAY REPAIR PARAMETERS ===")
		fmt.Printf("Gateway name: %s\n", gatewayName)
		fmt.Printf("Gateway type: %s\n", gatewayType)
		fmt.Printf("Network: %s\n", networkName)
		fmt.Printf("Broken node: %d\n", brokenNodeID)

		// FQDN (only for FQDN type)
		var fqdn string
		if gatewayType == workloads.GatewayFQDNType {
			if autoApprove && tfFQDN != "" {
				fqdn = tfFQDN
				fmt.Printf("FQDN: %s (from Terraform state)\n", fqdn)
			} else if autoApprove {
				log.Fatal().Msg("Cannot auto-approve: FQDN is required but not available from Terraform state. Run without -y.")
			} else {
				if tfFQDN != "" {
					fmt.Printf("\nFQDN from Terraform state: %s\n", tfFQDN)
					fmt.Printf("Enter FQDN (or press Enter to use %s): ", tfFQDN)
				} else {
					fmt.Print("\nEnter FQDN (e.g. cloud.example.com): ")
				}
				fqdnInput, err := scanner.ReadString('\n')
				if err != nil {
					log.Fatal().Err(err).Send()
				}
				fqdn = strings.TrimSpace(fqdnInput)
				if fqdn == "" {
					if tfFQDN != "" {
						fqdn = tfFQDN
					} else {
						log.Fatal().Msg("FQDN is required for FQDN gateway type.")
					}
				}
			}
		}

		// Backend URL
		defaultBackend := ""
		if len(tfBackends) > 0 {
			defaultBackend = tfBackends[0]
		}

		var backendURL string
		if autoApprove && defaultBackend != "" {
			backendURL = defaultBackend
			fmt.Printf("Backend: %s (auto)\n", backendURL)
		} else if autoApprove {
			log.Fatal().Msg("Cannot auto-approve: backend URL is required but not available. Run without -y.")
		} else {
			if defaultBackend != "" {
				if fromTF {
					fmt.Printf("Backend from Terraform state: %s\n", defaultBackend)
				} else {
					fmt.Printf("Auto-detected backend: %s\n", defaultBackend)
				}
				fmt.Printf("Enter backend URL (or press Enter to use %s): ", defaultBackend)
			} else {
				fmt.Print("Enter backend URL (e.g. http://10.20.2.2:80): ")
			}
			backendInput, err := scanner.ReadString('\n')
			if err != nil {
				log.Fatal().Err(err).Send()
			}
			backendURL = strings.TrimSpace(backendInput)
			if backendURL == "" {
				if defaultBackend != "" {
					backendURL = defaultBackend
				} else {
					log.Fatal().Msg("Backend URL is required.")
				}
			}
		}

		// TLS passthrough
		var tlsPassthrough bool
		if autoApprove {
			tlsPassthrough = tfTLS
		} else {
			tlsDefault := "no"
			if tfTLS {
				tlsDefault = "yes"
			}
			fmt.Printf("Enable TLS passthrough? (yes/no) [%s]: ", tlsDefault)
			tlsInput, err := scanner.ReadString('\n')
			if err != nil {
				log.Fatal().Err(err).Send()
			}
			tlsInput = strings.TrimSpace(strings.ToLower(tlsInput))
			if tlsInput == "" {
				tlsPassthrough = tfTLS
			} else {
				tlsPassthrough = tlsInput == "yes" || tlsInput == "y"
			}
		}

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

		if autoApprove {
			fmt.Println("\nAuto-approved (-y flag).")
		} else {
			fmt.Printf("\nTo confirm, type 'yes': ")
			confirmInput, err := scanner.ReadString('\n')
			if err != nil {
				log.Fatal().Err(err).Send()
			}
			if strings.TrimSpace(strings.ToLower(confirmInput)) != "yes" {
				fmt.Println("Aborted.")
				os.Exit(0)
			}
		}

		// Phase 7: Cancel broken contracts
		fmt.Println("\nCanceling orphaned contracts on broken node...")
		for _, cid := range []uint64{gwContractID, networkContractID} {
			err = t.SubstrateConn.CancelContract(t.Identity, cid)
			if err != nil {
				if strings.Contains(err.Error(), "ContractNotExists") {
					fmt.Printf("Contract %d already canceled, skipping.\n", cid)
				} else {
					log.Fatal().Err(err).Msgf("Failed to cancel contract %d. You may need to cancel it manually.", cid)
				}
			} else {
				fmt.Printf("Canceled contract %d\n", cid)
			}
		}

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

			fmt.Println("\n=== REPAIR COMPLETE ===")
			fmt.Printf("Gateway contract ID: %d\n", gw.ContractID)
			fmt.Printf("FQDN: %s\n", fqdn)
			fmt.Printf("Backend: %s\n", backendURL)
			fmt.Printf("Network: %s\n", networkName)
			fmt.Printf("Node: %d\n", brokenNodeID)

			printNetworkDeploymentIDs(znet.NodeDeploymentID)

			gwInfo, _ := json.MarshalIndent(gw, "", "\t")
			fmt.Println("\nGateway details:\n" + string(gwInfo))

			if znet.AccessWGConfig != "" {
				fmt.Println("\n=== WIREGUARD CONFIG ===")
				fmt.Println(znet.AccessWGConfig)
			}

			fmt.Println("\nGateway repair completed successfully!")
			fmt.Println("Please verify that your DNS record for", fqdn, "points to the gateway node.")
		} else {
			// Ensure we have the name contract ID for GatewayNameProxy
			if nameContractID == 0 {
				contracts, err := t.ContractsGetter.ListContractsByTwinID([]string{"Created"})
				if err == nil {
					for _, nc := range contracts.NameContracts {
						if nc.Name == gatewayName {
							nameContractID, _ = strconv.ParseUint(nc.ContractID, 10, 64)
							break
						}
					}
				}
				if nameContractID == 0 {
					log.Fatal().Msg("Could not find name contract for gateway name. It may need to be recreated manually.")
				}
				fmt.Printf("Found existing name contract: %d\n", nameContractID)
			}

			fmt.Println("\nDeploying Name gateway...")

			gw := workloads.GatewayNameProxy{
				NodeID:           brokenNodeID,
				Name:             gatewayName,
				Backends:         backends,
				TLSPassthrough:   tlsPassthrough,
				Network:          networkName,
				SolutionType:     projectName,
				NameContractID:   nameContractID,
				NodeDeploymentID: map[uint32]uint64{},
			}

			err = t.GatewayNameDeployer.Deploy(ctx, &gw)
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to deploy Name gateway")
			}

			fmt.Println("\n=== REPAIR COMPLETE ===")
			fmt.Printf("Gateway contract ID: %d\n", gw.ContractID)
			fmt.Printf("FQDN: %s\n", gw.FQDN)
			fmt.Printf("Backend: %s\n", backendURL)
			fmt.Printf("Network: %s\n", networkName)
			fmt.Printf("Node: %d\n", brokenNodeID)

			printNetworkDeploymentIDs(znet.NodeDeploymentID)

			gwInfo, _ := json.MarshalIndent(gw, "", "\t")
			fmt.Println("\nGateway details:\n" + string(gwInfo))

			if znet.AccessWGConfig != "" {
				fmt.Println("\n=== WIREGUARD CONFIG ===")
				fmt.Println(znet.AccessWGConfig)
			}

			fmt.Println("\nGateway repair completed successfully!")
			fmt.Println("Please verify that your DNS record for", gw.FQDN, "points to the gateway node.")
		}
	},
}

func printNetworkDeploymentIDs(nodeDeploymentID map[uint32]uint64) {
	fmt.Println("\nNetwork deployment IDs:")
	for node, cID := range nodeDeploymentID {
		fmt.Printf("  Node %d: contract %d\n", node, cID)
	}
}

func init() {
	rootCmd.AddCommand(repairGatewayCmd)
	repairGatewayCmd.Flags().Uint64("contract", 0,
		"Specify gateway contract ID directly, skipping interactive listing.")
	repairGatewayCmd.Flags().BoolP("yes", "y", false,
		"Auto-approve all prompts (requires Terraform state or sufficient defaults).")
	repairGatewayCmd.Flags().Bool("wg-config", false,
		"Retrieve and print the WireGuard config for the network, then exit (no repair).")
}
