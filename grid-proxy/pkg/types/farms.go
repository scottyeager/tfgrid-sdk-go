package types

// Farm info about the farm
type Farm struct {
	Name              string     `json:"name" sort:"name"`
	FarmID            int        `json:"farmId" sort:"farm_id"`
	TwinID            int        `json:"twinId" sort:"twin_id"`
	PricingPolicyID   int        `json:"pricingPolicyId"`
	CertificationType string     `json:"certificationType"`
	StellarAddress    string     `json:"stellarAddress"`
	Dedicated         bool       `json:"dedicated" sort:"dedicated"`
	PublicIps         []PublicIP `json:"publicIps" sort:"public_ips"`
}

// PublicIP info about public ip in the farm
type PublicIP struct {
	ID         string `json:"id"`
	IP         string `json:"ip"`
	FarmID     string `json:"farm_id"`
	ContractID int    `json:"contract_id"`
	Gateway    string `json:"gateway"`
}

// FarmFilter farm filters
type FarmFilter struct {
	FreeIPs           *uint64  `schema:"free_ips,omitempty"`
	TotalIPs          *uint64  `schema:"total_ips,omitempty"`
	StellarAddress    *string  `schema:"stellar_address,omitempty"`
	PricingPolicyID   *uint64  `schema:"pricing_policy_id,omitempty"`
	FarmID            *uint64  `schema:"farm_id,omitempty"`
	TwinID            *uint64  `schema:"twin_id,omitempty"`
	Name              *string  `schema:"name,omitempty"`
	NameContains      *string  `schema:"name_contains,omitempty"`
	CertificationType *string  `schema:"certification_type,omitempty"`
	Dedicated         *bool    `schema:"dedicated,omitempty"`
	NodeFreeMRU       *uint64  `schema:"node_free_mru,omitempty"`
	NodeFreeHRU       *uint64  `schema:"node_free_hru,omitempty"`
	NodeFreeSRU       *uint64  `schema:"node_free_sru,omitempty"`
	NodeTotalCRU      *uint64  `schema:"node_total_cru,omitempty"`
	NodeStatus        []string `schema:"node_status,omitempty"`
	NodeRentedBy      *uint64  `schema:"node_rented_by,omitempty"`
	NodeAvailableFor  *uint64  `schema:"node_available_for,omitempty"`
	NodeHasGPU        *bool    `schema:"node_has_gpu,omitempty"`
	NodeHasIpv6       *bool    `schema:"node_has_ipv6,omitempty"`
	NodeCertified     *bool    `schema:"node_certified,omitempty"`
	Country           *string  `schema:"country,omitempty"`
	Region            *string  `schema:"region,omitempty"`
}
