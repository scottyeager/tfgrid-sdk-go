// Package deployer is grid deployer
package deployer

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	"github.com/gorilla/schema"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	client "github.com/threefoldtech/tfgrid-sdk-go/grid-client/node"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/subi"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-proxy/pkg/types"
	"github.com/threefoldtech/zos/pkg/gridtypes"
	"github.com/threefoldtech/zos/pkg/gridtypes/zos"
)

// FilterNodes filters nodes using proxy
func FilterNodes(ctx context.Context, tf TFPluginClient, options types.NodeFilter, ssdDisks, hddDisks []uint64, rootfs []uint64, optionalLimit ...int) ([]types.Node, error) {
	limit := types.Limit{Randomize: true}

	if options.AvailableFor == nil {
		twinID := uint64(tf.TwinID)
		options.AvailableFor = &twinID
	}

	if len(optionalLimit) == 0 {
		return filterNodes(ctx, tf, options, ssdDisks, hddDisks, rootfs, limit)
	}

	pagesCount := 2
	cnt := optionalLimit[0]
	limit.Size = uint64(cnt)

	if cnt < 100 {
		pagesCount = 3
	}

	var nodes []types.Node
	var allErrors error
	var wg sync.WaitGroup
	var lock sync.Mutex

	for i := 0; i < pagesCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			if len(nodes) >= cnt {
				return
			}

			limit.Page = uint64(i)
			nodesFounds, err := filterNodes(ctx, tf, options, ssdDisks, hddDisks, rootfs, limit)
			if err != nil {
				lock.Lock()
				allErrors = errors.Wrap(allErrors, err.Error())
				lock.Unlock()

				return
			}

			lock.Lock()
			nodes = append(nodes, nodesFounds...)
			lock.Unlock()
		}(i)
	}
	wg.Wait()

	if len(nodes) < cnt {
		options, err := serializeOptions(options)
		if err != nil {
			log.Debug().Msg(err.Error())
		}
		return []types.Node{}, errors.Errorf("could not find enough node with options: %s", options)
	}

	return nodes[:cnt], allErrors
}

func filterNodes(ctx context.Context, tfPlugin TFPluginClient, options types.NodeFilter, ssdDisks, hddDisks []uint64, rootfs []uint64, limit types.Limit) ([]types.Node, error) {
	nodes, _, err := tfPlugin.GridProxyClient.Nodes(ctx, options, limit)
	if err != nil {
		return []types.Node{}, errors.Wrap(err, "could not fetch nodes from the rmb proxy")
	}

	if len(nodes) == 0 {
		options, err := serializeOptions(options)
		if err != nil {
			log.Debug().Msg(err.Error())
		}
		return []types.Node{}, errors.Errorf("could not find any node with options: %s", options)
	}

	// if no storage needed
	if options.FreeSRU == nil && options.FreeHRU == nil {
		// only pinging here because if there is storage required it will be pinged with Pools call
		return pingNodes(ctx, tfPlugin.NcPool, tfPlugin.SubstrateConn, nodes), nil
	}
	sort.Slice(ssdDisks, func(i, j int) bool {
		return ssdDisks[i] > ssdDisks[j]
	})

	// add rootfs at the end to as zos provisions zmounts first.
	ssdDisks = append(ssdDisks, rootfs...)

	sort.Slice(hddDisks, func(i, j int) bool {
		return hddDisks[i] > hddDisks[j]
	})

	// check pools
	var nodePools []types.Node
	var wg sync.WaitGroup
	var lock sync.Mutex

	for _, node := range nodes {
		wg.Add(1)

		go func(node types.Node) {
			defer wg.Done()

			client, err := tfPlugin.NcPool.GetNodeClient(tfPlugin.SubstrateConn, uint32(node.NodeID))
			if err != nil {
				log.Debug().Err(err).Msgf("failed to get node '%d' client", node.NodeID)
				return
			}

			pools, err := client.Pools(ctx)
			if err != nil {
				log.Debug().Err(err).Msgf("failed to get node '%d' pools", node.NodeID)
				return
			}
			if hasEnoughStorage(pools, ssdDisks, zos.SSDDevice) && hasEnoughStorage(pools, hddDisks, zos.HDDDevice) {
				lock.Lock()
				nodePools = append(nodePools, node)
				lock.Unlock()
			}
		}(node)
	}

	wg.Wait()

	if len(nodePools) == 0 {
		var freeSRU uint64
		if options.FreeSRU != nil {
			freeSRU = convertBytesToGB(*options.FreeSRU)
		}
		var freeHRU uint64
		if options.FreeHRU != nil {
			freeHRU = convertBytesToGB(*options.FreeHRU)
		}

		return []types.Node{}, errors.Errorf("could not find any node with free ssd pools: %d GB and free hdd pools: %d GB", freeSRU, freeHRU)
	}

	return nodePools, nil
}

