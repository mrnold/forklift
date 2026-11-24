package nutanix

import (
	"encoding/json"
	"strings"

	model "github.com/kubev2v/forklift/pkg/controller/provider/model/nutanix"
	libclient "github.com/kubev2v/forklift/pkg/lib/client/nutanix"
)

func entityName(specName, statusName, metadataName string) string {
	return libclient.Coalesce(specName, statusName, metadataName)
}

func clusterRef(specUUID, statusUUID string) string {
	return libclient.Coalesce(specUUID, statusUUID)
}

type clusterConfig struct {
	ClusterArch   string   `json:"cluster_arch"`
	OperationMode string   `json:"operation_mode"`
	ServiceList   []string `json:"service_list"`
	Timezone      string   `json:"timezone"`
	Build         struct {
		FullVersion string `json:"full_version"`
		Version     string `json:"version"`
	} `json:"build"`
}

type clusterEntity struct {
	Metadata libclient.Metadata `json:"metadata"`
	Spec     struct {
		Name      string `json:"name"`
		Resources struct {
			Config clusterConfig `json:"config"`
		} `json:"resources"`
	} `json:"spec"`
	Status struct {
		Name      string `json:"name"`
		State     string `json:"state"`
		Resources struct {
			Analysis struct {
				Storage struct {
					TotalCapacityBytes int64 `json:"total_capacity_bytes"`
					UsageBytes         int64 `json:"usage_bytes"`
				} `json:"storage_summary"`
				VMCount int64 `json:"vm_count"`
			} `json:"analysis"`
			Config  clusterConfig `json:"config"`
			Network struct {
				ExternalIP string `json:"external_ip"`
			} `json:"network"`
			Nodes struct {
				HypervisorServerList []struct {
					IP string `json:"ip"`
				} `json:"hypervisor_server_list"`
			} `json:"nodes"`
		} `json:"resources"`
	} `json:"status"`
}

func (e clusterEntity) isPrismCentralCluster() bool {
	for _, services := range [][]string{
		e.Spec.Resources.Config.ServiceList,
		e.Status.Resources.Config.ServiceList,
	} {
		for _, service := range services {
			if service == "PRISM_CENTRAL" {
				return true
			}
		}
	}
	return false
}

func (e *clusterEntity) ApplyTo(m *model.Cluster) {
	config := e.Status.Resources.Config

	m.ID = e.Metadata.UUID
	m.ClusterUUID = e.Metadata.UUID
	m.Name = entityName(e.Spec.Name, e.Status.Name, e.Metadata.Name)
	m.Timezone = config.Timezone
	m.ClusterArch = config.ClusterArch
	m.OperationMode = config.OperationMode
	m.Version = config.Build.Version
	m.BuildVersion = config.Build.FullVersion
	m.ExternalIP = e.Status.Resources.Network.ExternalIP
	m.NumNodes = len(e.Status.Resources.Nodes.HypervisorServerList)
	m.VMCount = e.Status.Resources.Analysis.VMCount
	m.TotalCapacity = e.Status.Resources.Analysis.Storage.TotalCapacityBytes
	m.UsedCapacity = e.Status.Resources.Analysis.Storage.UsageBytes
}

type hostEntity struct {
	Metadata libclient.Metadata `json:"metadata"`
	Spec     struct {
		ClusterReference libclient.Ref `json:"cluster_reference"`
		Name             string        `json:"name"`
	} `json:"spec"`
	Status struct {
		ClusterReference libclient.Ref `json:"cluster_reference"`
		Name             string        `json:"name"`
		State            string        `json:"state"`
		Resources        hostResources `json:"resources"`
	} `json:"status"`
}

type hostResources struct {
	Block struct {
		BlockModel string `json:"block_model"`
	} `json:"block"`
	CPUCapacityHz int64  `json:"cpu_capacity_hz"`
	CPUModel      string `json:"cpu_model"`
	HostType      string `json:"host_type"`
	Hypervisor    struct {
		HypervisorFullName string `json:"hypervisor_full_name"`
		NumVMs             int    `json:"num_vms"`
	} `json:"hypervisor"`
	IPMI struct {
		IP string `json:"ip"`
	} `json:"ipmi"`
	MemoryCapacityMiB int64  `json:"memory_capacity_mib"`
	NumCpuCores       int    `json:"num_cpu_cores"`
	NumCpuSockets     int    `json:"num_cpu_sockets"`
	NumCpuThreads     int    `json:"num_cpu_threads"`
	SerialNumber      string `json:"serial_number"`
}

