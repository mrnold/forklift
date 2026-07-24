package nutanix

import (
	"fmt"
	"net/http"

	planapi "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/plan"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
	plancontext "github.com/kubev2v/forklift/pkg/controller/plan/context"
	"github.com/kubev2v/forklift/pkg/controller/plan/util"
	libclient "github.com/kubev2v/forklift/pkg/lib/client/nutanix"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	cdi "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"

	model "github.com/kubev2v/forklift/pkg/controller/provider/web/nutanix"
)

// Nutanix v3 VM power states (spec/status.resources.power_state).
const (
	powerStateOn  = "ON"
	powerStateOff = "OFF"
)

// Nutanix v3 image entity states (status.state).
const (
	imageStatePending  = "PENDING"
	imageStateComplete = "COMPLETE"
	imageStateError    = "ERROR"
)

// imagePageSize bounds how many images are listed per page when searching
// for a migration's temporary catalog images by name.
const imagePageSize = 50

// Client performs source-side Nutanix migration actions. It embeds the
// shared REST client (pkg/lib/client/nutanix) also used by the inventory
// collector, so Get/Post/Put/Delete/Connect are available directly on r.
type Client struct {
	libclient.Client
	Context *plancontext.Context
}

func (r *Client) connect() error {
	r.URL = r.Context.Source.Provider.Spec.URL
	r.Secret = r.Context.Source.Secret
	r.Log = r.Context.Log.WithName("client")
	_, err := r.Connect()
	return err
}

func (r *Client) Close() {}

// Finalize deletes the temporary catalog images created by
// PreTransferActions for each migrated VM, once the migration has finished
// with them. Deletion failures are logged rather than returned, since
// finalization runs after the migration's outcome is already decided and a
// leftover image shouldn't be reported as a migration failure.
func (r *Client) Finalize(vms []*planapi.VMStatus, _ string) {
	for _, vm := range vms {
		disks, err := r.vmDisks(vm.Ref)
		if err != nil {
			r.Context.Log.Error(err, "Failed to look up VM disks for image cleanup", "vm", vm.String())
			continue
		}
		for _, disk := range disks {
			if disk.IsCdrom {
				continue
			}
			r.deleteImage(vm.Ref, disk.UUID)
		}
	}
}

func (r *Client) DetachDisks(_ ref.Ref) error {
	return nil
}

// PowerState returns the VM's current power state, read live from the
// Nutanix v3 API (not the, possibly stale, inventory cache).
func (r *Client) PowerState(vmRef ref.Ref) (planapi.VMPowerState, error) {
	entity, err := r.getVM(vmRef)
	if err != nil {
		return planapi.VMPowerStateUnknown, err
	}
	switch vmPowerState(entity) {
	case powerStateOn:
		return planapi.VMPowerStateOn, nil
	case powerStateOff:
		return planapi.VMPowerStateOff, nil
	default:
		return planapi.VMPowerStateUnknown, nil
	}
}

// PowerOn powers on the VM, unless it is already on.
func (r *Client) PowerOn(vmRef ref.Ref) error {
	state, err := r.PowerState(vmRef)
	if err != nil {
		return err
	}
	if state == planapi.VMPowerStateOn {
		return nil
	}
	return r.setPowerState(vmRef, powerStateOn)
}

// PowerOff powers off the VM, unless it is already off. Nutanix AHV
// performs a hard power-off (ACPI shutdown isn't modeled by this API);
// that's acceptable here since PowerOff is only used ahead of transferring
// disks, once the VM's data no longer needs to change.
func (r *Client) PowerOff(vmRef ref.Ref) error {
	state, err := r.PowerState(vmRef)
	if err != nil {
		return err
	}
	if state == planapi.VMPowerStateOff {
		return nil
	}
	return r.setPowerState(vmRef, powerStateOff)
}

// PoweredOff reports whether the VM has finished powering off.
func (r *Client) PoweredOff(vmRef ref.Ref) (bool, error) {
	state, err := r.PowerState(vmRef)
	if err != nil {
		return false, err
	}
	return state == planapi.VMPowerStateOff, nil
}

// getVM fetches the full v3 VM entity (metadata/spec/status) by UUID.
func (r *Client) getVM(vmRef ref.Ref) (entity map[string]interface{}, err error) {
	url := fmt.Sprintf("%s/api/nutanix/v3/vms/%s", r.URL, vmRef.ID)
	entity = map[string]interface{}{}
	status, err := r.Get(url, &entity)
	if err != nil {
		return nil, liberr.Wrap(err, "vm", vmRef.String())
	}
	if status != http.StatusOK {
		return nil, liberr.New("unexpected status fetching VM", "vm", vmRef.String(), "status", status)
	}
	return entity, nil
}

