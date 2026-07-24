package nutanix

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	nutanixweb "github.com/kubev2v/forklift/pkg/lib/client/nutanix"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libweb "github.com/kubev2v/forklift/pkg/lib/inventory/web"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	core "k8s.io/api/core/v1"
)

// Settings
const (
	// Connect retry delay.
	RetryDelay = time.Second * 5
	// Connection timeout.
	ConnectionTimeout = nutanixweb.ConnectionTimeout
)

// Per-request page sizes for v3 list endpoints. listAll() pages through as
// many requests as needed regardless of these values; they only bound how
// many entities are requested per page.
const (
	clusterPageSize = 100
	hostPageSize    = 1000
	vmPageSize      = 100
	subnetPageSize  = 500
	imagePageSize   = 500
	// Per-request page sizes for v4 "config"/"content" namespace endpoints.
	// listAllV4() pages through as many requests as needed regardless of
	// these values; the v4 image endpoint additionally caps $limit at 100.
	storageContainerV4PageSize = 100
	imageV4PageSize            = 100
)

// Client wraps the shared pkg/lib/client/nutanix REST client with the
// collector-specific concerns: Prism mode resolution/caching and the
// cluster-scoped, entity-specific list methods used by the collector.
// The generic HTTP/auth/pagination plumbing lives in the shared client so
// the migration plan adapter (pkg/controller/plan/adapter/nutanix) can
// reuse it without depending on this package.
type Client struct {
	// Base URL (e.g., https://prism-central:9440)
	url string
	// Secret containing credentials
	secret *core.Secret
	// Provider settings (prismType, clusterUuid, ...)
	settings map[string]string
	// Client timeout
	clientTimeout time.Duration
	// Logger
	log logging.LevelLogger
	// Resolved Prism endpoint configuration.
	prism PrismConfig
	// Whether prism config has been resolved.
	prismResolved bool

	// Shared REST client (connect/auth/get/post/pagination).
	web nutanixweb.Client
}

// ensureWebClient populates the shared REST client from this client's
// fields the first time it's needed. It never resets an already-populated
// r.web, so a live connection (and its TLS-configured transport) survives
// repeated calls.
func (r *Client) ensureWebClient() {
	if r.web.URL == "" {
		r.web = nutanixweb.Client{
			URL:     r.url,
			Secret:  r.secret,
			Timeout: r.clientTimeout,
			Log:     r.log,
		}
	}
}

// Connect and authenticate with Nutanix Prism, then resolve the Prism
// mode (Central vs Element) for this provider.
func (r *Client) connect() (status int, err error) {
	r.ensureWebClient()

	status, err = r.web.Connect()
	if err != nil {
		return
	}
	// Pick up the trimmed (no trailing slash) URL.
	r.url = r.web.URL

	if err = r.ensurePrismConfig(); err != nil {
		return status, err
	}

	r.log.Info("Successfully connected to Nutanix",
		"url", r.url,
		"prismMode", r.prism.Mode)

	return status, nil
}

// GET request
func (r *Client) get(url string, object interface{}, params ...libweb.Param) (status int, err error) {
	status, err = r.connect()
	if err != nil {
		return
	}
	return r.web.Get(url, object, params...)
}

// POST request (Nutanix uses POST for list operations)
func (r *Client) post(url string, body interface{}, object interface{}) (status int, err error) {
	status, err = r.connect()
	if err != nil {
		return
	}
	return r.web.Post(url, body, object)
}

// listAllV3 pages through a v3 list endpoint and unmarshals entities directly
// into typed structs, following the response's total_matches across pages.
func listAllV3[T any](r *Client, resourceKind string, filter map[string]interface{}, pageSize int) ([]T, error) {
	offset := 0
	entities := make([]T, 0)

	for {
		body := map[string]interface{}{
			"kind":   resourceKind,
			"length": pageSize,
			"offset": offset,
		}
		if filter != nil {
			body["filter"] = filter
		}

		var result v3ListResponse[T]
		url := fmt.Sprintf("%s/api/nutanix/v3/%ss/list", r.url, resourceKind)
		status, err := r.post(url, body, &result)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, liberr.New(fmt.Sprintf("unexpected status: %d", status))
		}

		entities = append(entities, result.Entities...)

		if len(result.Entities) == 0 {
			break
		}

		offset += len(result.Entities)
		if offset >= result.Metadata.TotalMatches {
			break
		}
	}

	return entities, nil
}