func (e hostEntity) clusterUUID() string {
	return clusterRef(e.Spec.ClusterReference.UUID, e.Status.ClusterReference.UUID)
}

func (e *hostEntity) ApplyTo(m *model.Host) {
	resources := e.Status.Resources

	m.ID = e.Metadata.UUID
	m.HostUUID = e.Metadata.UUID
	m.Name = entityName(e.Spec.Name, e.Status.Name, e.Metadata.Name)
	m.Cluster = e.clusterUUID()
	m.State = e.Status.State
	m.SerialNumber = resources.SerialNumber
	m.BlockModel = resources.Block.BlockModel
	m.HypervisorType = resources.Hypervisor.HypervisorFullName
	m.NumVMs = resources.Hypervisor.NumVMs
	m.HostType = resources.HostType
	m.CPUModel = resources.CPUModel
	m.CPUCapacityHz = resources.CPUCapacityHz
	m.NumCpuSockets = resources.NumCpuSockets
	m.NumCpuCores = resources.NumCpuCores
	m.NumCpuThreads = resources.NumCpuThreads
	m.MemoryCapacityMiB = resources.MemoryCapacityMiB
	m.IPMIAddress = resources.IPMI.IP
}

type networkEntity struct {
	Metadata libclient.Metadata `json:"metadata"`
	Spec     struct {
		ClusterReference     libclient.Ref    `json:"cluster_reference"`
		ClusterReferenceList []libclient.Ref  `json:"cluster_reference_list"`
		Name                 string           `json:"name"`
		Resources            networkResources `json:"resources"`
	} `json:"spec"`
	Status struct {
		ClusterReference     libclient.Ref    `json:"cluster_reference"`
		ClusterReferenceList []libclient.Ref  `json:"cluster_reference_list"`
		Name                 string           `json:"name"`
		Resources            networkResources `json:"resources"`
	} `json:"status"`
}

// dhcpAddr accepts either a plain string or {"ip":"..."} from Prism.
type dhcpAddr string

func (d *dhcpAddr) UnmarshalJSON(b []byte) error {
	if string(b) == "null" || len(b) == 0 {
		*d = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*d = dhcpAddr(s)
		return nil
	}
	var obj struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	*d = dhcpAddr(obj.IP)
	return nil
}

type networkResources struct {
	IPConfig struct {
		DefaultGatewayIP string `json:"default_gateway_ip"`
		DHCPOptions      struct {
			DHCPServerAddress    dhcpAddr `json:"dhcp_server_address"`
			DomainName           string   `json:"domain_name"`
			DomainNameServerList []string `json:"domain_name_server_list"`
		} `json:"dhcp_options"`
		PoolList []struct {
			Range string `json:"range"`
		} `json:"pool_list"`
		PrefixLength int    `json:"prefix_length"`
		SubnetIP     string `json:"subnet_ip"`
	} `json:"ip_config"`
	SubnetType           string `json:"subnet_type"`
	VlanID               int    `json:"vlan_id"`
	IsAdvancedNetworking bool   `json:"is_advanced_networking"`
}

func (e networkEntity) clusterUUID() string {
	if id := clusterRef(e.Spec.ClusterReference.UUID, e.Status.ClusterReference.UUID); id != "" {
		return id
	}
	for _, ref := range append(e.Status.ClusterReferenceList, e.Spec.ClusterReferenceList...) {
		if ref.UUID != "" {
			return ref.UUID
		}
	}
	return ""
}

func (e networkEntity) belongsToCluster(clusterUUID string) bool {
	if clusterUUID == "" {
		return true
	}
	if e.clusterUUID() == clusterUUID {
		return true
	}
	for _, ref := range append(e.Status.ClusterReferenceList, e.Spec.ClusterReferenceList...) {
		if ref.UUID == clusterUUID {
			return true
		}
	}
	return false
}