func pingNodes(ctx context.Context, clientGetter client.NodeClientGetter, sub subi.SubstrateExt, nodes []types.Node) []types.Node {
	var workingNodes []types.Node
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, node := range nodes {
		wg.Add(1)
		go func(node types.Node) {
			defer wg.Done()
			client, err := clientGetter.GetNodeClient(sub, uint32(node.NodeID))
			if err != nil {
				log.Debug().Err(err).Msgf("failed to get node %d client", node.NodeID)
				return
			}
			_, err = client.SystemVersion(ctx)
			if err != nil {
				log.Debug().Err(err).Msgf("failed to ping node %d", node.NodeID)
				return
			}
			mu.Lock()
			workingNodes = append(workingNodes, node)
			mu.Unlock()
		}(node)
	}
	wg.Wait()
	return workingNodes
}

var (
	trueVal  = true
	statusUp = "up"
)

// GetPublicNode return public node ID
func GetPublicNode(ctx context.Context, tfPlugin TFPluginClient, preferredNodes []uint32) (uint32, error) {
	preferredNodesSet := make(map[int]struct{})
	for _, node := range preferredNodes {
		preferredNodesSet[int(node)] = struct{}{}
	}

	nodes, err := FilterNodes(
		ctx,
		tfPlugin,
		types.NodeFilter{
			IPv4:   &trueVal,
			Status: &statusUp,
		},
		nil,
		nil,
		nil)
	if err != nil {
		return 0, err
	}

	// force add preferred nodes
	nodeMap := make(map[int]struct{})
	for _, node := range nodes {
		nodeMap[node.NodeID] = struct{}{}
	}

	for _, node := range preferredNodes {
		if _, ok := nodeMap[int(node)]; ok {
			continue
		}
		nodeInfo, err := tfPlugin.GridProxyClient.Node(ctx, node)
		if err != nil {
			log.Error().Msgf("failed to get node %d from the grid proxy", node)
			continue
		}
		if nodeInfo.PublicConfig.Ipv4 == "" {
			continue
		}
		if nodeInfo.Status != "up" {
			continue
		}
		nodes = append(nodes, types.Node{
			PublicConfig: nodeInfo.PublicConfig,
		})
	}

	lastPreferred := 0
	for i := range nodes {
		if _, ok := preferredNodesSet[nodes[i].NodeID]; ok {
			nodes[i], nodes[lastPreferred] = nodes[lastPreferred], nodes[i]
			lastPreferred++
		}
	}

	for _, node := range nodes {
		log.Printf("found a node with ipv4 public config: %d %s\n", node.NodeID, node.PublicConfig.Ipv4)
		ip, _, err := net.ParseCIDR(node.PublicConfig.Ipv4)
		if err != nil {
			log.Printf("could not parse public ip %s of node %d: %s", node.PublicConfig.Ipv4, node.NodeID, err.Error())
			continue
		}
		if ip.IsPrivate() {
			log.Printf("public ip %s of node %d is private", node.PublicConfig.Ipv4, node.NodeID)
			continue
		}
		return uint32(node.NodeID), nil
	}

	return 0, errors.New("no nodes with public ipv4")
}

// hasEnoughStorage checks if all deployment storage requirements can be satisfied with node's pools based on given disks order.
func hasEnoughStorage(pools []client.PoolMetrics, storages []uint64, poolType zos.DeviceType) bool {
	if len(storages) == 0 {
		return true
	}

	filteredPools := make([]client.PoolMetrics, 0)
	for _, pool := range pools {
		if pool.Type == poolType {
			filteredPools = append(filteredPools, pool)
		}
	}
	if len(filteredPools) == 0 {
		return false
	}
	for _, storage := range storages {
		sort.Slice(filteredPools, func(i, j int) bool {
			return (filteredPools[i].Size - filteredPools[i].Used) > (filteredPools[j].Size - filteredPools[j].Used)
		})
		// assuming zos provision to the largest pool always
		if filteredPools[0].Size-filteredPools[0].Used < gridtypes.Unit(storage) {
			return false
		}
		filteredPools[0].Used += gridtypes.Unit(storage)
	}
	return true
}

// serializeOptions used to encode a struct of NodeFilter type and convert it to string
// with only non-zero values and drop any field with zero-value
func serializeOptions(options types.NodeFilter) (string, error) {
	params := make(map[string][]string)
	err := schema.NewEncoder().Encode(options, params)
	if err != nil {
		return "", nil
	}

	// convert the map to string with `key: value` format
	//
	// example:
	//
	// map[string][]string{Status: [up]} -> "Status: [up]"
	var sb strings.Builder
	for key, val := range params {
		fmt.Fprintf(&sb, "%s: %v, ", key, val[0])
	}

	filter := sb.String()
	if len(filter) > 2 {
		filter = filter[:len(filter)-2]
	}
	return filter, nil
}

func convertBytesToGB(bytes uint64) uint64 {
	gb := bytes / (1024 * 1024 * 1024)
	return gb
}