// setPowerState submits a v3 PUT that transitions the VM to the given
// power state. Nutanix v3's update pattern requires sending the full spec
// back (as returned by GET, with the desired field changed), not a partial
// patch.
func (r *Client) setPowerState(vmRef ref.Ref, state string) error {
	entity, err := r.getVM(vmRef)
	if err != nil {
		return err
	}
	spec, ok := entity["spec"].(map[string]interface{})
	if !ok {
		return liberr.New("malformed VM entity: missing spec", "vm", vmRef.String())
	}
	resources, ok := spec["resources"].(map[string]interface{})
	if !ok {
		return liberr.New("malformed VM entity: missing spec.resources", "vm", vmRef.String())
	}
	resources["power_state"] = state

	body := map[string]interface{}{
		"metadata": entity["metadata"],
		"spec":     spec,
	}
	if apiVersion, found := entity["api_version"]; found {
		body["api_version"] = apiVersion
	}

	url := fmt.Sprintf("%s/api/nutanix/v3/vms/%s", r.URL, vmRef.ID)
	status, err := r.Put(url, body, nil)
	if err != nil {
		return liberr.Wrap(err, "vm", vmRef.String(), "state", state)
	}
	if status != http.StatusOK && status != http.StatusAccepted {
		return liberr.New("unexpected status setting power state", "vm", vmRef.String(), "state", state, "status", status)
	}
	return nil
}

// vmPowerState extracts resources.power_state from a v3 VM entity,
// preferring the observed status over the desired spec.
func vmPowerState(entity map[string]interface{}) string {
	if status, ok := entity["status"].(map[string]interface{}); ok {
		if resources, ok := status["resources"].(map[string]interface{}); ok {
			if state, ok := resources["power_state"].(string); ok && state != "" {
				return state
			}
		}
	}
	if spec, ok := entity["spec"].(map[string]interface{}); ok {
		if resources, ok := spec["resources"].(map[string]interface{}); ok {
			if state, ok := resources["power_state"].(string); ok {
				return state
			}
		}
	}
	return ""
}

func (r *Client) CreateSnapshot(_ ref.Ref, _ util.HostsFunc) (string, string, error) {
	return "", "", nil
}

func (r *Client) RemoveSnapshot(_ ref.Ref, _ string, _ util.HostsFunc) (string, error) {
	return "", nil
}

func (r *Client) CheckSnapshotReady(_ ref.Ref, _ planapi.Precopy, _ util.HostsFunc) (bool, string, error) {
	return true, "", nil
}

func (r *Client) CheckSnapshotRemove(_ ref.Ref, _ planapi.Precopy, _ util.HostsFunc) (bool, error) {
	return true, nil
}

func (r *Client) SetCheckpoints(_ ref.Ref, _ []planapi.Precopy, _ []cdi.DataVolume, _ bool, _ util.HostsFunc) error {
	return nil
}

func (r *Client) GetSnapshotDeltas(_ ref.Ref, _ string, _ util.HostsFunc) (s map[string]string, err error) {
	return
}

// PreTransferActions creates a Nutanix Image Service catalog image from
// each of the VM's non-CDROM disks, so their contents become downloadable
// over HTTP for Builder.DataVolumes (GET /images/{uuid}/file). Returns
// ready=true only once every disk's image has finished creating.
func (r *Client) PreTransferActions(vmRef ref.Ref) (ready bool, err error) {
	disks, err := r.vmDisks(vmRef)
	if err != nil {
		return false, err
	}

	ready = true
	for _, disk := range disks {
		if disk.IsCdrom {
			continue
		}
		diskReady, imgErr := r.ensureImage(vmRef, disk)
		if imgErr != nil {
			return false, imgErr
		}
		ready = ready && diskReady
	}
	return ready, nil
}

// vmDisks looks up a VM's disks from inventory.
func (r *Client) vmDisks(vmRef ref.Ref) ([]model.Disk, error) {
	vm := &model.VM{}
	if err := r.Context.Source.Inventory.Find(vm, vmRef); err != nil {
		return nil, liberr.Wrap(err, "vm", vmRef.String())
	}
	return vm.Disks, nil
}

