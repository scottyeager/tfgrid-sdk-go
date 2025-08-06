// Package workloads includes workloads types (vm, zdb, QSFS, public IP, gateway name, gateway fqdn, disk)
package workloads

import (
	"testing"

	"github.com/scottyeager/tfgrid-sdk-go/grid-client/zos"
	"github.com/stretchr/testify/assert"
)

func TestPublicIPWorkload(t *testing.T) {
	var publicIPWorkload zos.Workload

	t.Run("test_construct_pub_ip_workload", func(t *testing.T) {
		publicIPWorkload = ConstructPublicIPWorkload("test", true, true)
		assert.Equal(t, publicIPWorkload.Type, zos.PublicIPType)
	})
}
