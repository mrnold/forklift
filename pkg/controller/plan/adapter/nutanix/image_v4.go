package nutanix

import (
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
	model "github.com/kubev2v/forklift/pkg/controller/provider/web/nutanix"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libweb "github.com/kubev2v/forklift/pkg/lib/inventory/web"
)

// imagesV4Path is Prism Central's Image Service (vmm) endpoint. Prism
// Central doesn't support disk-based image creation through the v3
// "image" kind used on Prism Element (see PreTransferActions); this v4
// endpoint is its replacement.
const imagesV4Path = "/api/vmm/v4.0/content/images"

// findImageV4ByName returns the v4 image entity with the given name.
// found is false if no such image exists yet. The v4 Image entity has no
// explicit lifecycle state field (unlike v3's status.state), so ready
// instead reports whether sizeBytes has been populated -- Nutanix's
// signal that the image's content has finished uploading.
func (r *Client) findImageV4ByName(name string) (entity map[string]interface{}, found, ready bool, err error) {
	requestURL := fmt.Sprintf("%s%s", r.URL, imagesV4Path)
	result := map[string]interface{}{}
	status, err := r.Get(requestURL, &result, libweb.Param{Key: "$filter", Value: fmt.Sprintf("name eq '%s'", name)})
	if err != nil {
		return nil, false, false, liberr.Wrap(err, "image", name)
	}
	if status != http.StatusOK {
		return nil, false, false, liberr.New("unexpected status listing images", "image", name, "status", status)
	}

	entities, ok := result["data"].([]interface{})
	if !ok || len(entities) == 0 {
		return nil, false, false, nil
	}
	e, ok := entities[0].(map[string]interface{})
	if !ok {
		return nil, false, false, nil
	}
	return e, true, imageV4SizeBytes(e) > 0, nil
}

// createImageV4 submits a v4 image creation request for a DISK_IMAGE
// sourced from the given VM disk. Creation is asynchronous; callers poll
// via findImageV4ByName.
func (r *Client) createImageV4(name, diskUUID string) error {
	body := map[string]interface{}{
		"name": name,
		"type": "DISK_IMAGE",
		"source": map[string]interface{}{
			"$objectType": "vmm.v4.content.VmDiskSource",
			"extId":       diskUUID,
		},
	}
	requestURL := fmt.Sprintf("%s%s", r.URL, imagesV4Path)
	status, err := r.Post(requestURL, body, nil)
	if err != nil {
		return liberr.Wrap(err, "disk", diskUUID, "image", name)
	}
	if status != http.StatusOK && status != http.StatusAccepted {
		return liberr.New("unexpected status creating image", "disk", diskUUID, "image", name, "status", status)
	}
	return nil
}

// deleteImageV4 deletes the v4 catalog image for a VM disk, if one
// exists. Errors are logged rather than returned; see Finalize.
func (r *Client) deleteImageV4(vmRef ref.Ref, diskUUID string) {
	name := migrationImageName(vmRef, diskUUID)
	entity, found, _, err := r.findImageV4ByName(name)
	if err != nil {
		r.Context.Log.Error(err, "Failed to look up image for cleanup", "vm", vmRef.String(), "image", name)
		return
	}
	if !found {
		return
	}
	extID := imageV4ExtID(entity)
	requestURL := fmt.Sprintf("%s%s/%s", r.URL, imagesV4Path, url.PathEscape(extID))
	if status, err := r.Delete(requestURL, nil); err != nil || (status != http.StatusOK && status != http.StatusAccepted) {
		r.Context.Log.Error(err, "Failed to delete temporary image", "vm", vmRef.String(), "image", extID, "status", status)
	}
}

