package nutanix

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	planapi "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/plan"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
	plancontext "github.com/kubev2v/forklift/pkg/controller/plan/context"
	webbase "github.com/kubev2v/forklift/pkg/controller/provider/web/base"
	model "github.com/kubev2v/forklift/pkg/controller/provider/web/nutanix"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	core "k8s.io/api/core/v1"
)

// errNotImplemented is returned by fakeInventory methods that aren't
// exercised by these tests, so callers get a clear error instead of a
// silent nil/nil.
var errNotImplemented = fmt.Errorf("not implemented by fakeInventory")

// fakeInventory is a minimal web.Client stub that only implements Find,
// returning a fixed VM. The other methods aren't exercised by these tests.
type fakeInventory struct {
	vm *model.VM
}

func (f *fakeInventory) Finder() webbase.Finder { return nil }
func (f *fakeInventory) Get(_ interface{}, _ string) error {
	return nil
}
func (f *fakeInventory) List(_ interface{}, _ ...webbase.Param) error {
	return nil
}
func (f *fakeInventory) Watch(_ interface{}, _ webbase.EventHandler) (*webbase.Watch, error) {
	return nil, errNotImplemented
}
func (f *fakeInventory) Find(resource interface{}, _ webbase.Ref) error {
	if vm, ok := resource.(*model.VM); ok {
		*vm = *f.vm
	}
	return nil
}
func (f *fakeInventory) VM(_ *webbase.Ref) (interface{}, error) { return f.vm, nil }
func (f *fakeInventory) Workload(_ *webbase.Ref) (interface{}, error) {
	return nil, errNotImplemented
}
func (f *fakeInventory) Network(_ *webbase.Ref) (interface{}, error) {
	return nil, errNotImplemented
}
func (f *fakeInventory) Storage(_ *webbase.Ref) (interface{}, error) {
	return nil, errNotImplemented
}
func (f *fakeInventory) Host(_ *webbase.Ref) (interface{}, error) {
	return nil, errNotImplemented
}

func newTestClient(url string) *Client {
	return newTestClientWithInventory(url, nil)
}

func newTestClientWithInventory(url string, vm *model.VM) *Client {
	return &Client{
		Context: &plancontext.Context{
			Source: plancontext.Source{
				Provider: &api.Provider{Spec: api.ProviderSpec{URL: url}},
				Secret: &core.Secret{
					Data: map[string][]byte{
						"user":     []byte("admin"),
						"password": []byte("password"),
					},
				},
				Inventory: &fakeInventory{vm: vm},
			},
			Log: logging.WithName("test"),
		},
	}
}

// TestConnect verifies connect() picks up the provider URL and secret from
// the plan context and authenticates against the Nutanix API.
func TestConnect(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") == "" {
			t.Error("expected Authorization header")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"entities":[]}`))
	}))
	defer server.Close()

	client := newTestClient(server.URL)

	if err := client.connect(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requests == 0 {
		t.Fatal("expected connect() to issue at least one authenticated request")
	}
	if client.URL != server.URL {
		t.Fatalf("expected client URL to be %s, got %s", server.URL, client.URL)
	}
}

// TestConnect_FailsOnUnauthorized verifies connect() surfaces an error when
// the connectivity probe is rejected.
func TestConnect_FailsOnUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := newTestClient(server.URL)

	if err := client.connect(); err == nil {
		t.Fatal("expected an error when the connectivity probe returns 401")
	}
}

// vmEntity builds a minimal v3 VM entity body with the given power state.
func vmEntity(uuid, powerState string) map[string]interface{} {
	return map[string]interface{}{
		"api_version": "3.1",
		"metadata":    map[string]interface{}{"uuid": uuid},
		"spec": map[string]interface{}{
			"name":      "test-vm",
			"resources": map[string]interface{}{"power_state": powerState},
		},
		"status": map[string]interface{}{
			"resources": map[string]interface{}{"power_state": powerState},
		},
	}
}

// newPowerTestServer serves the connectivity probe plus GET/PUT for a
// single VM entity, tracking PUT bodies for assertions. The entity's
// power state is updated in place when a PUT is received, mimicking
// Nutanix applying the change.
func newPowerTestServer(t *testing.T, entity map[string]interface{}) (server *httptest.Server, puts *[]map[string]interface{}) {
	t.Helper()
	var putBodies []map[string]interface{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			// Connectivity probe (clusters/list).
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"entities":[]}`))
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(entity)
		case http.MethodPut:
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			putBodies = append(putBodies, body)
			if spec, ok := body["spec"].(map[string]interface{}); ok {
				if resources, ok := spec["resources"].(map[string]interface{}); ok {
					entity["spec"].(map[string]interface{})["resources"].(map[string]interface{})["power_state"] = resources["power_state"]
					entity["status"].(map[string]interface{})["resources"].(map[string]interface{})["power_state"] = resources["power_state"]
				}
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	return server, &putBodies
}