// ensureImage returns whether disk's catalog image has finished creating,
// creating it first if it doesn't exist yet.
func (r *Client) ensureImage(vmRef ref.Ref, disk model.Disk) (ready bool, err error) {
	name := migrationImageName(vmRef, disk.UUID)
	entity, found, err := r.findImageByName(name)
	if err != nil {
		return false, err
	}
	if !found {
		return false, r.createImage(name, disk.UUID)
	}
	switch imageState(entity) {
	case imageStateComplete:
		return true, nil
	case imageStateError:
		return false, liberr.New("Nutanix image creation failed", "vm", vmRef.String(), "disk", disk.UUID, "image", name)
	default:
		return false, nil
	}
}

// findImageByName returns the v3 image entity with the given spec.name.
// found is false if no such image exists yet. Nutanix doesn't guarantee
// image names are unique, but names generated by migrationImageName are,
// so the first match is authoritative.
func (r *Client) findImageByName(name string) (entity map[string]interface{}, found bool, err error) {
	entities, err := r.ListAll("image", nil, imagePageSize)
	if err != nil {
		return nil, false, err
	}
	for _, e := range entities {
		if imageName(e) == name {
			return e, true, nil
		}
	}
	return nil, false, nil
}

// createImage submits a v3 image creation request for a DISK_IMAGE sourced
// from the given VM disk. Creation is asynchronous; callers poll via
// findImageByName/imageState.
func (r *Client) createImage(name, diskUUID string) error {
	body := map[string]interface{}{
		"api_version": "3.1.0",
		"metadata": map[string]interface{}{
			"kind": "image",
		},
		"spec": map[string]interface{}{
			"name": name,
			"resources": map[string]interface{}{
				"image_type": "DISK_IMAGE",
				"data_source_reference": map[string]interface{}{
					"kind": "vm_disk",
					"uuid": diskUUID,
				},
			},
		},
	}
	url := fmt.Sprintf("%s/api/nutanix/v3/images", r.URL)
	status, err := r.Post(url, body, nil)
	if err != nil {
		return liberr.Wrap(err, "disk", diskUUID, "image", name)
	}
	if status != http.StatusOK && status != http.StatusAccepted {
		return liberr.New("unexpected status creating image", "disk", diskUUID, "image", name, "status", status)
	}
	return nil
}

// deleteImage deletes the catalog image for a VM disk, if one exists.
// Errors are logged rather than returned; see Finalize.
func (r *Client) deleteImage(vmRef ref.Ref, diskUUID string) {
	name := migrationImageName(vmRef, diskUUID)
	entity, found, err := r.findImageByName(name)
	if err != nil {
		r.Context.Log.Error(err, "Failed to look up image for cleanup", "vm", vmRef.String(), "image", name)
		return
	}
	if !found {
		return
	}
	uuid := imageUUID(entity)
	url := fmt.Sprintf("%s/api/nutanix/v3/images/%s", r.URL, uuid)
	if status, err := r.Delete(url, nil); err != nil || (status != http.StatusOK && status != http.StatusAccepted) {
		r.Context.Log.Error(err, "Failed to delete temporary image", "vm", vmRef.String(), "image", uuid, "status", status)
	}
}

// migrationImageName is a deterministic, discoverable name for the
// temporary catalog image created from a VM disk. It's derived from the VM
// and disk UUIDs (rather than tracked as in-memory state) so that
// PreTransferActions, Builder.DataVolumes, and Finalize -- which may each
// run against separate Client instances -- can all find the same image by
// listing rather than sharing runtime state.
func migrationImageName(vmRef ref.Ref, diskUUID string) string {
	return fmt.Sprintf("forklift-migration-%s-%s", vmRef.ID, diskUUID)
}

// imageName reads spec.name from a v3 image entity.
func imageName(entity map[string]interface{}) string {
	if spec, ok := entity["spec"].(map[string]interface{}); ok {
		if name, ok := spec["name"].(string); ok {
			return name
		}
	}
	return ""
}

// imageState reads status.state from a v3 image entity.
func imageState(entity map[string]interface{}) string {
	if status, ok := entity["status"].(map[string]interface{}); ok {
		if state, ok := status["state"].(string); ok {
			return state
		}
	}
	return ""
}

// imageUUID reads metadata.uuid from a v3 image entity.
func imageUUID(entity map[string]interface{}) string {
	if metadata, ok := entity["metadata"].(map[string]interface{}); ok {
		if uuid, ok := metadata["uuid"].(string); ok {
			return uuid
		}
	}
	return ""
}