func mergeNetworkResources(status, spec networkResources) networkResources {
	out := status
	if out.SubnetType == "" {
		out.SubnetType = spec.SubnetType
	}
	if out.VlanID == 0 {
		out.VlanID = spec.VlanID
	}
	if !out.IsAdvancedNetworking {
		out.IsAdvancedNetworking = spec.IsAdvancedNetworking
	}
	if out.IPConfig.SubnetIP == "" {
		out.IPConfig.SubnetIP = spec.IPConfig.SubnetIP
	}
	if out.IPConfig.PrefixLength == 0 {
		out.IPConfig.PrefixLength = spec.IPConfig.PrefixLength
	}
	if out.IPConfig.DefaultGatewayIP == "" {
		out.IPConfig.DefaultGatewayIP = spec.IPConfig.DefaultGatewayIP
	}
	if out.IPConfig.DHCPOptions.DHCPServerAddress == "" {
		out.IPConfig.DHCPOptions.DHCPServerAddress = spec.IPConfig.DHCPOptions.DHCPServerAddress
	}
	if out.IPConfig.DHCPOptions.DomainName == "" {
		out.IPConfig.DHCPOptions.DomainName = spec.IPConfig.DHCPOptions.DomainName
	}
	if len(out.IPConfig.DHCPOptions.DomainNameServerList) == 0 {
		out.IPConfig.DHCPOptions.DomainNameServerList = spec.IPConfig.DHCPOptions.DomainNameServerList
	}
	if len(out.IPConfig.PoolList) == 0 {
		out.IPConfig.PoolList = spec.IPConfig.PoolList
	}
	return out
}

func (e *networkEntity) ApplyTo(m *model.Network) {
	resources := mergeNetworkResources(e.Status.Resources, e.Spec.Resources)
	ipConfig := resources.IPConfig

	m.ID = e.Metadata.UUID
	m.NetworkUUID = e.Metadata.UUID
	m.Name = entityName(e.Spec.Name, e.Status.Name, e.Metadata.Name)
	m.Cluster = e.clusterUUID()
	m.SubnetType = resources.SubnetType
	m.VlanID = resources.VlanID
	m.AdvancedNetworking = resources.IsAdvancedNetworking
	m.NetworkAddress = ipConfig.SubnetIP
	m.PrefixLength = ipConfig.PrefixLength
	m.DefaultGateway = ipConfig.DefaultGatewayIP
	m.DHCPServerIP = string(ipConfig.DHCPOptions.DHCPServerAddress)
	m.DHCPDomainName = ipConfig.DHCPOptions.DomainName
	m.DNSServers = strings.Join(ipConfig.DHCPOptions.DomainNameServerList, ",")

	ranges := make([]string, 0, len(ipConfig.PoolList))
	for _, pool := range ipConfig.PoolList {
		if pool.Range != "" {
			ranges = append(ranges, pool.Range)
		}
	}
	m.IPPoolRanges = strings.Join(ranges, ",")
}

type storageContainerEntity struct {
	Metadata libclient.Metadata     `json:"metadata"`
	Status   storageContainerStatus `json:"status"`
}

type storageContainerStatus struct {
	Resources storageContainerResources `json:"resources"`
}

type storageContainerResources struct {
	ClusterReference   libclient.Ref `json:"cluster_reference"`
	CompressionEnabled bool          `json:"compression_enabled"`
	ErasureCode        string        `json:"erasure_code"`
	MaxCapacityBytes   int64         `json:"max_capacity_bytes"`
	OnDiskDedup        string        `json:"on_disk_dedup"`
	ReplicationFactor  int           `json:"replication_factor"`
	UsageBytes         int64         `json:"usage_bytes"`
}

func (e storageContainerEntity) clusterUUID() string {
	return e.Status.Resources.ClusterReference.UUID
}

func (e *storageContainerEntity) ApplyTo(m *model.StorageContainer) {
	resources := e.Status.Resources

	m.ID = e.Metadata.UUID
	m.StorageContainerUUID = e.Metadata.UUID
	m.Name = e.Metadata.Name
	m.Cluster = resources.ClusterReference.UUID
	m.ReplicationFactor = resources.ReplicationFactor
	m.MaxCapacityBytes = resources.MaxCapacityBytes
	m.UsageBytes = resources.UsageBytes
	if m.MaxCapacityBytes > 0 {
		m.FreeBytes = m.MaxCapacityBytes - m.UsageBytes
	}
	m.CompressionEnabled = resources.CompressionEnabled
	m.OnDiskDedup = resources.OnDiskDedup
	m.ErasureCode = resources.ErasureCode
}

// imageEntity is a v3 Image Service wire entity mapped into inventory.
type imageEntity libclient.V3Image

func (e *imageEntity) ApplyTo(m *model.Image) {
	resources := e.Status.Resources

	m.ID = e.Metadata.UUID
	m.ImageUUID = e.Metadata.UUID
	m.Name = entityName(e.Spec.Name, e.Status.Name, e.Metadata.Name)
	m.ImageType = resources.ImageType
	m.SizeBytes = resources.SizeBytes
	m.Architecture = resources.Architecture
	m.SourceURI = resources.SourceURI
}
