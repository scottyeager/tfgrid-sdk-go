// Package filters for filtering nodes for needed resources
package filters

import (
	"github.com/threefoldtech/tfgrid-sdk-go/grid-client/workloads"
	"github.com/threefoldtech/tfgrid-sdk-go/grid-proxy/pkg/types"
)

// BuildK8sNodeFilter build a filter for a k8s node
func BuildK8sNodeFilter(k8sNode workloads.K8sNode, farmID uint64) (types.NodeFilter, []uint64, []uint64) {
	freeMRUs := k8sNode.MemoryMB / 1024
	freeSRUs := k8sNode.DiskSizeGB
	freeIPs := uint64(0)

	if k8sNode.PublicIP {
		freeIPs = uint64(1)
	}
	disks := []uint64{*convertGBToBytes(k8sNode.DiskSizeGB)}
	// k8s rootfs is either 2 or 0.5
	rootfss := []uint64{*convertGBToBytes(uint64(2))}

	return buildGenericFilter(&freeMRUs, &freeSRUs, nil, &freeIPs, []uint64{farmID}, nil), disks, rootfss
}

// BuildVMFilter build a filter for a vm
func BuildVMFilter(vm workloads.VM, disk workloads.Disk, farmID uint64) (types.NodeFilter, []uint64, []uint64) {
	freeMRUs := vm.MemoryMB / 1024
	freeSRUs := vm.RootfsSizeMB / 1024
	freeIPs := uint64(0)
	if vm.PublicIP {
		freeIPs = 1
	}
	freeSRUs += disk.SizeGB

	disks := make([]uint64, 0)
	if disk.SizeGB > 0 {
		disks = append(disks, *convertGBToBytes(disk.SizeGB))
	}
	rootfss := []uint64{*convertGBToBytes(vm.RootfsSizeMB / 1024)}
	return buildGenericFilter(&freeMRUs, &freeSRUs, nil, &freeIPs, []uint64{farmID}, nil), disks, rootfss
}

// BuildGatewayFilter build a filter for a gateway
func BuildGatewayFilter(farmID uint64) types.NodeFilter {
	domain := true
	return buildGenericFilter(nil, nil, nil, nil, []uint64{farmID}, &domain)
}

// BuildZDBFilter build a filter for a zdbs
func BuildZDBFilter(zdb workloads.ZDB, n int, farmID uint64) (types.NodeFilter, []uint64) {
	freeHRUs := zdb.SizeGB * uint64(n)
	return buildGenericFilter(nil, nil, &freeHRUs, nil, []uint64{farmID}, nil), []uint64{*convertGBToBytes(freeHRUs)}
}

func buildGenericFilter(mrus, srus, hrus, ips *uint64, farmIDs []uint64, domain *bool) types.NodeFilter {
	var freeMRUs *uint64
	if mrus != nil {
		freeMRUs = convertGBToBytes(*mrus)
	}
	var freeSRUs *uint64
	if srus != nil {
		freeSRUs = convertGBToBytes(*srus)
	}
	var freeHRUs *uint64
	if hrus != nil {
		freeHRUs = convertGBToBytes(*hrus)
	}
	rented := false
	return types.NodeFilter{
		Status:  []string{"up"},
		FreeMRU: freeMRUs,
		FreeSRU: freeSRUs,
		FreeHRU: freeHRUs,
		FreeIPs: ips,
		FarmIDs: farmIDs,
		Domain:  domain,
		Rented:  &rented,
	}
}

func convertGBToBytes(gb uint64) *uint64 {
	bytes := gb * 1024 * 1024 * 1024
	return &bytes
}
