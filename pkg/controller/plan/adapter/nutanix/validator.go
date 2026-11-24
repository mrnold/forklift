package nutanix

import (
	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
	planbase "github.com/kubev2v/forklift/pkg/controller/plan/adapter/base"
	plancontext "github.com/kubev2v/forklift/pkg/controller/plan/context"
	model "github.com/kubev2v/forklift/pkg/controller/provider/web/nutanix"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Validator struct {
	*plancontext.Context
}

func (r *Validator) WarmMigration() bool {
	return false
}

func (r *Validator) MigrationType() bool {
	switch r.Plan.Spec.Type {
	case api.MigrationCold, "":
		return true
	default:
		return false
	}
}

func (r *Validator) StorageMapped(_ ref.Ref) (bool, error) {
	// TODO: validate storage container mappings against VM disks
	return true, nil
}

func (r *Validator) DirectStorage(_ ref.Ref) (bool, error) {
	return true, nil
}

func (r *Validator) NetworksMapped(vmRef ref.Ref) (bool, error) {
	vm := &model.VM{}
	if err := r.Source.Inventory.Find(vm, vmRef); err != nil {
		return false, liberr.Wrap(err, "vm", vmRef.String())
	}
	if r.Map.Network == nil {
		return false, nil
	}
	for _, nic := range vm.NICs {
		if nic.SubnetUUID == "" {
			continue
		}
		mapped := false
		for _, pair := range r.Map.Network.Spec.Map {
			if pair.Source.ID == nic.SubnetUUID {
				mapped = true
				break
			}
		}
		if !mapped {
			return false, nil
		}
	}
	return true, nil
}

func (r *Validator) MaintenanceMode(_ ref.Ref) (bool, error) {
	return true, nil
}

func (r *Validator) NICNetworkRefs(vmRef ref.Ref) ([]ref.Ref, error) {
	vm := &model.VM{}
	if err := r.Source.Inventory.Find(vm, vmRef); err != nil {
		return nil, liberr.Wrap(err, "vm", vmRef.String())
	}
	refs := make([]ref.Ref, 0, len(vm.NICs))
	for _, nic := range vm.NICs {
		if nic.SubnetUUID == "" {
			continue
		}
		refs = append(refs, ref.Ref{ID: nic.SubnetUUID, Name: nic.SubnetName})
	}
	return refs, nil
}

func (r *Validator) StaticIPs(vmRef ref.Ref) (bool, error) {
	if !r.Plan.Spec.PreserveStaticIPs {
		return true, nil
	}
	vm := &model.VM{}
	if err := r.Source.Inventory.Find(vm, vmRef); err != nil {
		return false, liberr.Wrap(err, "vm", vmRef.String())
	}
	for _, nic := range vm.NICs {
		if !nicPreservesStaticIP(nic, r.Context) {
			continue
		}
		if len(nic.StaticIPConfigs) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (r *Validator) UdnStaticIPs(_ ref.Ref, _ client.Client) (bool, error) {
	return true, nil
}

func (r *Validator) SharedDisks(_ ref.Ref, _ client.Client) (bool, string, string, error) {
	return true, "", "", nil
}

func (r *Validator) ExcludedDisks(_ ref.Ref) (bool, string, string, error) {
	return true, "", "", nil
}

func (r *Validator) ChangeTrackingEnabled(_ ref.Ref) (bool, error) {
	return true, nil
}

func (r *Validator) HasSnapshot(_ ref.Ref) (bool, string, string, error) {
	return true, "", "", nil
}

func (r *Validator) PowerState(_ ref.Ref) (bool, error) {
	return true, nil
}

func (r *Validator) VMMigrationType(_ ref.Ref) (bool, error) {
	return true, nil
}

func (r *Validator) InvalidDiskSizes(_ ref.Ref) ([]string, error) {
	// TODO: return disk UUIDs with invalid sizes
	return nil, nil
}

func (r *Validator) MacConflicts(_ ref.Ref) ([]planbase.MacConflict, error) {
	// TODO: check source NIC MACs against destination inventory
	return nil, nil
}

func (r *Validator) PVCNameTemplate(_ ref.Ref, _ string) (bool, error) {
	// TODO: validate PVC name template against VM disks
	return true, nil
}

func (r *Validator) GuestToolsInstalled(_ ref.Ref) (bool, error) {
	return true, nil
}

func (r *Validator) ConsolidationNeeded(_ ref.Ref) (bool, error) {
	return false, nil
}