// ensureImageV4 returns whether disk's v4 catalog image has finished
// uploading, creating it first if it doesn't exist yet. Mirrors
// ensureImage's create-if-missing/poll-by-name pattern, adapted to the v4
// Image entity's lack of an explicit error state: a creation failure
// (e.g. an invalid disk reference) won't be visible here, only as the
// image never appearing in subsequent polls.
func (r *Client) ensureImageV4(vmRef ref.Ref, disk model.Disk) (ready bool, err error) {
	name := migrationImageName(vmRef, disk.UUID)
	_, found, ready, err := r.findImageV4ByName(name)
	if err != nil {
		return false, err
	}
	if !found {
		return false, r.createImageV4(name, disk.UUID)
	}
	return ready, nil
}

// resolveImageV4DownloadURL performs Prism Central's redirect handshake
// for a v4 catalog image's file download: GET .../file responds with a
// 302 pointing at the actual download location, plus an X-Redirect-Token
// header that must be replayed as a Cookie on the follow-up request. A
// generic HTTP client -- like CDI's importer -- has no way to know to do
// that on its own, so this resolves it once, up front. The cookie is kept
// in a SecretExtraHeaders Secret (see Builder.centralHTTPSource) rather
// than baked into the DataVolume spec.
func (r *Client) resolveImageV4DownloadURL(extID string) (downloadURL, cookie string, err error) {
	requestURL := fmt.Sprintf("%s%s/%s/file", r.URL, imagesV4Path, url.PathEscape(extID))
	status, header, err := r.GetNoRedirect(requestURL)
	if err != nil {
		return "", "", err
	}
	if status != http.StatusFound {
		return "", "", liberr.New("unexpected status resolving image download", "image", extID, "status", status)
	}

	location := header.Get("Location")
	token := header.Get("X-Redirect-Token")
	if location == "" || token == "" {
		return "", "", liberr.New("missing redirect location or token resolving image download", "image", extID)
	}
	return location, token, nil
}

// preferClusterExternalURL rewrites a PE entity_download Location that
// points at a CVM address so it uses the cluster's external IP (VIP)
// instead. Prism Element certificates commonly list only the VIP in
// their SAN, while PC's redirect uses the CVM IP -- which would fail
// CDI's TLS verification. The VIP serves the same path with the same
// redirect cookie.
func (r *Client) preferClusterExternalURL(downloadURL string) string {
	parsed, err := url.Parse(downloadURL)
	if err != nil || parsed.Host == "" {
		return downloadURL
	}
	vip, err := r.clusterExternalIP()
	if err != nil || vip == "" {
		return downloadURL
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		host = parsed.Host
		port = ""
	}
	if host == vip {
		return downloadURL
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(vip, port)
	} else {
		parsed.Host = vip
	}
	return parsed.String()
}

// clusterExternalIP returns the Prism Element cluster VIP
// (status.resources.network.external_ip). PE certificates typically
// list only that VIP in their SAN, while PC redirects downloads to a
// CVM address -- so callers rewrite the Location host to this VIP.
// Prism Central's own pseudo-cluster has no external_ip and is skipped.
func (r *Client) clusterExternalIP() (string, error) {
	result, err := r.List("cluster", nil, 0, 20)
	if err != nil {
		return "", err
	}
	raw, _ := result["entities"].([]interface{})
	for _, item := range raw {
		entity, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		status, _ := entity["status"].(map[string]interface{})
		resources, _ := status["resources"].(map[string]interface{})
		netcfg, _ := resources["network"].(map[string]interface{})
		if ip, ok := netcfg["external_ip"].(string); ok && ip != "" {
			return ip, nil
		}
	}
	return "", nil
}

// imageV4SizeBytes reads sizeBytes from a v4 image entity.
func imageV4SizeBytes(entity map[string]interface{}) int64 {
	if size, ok := entity["sizeBytes"].(float64); ok {
		return int64(size)
	}
	return 0
}

// imageV4ExtID reads extId from a v4 image entity.
func imageV4ExtID(entity map[string]interface{}) string {
	if extID, ok := entity["extId"].(string); ok {
		return extID
	}
	return ""
}
