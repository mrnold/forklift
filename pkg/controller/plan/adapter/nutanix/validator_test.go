package nutanix

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
	plancontext "github.com/kubev2v/forklift/pkg/controller/plan/context"
	provmodel "github.com/kubev2v/forklift/pkg/controller/provider/model/nutanix"
	model "github.com/kubev2v/forklift/pkg/controller/provider/web/nutanix"
)

func TestNetworksMapped(t *testing.T) {
	vm := &model.VM{}
	vm.ID = "vm-1"
	vm.NICs = []provmodel.NIC{
		{SubnetUUID: "net-adv", SubnetName: "Advanced-VLAN"},
		{SubnetUUID: "net-basic", SubnetName: "Basic-VLAN"},
	}

	v := &Validator{
		Context: &plancontext.Context{
			Source: plancontext.Source{Inventory: &fakeInventory{vm: vm}},
		},
	}
	v.Map.Network = &api.NetworkMap{
		Spec: api.NetworkMapSpec{
			Map: []api.NetworkPair{
				{Source: api.NetworkSourceRef{Ref: ref.Ref{ID: "net-adv"}}},
				{Source: api.NetworkSourceRef{Ref: ref.Ref{ID: "net-basic"}}},
			},
		},
	}

	ok, err := v.NetworksMapped(ref.Ref{ID: "vm-1"})
	if err != nil || !ok {
		t.Fatalf("expected mapped, ok=%v err=%v", ok, err)
	}

	v.Map.Network.Spec.Map = v.Map.Network.Spec.Map[:1]
	ok, err = v.NetworksMapped(ref.Ref{ID: "vm-1"})
	if err != nil || ok {
		t.Fatalf("expected unmapped, ok=%v err=%v", ok, err)
	}
}

func TestNICNetworkRefs(t *testing.T) {
	vm := &model.VM{}
	vm.ID = "vm-1"
	vm.NICs = []provmodel.NIC{
		{SubnetUUID: "net-adv", SubnetName: "Advanced-VLAN"},
		{SubnetUUID: "", SubnetName: ""},
	}

	v := &Validator{
		Context: &plancontext.Context{
			Source: plancontext.Source{Inventory: &fakeInventory{vm: vm}},
		},
	}

	refs, err := v.NICNetworkRefs(ref.Ref{ID: "vm-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].ID != "net-adv" || refs[0].Name != "Advanced-VLAN" {
		t.Fatalf("unexpected refs: %+v", refs)
	}
}
