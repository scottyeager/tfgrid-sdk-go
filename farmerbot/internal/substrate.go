package internal

import (
	"github.com/centrifuge/go-substrate-rpc-client/v4/types"
	substrate "github.com/threefoldtech/tfchain/clients/tfchain-client-go"
)

// Substrate is substrate client interface
type Substrate interface {
	SetNodePowerTarget(identity substrate.Identity, nodeID uint32, up bool) (hash types.Hash, err error)
	GetPowerTarget(nodeID uint32) (power substrate.NodePower, err error)

	GetNodeRentContract(nodeID uint32) (uint64, error)
	GetNode(nodeID uint32) (*substrate.Node, error)
	GetFarm(id uint32) (*substrate.Farm, error)
	GetNodes(farmID uint32) ([]uint32, error)
	GetDedicatedNodePrice(nodeID uint32) (uint64, error)
	GetTwinByPubKey(publicKey []byte) (uint32, error)
}
