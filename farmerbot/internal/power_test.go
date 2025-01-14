package internal

import (
	"context"
	"testing"
	"time"

	"github.com/centrifuge/go-substrate-rpc-client/v4/types"
	"github.com/golang/mock/gomock"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	substrate "github.com/threefoldtech/tfchain/clients/tfchain-client-go"
	"github.com/threefoldtech/tfgrid-sdk-go/farmerbot/mocks"
	"github.com/threefoldtech/tfgrid-sdk-go/rmb-sdk-go/peer"
	"github.com/threefoldtech/zos/pkg/gridtypes"
)

func TestPowerLargeScale(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	sub := mocks.NewMockSubstrate(ctrl)
	rmb := mocks.NewMockRMB(ctrl)

	ctx := context.Background()

	inputs := Config{
		FarmID:        1,
		IncludedNodes: []uint32{1, 2, 3, 4, 5, 6, 7},
		PriorityNodes: []uint32{7, 2, 2, 10},
	}

	// mock state
	resources := gridtypes.Capacity{HRU: 1, SRU: 1, CRU: 1, MRU: 1}
	mockRMBAndSubstrateCalls(ctx, sub, rmb, inputs, true, false, resources, []string{}, false, false)

	state, err := newState(ctx, sub, rmb, inputs, farmTwinID)
	assert.NoError(t, err)

	farmerbot := FarmerBot{
		state:            state,
		substrateManager: nil,
		gridProxyClient:  nil,
		rmbNodeClient:    rmb,
		network:          "dev",
		mnemonicOrSeed:   aliceSeed,
		keyType:          peer.KeyTypeSr25519,
		identity:         nil,
		twinID:           0,
	}

	t.Run("test valid power off: all nodes will be off except one node", func(t *testing.T) {
		for _, node := range farmerbot.nodes {
			if node.ID == 6 {
				continue
			}
			sub.EXPECT().SetNodePowerTarget(farmerbot.identity, uint32(node.ID), false).Return(types.Hash{}, nil)
		}

		err = farmerbot.manageNodesPower(sub)
		assert.NoError(t, err)

		assert.Equal(t, len(farmerbot.filterNodesPower([]powerState{on})), 1)
		assert.Equal(t, len(farmerbot.filterNodesPower([]powerState{shuttingDown})), len(farmerbot.nodes)-1)
	})

	t.Run("test valid power off (all are off except 2): all nodes will be off except one node", func(t *testing.T) {
		for _, node := range farmerbot.nodes {
			node.lastTimePowerStateChanged = time.Now().Add(-periodicWakeUpDuration - time.Minute)
			if node.ID == 1 || node.ID == 2 {
				node.powerState = on
				assert.NoError(t, state.updateNode(node))
				continue
			}

			node.powerState = off
			assert.NoError(t, state.updateNode(node))
		}

		sub.EXPECT().SetNodePowerTarget(farmerbot.identity, gomock.Any(), false).Return(types.Hash{}, nil)

		err = farmerbot.manageNodesPower(sub)
		assert.NoError(t, err)

		assert.Equal(t, len(farmerbot.filterNodesPower([]powerState{on})), 1)
		assert.Equal(t, len(farmerbot.filterNodesPower([]powerState{shuttingDown})), 1)
		assert.Equal(t, len(farmerbot.filterNodesPower([]powerState{off})), len(farmerbot.nodes)-2)
	})
}