// newConnectedTestClient builds a plan adapter Client and connects it
// against the given server, as the Adapter would before issuing requests.
func newConnectedTestClient(t *testing.T, url string) *Client {
	t.Helper()
	client := newTestClient(url)
	if err := client.connect(); err != nil {
		t.Fatalf("unexpected error connecting: %v", err)
	}
	return client
}

func TestPowerState(t *testing.T) {
	cases := []struct {
		raw  string
		want planapi.VMPowerState
	}{
		{powerStateOn, planapi.VMPowerStateOn},
		{powerStateOff, planapi.VMPowerStateOff},
		{"", planapi.VMPowerStateUnknown},
	}
	for _, c := range cases {
		server, _ := newPowerTestServer(t, vmEntity("uuid-1", c.raw))
		defer server.Close()

		client := newConnectedTestClient(t, server.URL)
		state, err := client.PowerState(ref.Ref{ID: "uuid-1"})
		if err != nil {
			t.Fatalf("unexpected error for raw state %q: %v", c.raw, err)
		}
		if state != c.want {
			t.Fatalf("raw state %q: expected %s, got %s", c.raw, c.want, state)
		}
	}
}

func TestPoweredOff(t *testing.T) {
	server, _ := newPowerTestServer(t, vmEntity("uuid-1", powerStateOff))
	defer server.Close()

	client := newConnectedTestClient(t, server.URL)
	off, err := client.PoweredOff(ref.Ref{ID: "uuid-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !off {
		t.Fatal("expected PoweredOff to be true when power_state is OFF")
	}
}

// TestPowerOff_SkipsPutWhenAlreadyOff verifies PowerOff is a no-op (no PUT
// issued) when the VM is already off.
func TestPowerOff_SkipsPutWhenAlreadyOff(t *testing.T) {
	server, puts := newPowerTestServer(t, vmEntity("uuid-1", powerStateOff))
	defer server.Close()

	client := newConnectedTestClient(t, server.URL)
	if err := client.PowerOff(ref.Ref{ID: "uuid-1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*puts) != 0 {
		t.Fatalf("expected no PUT requests, got %d", len(*puts))
	}
}

// TestPowerOff_SubmitsPut verifies PowerOff submits a PUT with
// power_state=OFF when the VM is currently on, and that the transition is
// reflected on a subsequent PoweredOff check.
func TestPowerOff_SubmitsPut(t *testing.T) {
	server, puts := newPowerTestServer(t, vmEntity("uuid-1", powerStateOn))
	defer server.Close()

	client := newConnectedTestClient(t, server.URL)
	if err := client.PowerOff(ref.Ref{ID: "uuid-1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*puts) != 1 {
		t.Fatalf("expected exactly one PUT request, got %d", len(*puts))
	}
	spec, ok := (*puts)[0]["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("expected PUT body to include spec")
	}
	resources, ok := spec["resources"].(map[string]interface{})
	if !ok || resources["power_state"] != powerStateOff {
		t.Fatalf("expected PUT spec.resources.power_state to be %q, got %v", powerStateOff, resources["power_state"])
	}

	off, err := client.PoweredOff(ref.Ref{ID: "uuid-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !off {
		t.Fatal("expected PoweredOff to be true after PowerOff")
	}
}

// TestPowerOn_SkipsPutWhenAlreadyOn mirrors TestPowerOff_SkipsPutWhenAlreadyOff.
func TestPowerOn_SkipsPutWhenAlreadyOn(t *testing.T) {
	server, puts := newPowerTestServer(t, vmEntity("uuid-1", powerStateOn))
	defer server.Close()

	client := newConnectedTestClient(t, server.URL)
	if err := client.PowerOn(ref.Ref{ID: "uuid-1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*puts) != 0 {
		t.Fatalf("expected no PUT requests, got %d", len(*puts))
	}
}

// newImageTestServer serves the connectivity probe plus a minimal v3 image
// list/create/delete implementation backed by an in-memory store keyed by
// UUID, for testing the catalog image lifecycle used by PreTransferActions
// and Finalize.
func newImageTestServer(t *testing.T, images map[string]map[string]interface{}) *httptest.Server {
	t.Helper()
	nextID := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/clusters/list"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"entities":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/images/list"):
			entities := make([]map[string]interface{}, 0, len(images))
			for _, e := range images {
				entities = append(entities, e)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"entities": entities,
				"metadata": map[string]interface{}{"total_matches": len(entities)},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/images"):
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			nextID++
			uuid := fmt.Sprintf("image-%d", nextID)
			images[uuid] = map[string]interface{}{
				"metadata": map[string]interface{}{"uuid": uuid},
				"spec":     body["spec"],
				"status":   map[string]interface{}{"state": imageStatePending},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodDelete:
			delete(images, path.Base(r.URL.Path))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
}

func TestFindImageByName_NotFound(t *testing.T) {
	server := newImageTestServer(t, map[string]map[string]interface{}{})
	defer server.Close()

	client := newConnectedTestClient(t, server.URL)
	_, found, err := client.findImageByName("missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected image not to be found")
	}
}

func TestFindImageByName_Found(t *testing.T) {
	images := map[string]map[string]interface{}{
		"image-1": {
			"metadata": map[string]interface{}{"uuid": "image-1"},
			"spec":     map[string]interface{}{"name": "forklift-migration-vm-1-disk-1"},
			"status":   map[string]interface{}{"state": imageStateComplete},
		},
	}
	server := newImageTestServer(t, images)
	defer server.Close()

	client := newConnectedTestClient(t, server.URL)
	entity, found, err := client.findImageByName("forklift-migration-vm-1-disk-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected image to be found")
	}
	if imageUUID(entity) != "image-1" {
		t.Fatalf("expected uuid image-1, got %s", imageUUID(entity))
	}
	if imageState(entity) != imageStateComplete {
		t.Fatalf("expected state %s, got %s", imageStateComplete, imageState(entity))
	}
}

func TestCreateImage_PostsExpectedBody(t *testing.T) {
	images := map[string]map[string]interface{}{}
	server := newImageTestServer(t, images)
	defer server.Close()

	client := newConnectedTestClient(t, server.URL)
	if err := client.createImage("forklift-migration-vm-1-disk-1", "disk-uuid-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("expected exactly one image to be created, got %d", len(images))
	}
	for _, entity := range images {
		spec, _ := entity["spec"].(map[string]interface{})
		if spec["name"] != "forklift-migration-vm-1-disk-1" {
			t.Fatalf("unexpected image name: %v", spec["name"])
		}
		resources, _ := spec["resources"].(map[string]interface{})
		if resources["image_type"] != "DISK_IMAGE" {
			t.Fatalf("expected image_type DISK_IMAGE, got %v", resources["image_type"])
		}
		dataSource, _ := resources["data_source_reference"].(map[string]interface{})
		if dataSource["kind"] != "vm_disk" || dataSource["uuid"] != "disk-uuid-1" {
			t.Fatalf("unexpected data_source_reference: %v", dataSource)
		}
	}
}

// TestPreTransferActions_CreatesImagesAndWaitsForComplete verifies
// PreTransferActions creates one image per non-CDROM disk, reports not
// ready while any image is still pending, and reports ready once every
// image has transitioned to COMPLETE -- without needing to change the VM
// disk list between polls (as the migration controller reconciles).
func TestPreTransferActions_CreatesImagesAndWaitsForComplete(t *testing.T) {
	vm := &model.VM{VM1: model.VM1{
		Disks: []model.Disk{
			{UUID: "disk-1", DiskSizeBytes: 1024},
			{UUID: "cdrom-1", IsCdrom: true},
		},
	}}
	images := map[string]map[string]interface{}{}
	server := newImageTestServer(t, images)
	defer server.Close()

	client := newTestClientWithInventory(server.URL, vm)
	if err := client.connect(); err != nil {
		t.Fatalf("unexpected error connecting: %v", err)
	}

	vmRef := ref.Ref{ID: "vm-1"}
	ready, err := client.PreTransferActions(vmRef)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ready {
		t.Fatal("expected ready=false on first call, before the image exists")
	}
	if len(images) != 1 {
		t.Fatalf("expected exactly one image to be created (CDROM should be skipped), got %d", len(images))
	}

	ready, err = client.PreTransferActions(vmRef)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ready {
		t.Fatal("expected ready=false while the image is still PENDING")
	}

	for _, entity := range images {
		entity["status"].(map[string]interface{})["state"] = imageStateComplete
	}

	ready, err = client.PreTransferActions(vmRef)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ready {
		t.Fatal("expected ready=true once the image is COMPLETE")
	}
}

// TestPreTransferActions_ErrorsOnImageError verifies a failed image
// creation surfaces as an error rather than looping forever.
func TestPreTransferActions_ErrorsOnImageError(t *testing.T) {
	vm := &model.VM{VM1: model.VM1{Disks: []model.Disk{{UUID: "disk-1"}}}}
	images := map[string]map[string]interface{}{
		"image-1": {
			"metadata": map[string]interface{}{"uuid": "image-1"},
			"spec":     map[string]interface{}{"name": "forklift-migration-vm-1-disk-1"},
			"status":   map[string]interface{}{"state": imageStateError},
		},
	}
	server := newImageTestServer(t, images)
	defer server.Close()

	client := newTestClientWithInventory(server.URL, vm)
	if err := client.connect(); err != nil {
		t.Fatalf("unexpected error connecting: %v", err)
	}

	if _, err := client.PreTransferActions(ref.Ref{ID: "vm-1"}); err == nil {
		t.Fatal("expected an error when the catalog image failed to create")
	}
}

// TestFinalize_DeletesImages verifies Finalize deletes each non-CDROM
// disk's catalog image and leaves unrelated images untouched.
func TestFinalize_DeletesImages(t *testing.T) {
	vm := &model.VM{VM1: model.VM1{
		Disks: []model.Disk{
			{UUID: "disk-1"},
			{UUID: "cdrom-1", IsCdrom: true},
		},
	}}
	images := map[string]map[string]interface{}{
		"image-1": {
			"metadata": map[string]interface{}{"uuid": "image-1"},
			"spec":     map[string]interface{}{"name": migrationImageName(ref.Ref{ID: "vm-1"}, "disk-1")},
			"status":   map[string]interface{}{"state": imageStateComplete},
		},
		"image-unrelated": {
			"metadata": map[string]interface{}{"uuid": "image-unrelated"},
			"spec":     map[string]interface{}{"name": "unrelated-image"},
			"status":   map[string]interface{}{"state": imageStateComplete},
		},
	}
	server := newImageTestServer(t, images)
	defer server.Close()

	client := newTestClientWithInventory(server.URL, vm)
	if err := client.connect(); err != nil {
		t.Fatalf("unexpected error connecting: %v", err)
	}

	client.Finalize([]*planapi.VMStatus{{VM: planapi.VM{Ref: ref.Ref{ID: "vm-1"}}}}, "test-plan")

	if _, found := images["image-1"]; found {
		t.Fatal("expected the VM's catalog image to be deleted")
	}
	if _, found := images["image-unrelated"]; !found {
		t.Fatal("expected an unrelated image to be left alone")
	}
}
