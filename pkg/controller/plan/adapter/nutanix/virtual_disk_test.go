package nutanix

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestVirtualDiskListURL_PrismElementUsesClusterVIP(t *testing.T) {
	got := virtualDiskListURL("https://pe.example.com:9440", testCluster(), true)
	want := "https://10.0.0.50:9440/PrismGateway/services/rest/v2.0/virtual_disks/"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVirtualDiskListURL_PrismCentralProxiesToCluster(t *testing.T) {
	got := virtualDiskListURL("https://pc.example.com:9440/", testCluster(), false)

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("unparseable URL %q: %v", got, err)
	}
	if parsed.Host != "pc.example.com:9440" {
		t.Errorf("expected request to Prism Central, got host %q", parsed.Host)
	}
	if parsed.Path != "/PrismGateway/services/rest/v2.0/virtual_disks/" {
		t.Errorf("unexpected path %q", parsed.Path)
	}
	if v := parsed.Query().Get("proxyClusterUuid"); v != testCluster().ID {
		t.Errorf("proxyClusterUuid = %q, want %q", v, testCluster().ID)
	}
}

func TestResolveVmDiskUUIDs_MapsDeviceToVmDisk(t *testing.T) {
	const body = `{"metadata":{"grand_total_entities":2,"end_index":2},"entities":[
		{"uuid":"vmdisk-a","device_uuid":"device-a"},
		{"uuid":"vmdisk-b","device_uuid":"device-b"}
	]}`
	var req *http.Request
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/nutanix/v3/clusters/list":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"entities":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/PrismGateway/services/rest/v2.0/virtual_disks/":
			req = r.Clone(r.Context())
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	builder, client := catalogImageTestSetup(t, server)
	got, err := builder.resolveVmDiskUUIDs(client, virtualDiskListURL(server.URL, testCluster(), false), []string{"device-b", "device-a"})
	if err != nil {
		t.Fatalf("resolveVmDiskUUIDs: %v", err)
	}
	if got["device-a"] != "vmdisk-a" || got["device-b"] != "vmdisk-b" {
		t.Fatalf("got %#v", got)
	}
	if req == nil {
		t.Fatal("virtual_disks was not requested")
	}
	if v := req.URL.Query().Get("proxyClusterUuid"); v != testCluster().ID {
		t.Errorf("request proxyClusterUuid = %q, want %q", v, testCluster().ID)
	}
	if v := req.URL.Query().Get("page"); v != "1" {
		t.Errorf("page = %q, want 1", v)
	}
}

func TestResolveVmDiskUUIDs_MissingDevice(t *testing.T) {
	const body = `{"metadata":{"grand_total_entities":1,"end_index":1},"entities":[
		{"uuid":"vmdisk-a","device_uuid":"device-a"}
	]}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/nutanix/v3/clusters/list":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"entities":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/PrismGateway/services/rest/v2.0/virtual_disks/":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	builder, client := catalogImageTestSetup(t, server)
	_, err := builder.resolveVmDiskUUIDs(client, virtualDiskListURL(server.URL, testCluster(), false), []string{"device-missing"})
	if err == nil {
		t.Fatal("expected error for missing device uuid")
	}
}