func TestPower(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	sub := mocks.NewMockSubstrate(ctrl)
	rmb := mocks.NewMockRMB(ctrl)

	ctx := context.Background()

	inputs := Config{
		FarmID:        1,
		IncludedNodes: []uint32{1, 2},
		Power: power{WakeUpThresholdPercentages: ThresholdPercentages{
			CRU: 30,
			SRU: 30,
			MRU: 30,
			HRU: 30,
		}},
	}

	farmerbot, err := NewFarmerBot(ctx, inputs, "dev", aliceSeed, peer.KeyTypeSr25519)
	assert.Error(t, err)

	// mock state
	resources := gridtypes.Capacity{HRU: 1, SRU: 1, CRU: 1, MRU: 1}
	mockRMBAndSubstrateCalls(ctx, sub, rmb, inputs, true, false, resources, []string{}, false, false)

	state, err := newState(ctx, sub, rmb, inputs, farmTwinID)
	assert.NoError(t, err)
	farmerbot.state = state

	oldNode1 := farmerbot.nodes[0]
	oldNode2 := farmerbot.nodes[1]

	t.Run("test valid power on: already on", func(t *testing.T) {
		err = farmerbot.powerOn(sub, uint32(state.nodes[0].ID))
		assert.NoError(t, err)
	})

	t.Run("test valid power on: already waking up", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.powerState = wakingUp
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.powerOn(sub, uint32(state.nodes[0].ID))
		assert.NoError(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test valid power on", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.powerState = off
		assert.NoError(t, state.updateNode(testNode))

		sub.EXPECT().SetNodePowerTarget(farmerbot.identity, uint32(state.nodes[0].ID), true).Return(types.Hash{}, nil)

		err = farmerbot.powerOn(sub, uint32(state.nodes[0].ID))
		assert.NoError(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test invalid power on: node not found", func(t *testing.T) {
		err = farmerbot.powerOn(sub, 3)
		assert.Error(t, err)
	})

	t.Run("test invalid power on: set node failed", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.powerState = off
		assert.NoError(t, state.updateNode(testNode))

		sub.EXPECT().SetNodePowerTarget(farmerbot.identity, uint32(state.nodes[0].ID), true).Return(types.Hash{}, errors.New("error"))

		err = farmerbot.powerOn(sub, uint32(state.nodes[0].ID))
		assert.Error(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test valid power off: already off", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.powerState = off
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.powerOff(sub, uint32(state.nodes[0].ID))
		assert.NoError(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test valid power off: already shutting down", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.powerState = shuttingDown
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.powerOff(sub, uint32(state.nodes[0].ID))
		assert.NoError(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test valid power off", func(t *testing.T) {
		sub.EXPECT().SetNodePowerTarget(farmerbot.identity, uint32(state.nodes[0].ID), false).Return(types.Hash{}, nil)

		err = farmerbot.powerOff(sub, uint32(state.nodes[0].ID))
		assert.NoError(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test invalid power off: one node is on and cannot be off", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.powerState = off
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.powerOff(sub, uint32(state.nodes[1].ID))
		assert.Error(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
		assert.NoError(t, state.updateNode(oldNode2))
	})

	t.Run("test invalid power off: node is set to never shutdown", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.neverShutDown = true
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.powerOff(sub, uint32(state.nodes[0].ID))
		assert.Error(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test invalid power off: node has public config", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.PublicConfig.HasValue = true
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.powerOff(sub, uint32(state.nodes[0].ID))
		assert.Error(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test invalid power off: node has rent contract", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.hasActiveRentContract = true
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.powerOff(sub, uint32(state.nodes[0].ID))
		assert.Error(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test invalid power off: node has active contracts", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.hasActiveContracts = true
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.powerOff(sub, uint32(state.nodes[0].ID))
		assert.Error(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test invalid power off: node power changed", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.lastTimePowerStateChanged = time.Now()
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.powerOff(sub, uint32(state.nodes[0].ID))
		assert.Error(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test invalid power off: node has used resources", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.resources.used = testNode.resources.total
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.powerOff(sub, uint32(state.nodes[0].ID))
		assert.Error(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test invalid power off: node not found", func(t *testing.T) {
		err = farmerbot.powerOff(sub, 3)
		assert.Error(t, err)
	})

	t.Run("test invalid power off: set node power failed", func(t *testing.T) {
		sub.EXPECT().SetNodePowerTarget(farmerbot.identity, uint32(state.nodes[0].ID), false).Return(types.Hash{}, errors.New("error"))
		sub.EXPECT().GetPowerTarget(gomock.Any()).Return(substrate.NodePower{}, nil)

		err = farmerbot.powerOff(sub, uint32(state.nodes[0].ID))
		assert.Error(t, err)
		assert.Equal(t, state.nodes[0].powerState, on)
		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test invalid power off: set and get node power failed", func(t *testing.T) {
		sub.EXPECT().SetNodePowerTarget(farmerbot.identity, uint32(state.nodes[0].ID), false).Return(types.Hash{}, errors.New("error"))
		sub.EXPECT().GetPowerTarget(gomock.Any()).Return(substrate.NodePower{}, errors.New("error"))

		err = farmerbot.powerOff(sub, uint32(state.nodes[0].ID))
		assert.Error(t, err)
		assert.Equal(t, state.nodes[0].powerState, on)
		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test invalid power off: set node power failed but target is changed", func(t *testing.T) {
		sub.EXPECT().SetNodePowerTarget(farmerbot.identity, uint32(state.nodes[0].ID), false).Return(types.Hash{}, errors.New("error"))
		sub.EXPECT().GetPowerTarget(uint32(state.nodes[0].ID)).Return(substrate.NodePower{
			Target: substrate.Power{IsDown: true},
		}, nil)

		err = farmerbot.powerOff(sub, uint32(state.nodes[0].ID))
		assert.Error(t, err)
		assert.Equal(t, state.nodes[0].powerState, shuttingDown)
		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test power management: a node to shutdown (failed set the first node)", func(t *testing.T) {
		sub.EXPECT().SetNodePowerTarget(farmerbot.identity, gomock.Any(), false).Return(types.Hash{}, errors.New("error"))
		sub.EXPECT().GetPowerTarget(gomock.Any()).Return(substrate.NodePower{}, nil)
		sub.EXPECT().SetNodePowerTarget(farmerbot.identity, gomock.Any(), false).Return(types.Hash{}, nil)

		err = farmerbot.manageNodesPower(sub)
		assert.NoError(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
		assert.NoError(t, state.updateNode(oldNode2))
	})

	t.Run("test power management: a node to shutdown (failed set the first node but power target changes)", func(t *testing.T) {
		sub.EXPECT().SetNodePowerTarget(farmerbot.identity, gomock.Any(), false).Return(types.Hash{}, errors.New("error"))
		sub.EXPECT().GetPowerTarget(gomock.Any()).Return(substrate.NodePower{
			Target: substrate.Power{IsDown: true},
		}, nil)

		err = farmerbot.manageNodesPower(sub)
		assert.NoError(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
		assert.NoError(t, state.updateNode(oldNode2))
	})

	t.Run("test power management: nothing to shut down", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.powerState = off
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.manageNodesPower(sub)
		assert.NoError(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test power management: cannot shutdown public config", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.PublicConfig.HasValue = true
		assert.NoError(t, state.updateNode(testNode))
		testNode = state.nodes[1]
		testNode.PublicConfig.HasValue = true
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.manageNodesPower(sub)
		assert.NoError(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
		assert.NoError(t, state.updateNode(oldNode2))
	})

	t.Run("test power management: node is waking up", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.powerState = wakingUp
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.manageNodesPower(sub)
		assert.NoError(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
	})

	t.Run("test power management: a node to wake up (node 1 is used and node 2 is off)", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.resources.used = testNode.resources.total
		assert.NoError(t, state.updateNode(testNode))
		testNode = state.nodes[1]
		testNode.powerState = off
		assert.NoError(t, state.updateNode(testNode))

		sub.EXPECT().SetNodePowerTarget(farmerbot.identity, uint32(state.nodes[1].ID), true).Return(types.Hash{}, nil)

		err = farmerbot.manageNodesPower(sub)
		assert.NoError(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
		assert.NoError(t, state.updateNode(oldNode2))
	})

	t.Run("test power management: a node to wake up (node 1 has rent contract)", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.hasActiveRentContract = true
		assert.NoError(t, state.updateNode(testNode))
		testNode = state.nodes[1]
		testNode.powerState = off
		assert.NoError(t, state.updateNode(testNode))

		sub.EXPECT().SetNodePowerTarget(farmerbot.identity, uint32(state.nodes[1].ID), true).Return(types.Hash{}, nil)

		err = farmerbot.manageNodesPower(sub)
		assert.NoError(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
		assert.NoError(t, state.updateNode(oldNode2))
	})

	t.Run("test invalid power management: no nodes to wake up (usage is high)", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.resources.used = testNode.resources.total
		assert.NoError(t, state.updateNode(testNode))
		testNode = state.nodes[1]
		testNode.resources.used = testNode.resources.total
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.manageNodesPower(sub)
		assert.Error(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
		assert.NoError(t, state.updateNode(oldNode2))
	})

	t.Run("test valid power management: second node has no resources (usage is low)", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.resources.used = testNode.resources.total
		testNode.resources.used.cru = 0
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.manageNodesPower(sub)
		assert.NoError(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
		assert.NoError(t, state.updateNode(oldNode2))
	})

	t.Run("test power management: total resources is 0 (nothing happens)", func(t *testing.T) {
		testNode := state.nodes[0]
		testNode.resources.total = capacity{}
		assert.NoError(t, state.updateNode(testNode))
		testNode = state.nodes[1]
		testNode.resources.total = capacity{}
		assert.NoError(t, state.updateNode(testNode))

		err = farmerbot.manageNodesPower(sub)
		assert.NoError(t, err)

		assert.NoError(t, state.updateNode(oldNode1))
		assert.NoError(t, state.updateNode(oldNode2))
	})
}