// listAll pages through a v3 list endpoint, following the response's
// total_matches, and returns every entity across all pages. This keeps a
// single provider from silently truncating on Prism inventories larger than
// one page.
func (r *Client) listAll(resourceKind string, filter map[string]interface{}, pageSize int) (entities []map[string]interface{}, err error) {
	rawEntities, err := listAllV3[map[string]interface{}](r, resourceKind, filter, pageSize)
	if err != nil {
		return nil, err
	}
	return rawEntities, nil
}

// listAllV4 pages through a v4 "config" namespace list endpoint, following
// the response's metadata.totalAvailableResults, and returns every raw
// entity across all pages.
func listAllV4[T any](r *Client, path string, pageSize int) ([]T, error) {
	url := fmt.Sprintf("%s%s", r.url, path)
	page := 0
	entities := make([]T, 0)

	for {
		var result v4ListResponse[T]
		status, err := r.get(url, &result,
			libweb.Param{Key: "$page", Value: strconv.Itoa(page)},
			libweb.Param{Key: "$limit", Value: strconv.Itoa(pageSize)},
		)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, liberr.New(fmt.Sprintf("unexpected status listing %s: %d", path, status))
		}

		entities = append(entities, result.Data...)

		if len(result.Data) == 0 {
			break
		}

		if len(entities) >= result.Metadata.TotalAvailableResults {
			break
		}

		page++
	}

	return entities, nil
}

// List all clusters, scoped to the configured clusterUuid (if any).
// Prism Central's own self-registered pseudo-cluster entry is excluded --
// see isPrismCentralCluster.
func (r *Client) listClusters() (entities []clusterEntity, err error) {
	entities, err = listAllV3[clusterEntity](r, "cluster", nil, clusterPageSize)
	if err != nil {
		return nil, err
	}
	entities = withoutPrismCentralClusters(entities)
	if err = r.ensurePrismConfig(); err != nil {
		return nil, err
	}
	return filterByMatch(entities, r.prism.ClusterUUID, func(entity clusterEntity) string {
		return entity.Metadata.UUID
	}), nil
}

// List all hosts, scoped to the configured clusterUuid (if any). Hosts
// belonging to Prism Central's own pseudo-cluster (i.e. its underlying
// appliance, not a real hypervisor node) are excluded.
func (r *Client) listHosts() (entities []hostEntity, err error) {
	entities, err = listAllV3[hostEntity](r, "host", nil, hostPageSize)
	if err != nil {
		return nil, err
	}
	clusters, err := listAllV3[clusterEntity](r, "cluster", nil, clusterPageSize)
	if err != nil {
		return nil, err
	}
	entities = excludeHostsByCluster(entities, excludedClusterUUIDs(clusters))
	if err = r.ensurePrismConfig(); err != nil {
		return nil, err
	}
	return filterByMatch(entities, r.prism.ClusterUUID, func(entity hostEntity) string {
		return entity.clusterUUID()
	}), nil
}

// List all VMs, scoped to the configured clusterUuid (if any).
func (r *Client) listVMs() (entities []vmEntity, err error) {
	entities, err = listAllV3[vmEntity](r, "vm", nil, vmPageSize)
	if err != nil {
		return nil, err
	}
	if err = r.ensurePrismConfig(); err != nil {
		return nil, err
	}
	return filterByMatch(entities, r.prism.ClusterUUID, func(entity vmEntity) string {
		return entity.Spec.ClusterReference.UUID
	}), nil
}

// List all subnets (networks), scoped to the configured clusterUuid (if any).
func (r *Client) listSubnets() (entities []networkEntity, err error) {
	entities, err = listAllV3[networkEntity](r, "subnet", nil, subnetPageSize)
	if err != nil {
		return nil, err
	}
	if err = r.ensurePrismConfig(); err != nil {
		return nil, err
	}
	return filterByMatch(entities, r.prism.ClusterUUID, func(entity networkEntity) string {
		return entity.clusterUUID()
	}), nil
}
