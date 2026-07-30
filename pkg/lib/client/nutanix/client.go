// Package nutanix provides a minimal, shared Nutanix Prism REST client
// (connect/auth, generic GET/POST/PUT/DELETE, and v3/v4 list pagination).
// It is deliberately narrow: resource-specific request/response shaping
// (e.g. how a VM or Image entity is built or interpreted) stays in the
// packages that use this client -- the inventory collector
// (pkg/controller/provider/container/nutanix) and the migration plan
// adapter (pkg/controller/plan/adapter/nutanix) -- so this package has no
// knowledge of either.
package nutanix

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kubev2v/forklift/pkg/controller/base"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libweb "github.com/kubev2v/forklift/pkg/lib/inventory/web"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	"github.com/kubev2v/forklift/pkg/lib/util"
	core "k8s.io/api/core/v1"
)

// ConnectionTimeout bounds how long Connect() and subsequent requests wait
// for a response once a request has been sent, when the caller hasn't set
// a more specific Client.Timeout.
const ConnectionTimeout = 30 * time.Second

// Client is a minimal Nutanix Prism (v3/v4) REST client shared by the
// inventory collector and the migration plan adapter. Callers set URL and
// Secret (and optionally Timeout and Log) before calling Connect().
type Client struct {
	// Base URL (e.g., https://prism-central:9440).
	URL string
	// Secret containing credentials (user/password, ca.crt, insecureSkipVerify).
	Secret *core.Secret
	// Per-request response timeout. Defaults to ConnectionTimeout when zero.
	Timeout time.Duration
	// Logger. Defaults to a package logger when unset.
	Log logging.LevelLogger

	client *libweb.Client
}

// Connect and authenticate with Nutanix Prism. Idempotent: repeated calls
// on an already-connected Client are a no-op. Connectivity is verified with
// a minimal request (listing a single cluster).
func (r *Client) Connect() (status int, err error) {
	var tlsClientConfig *tls.Config

	if r.client != nil {
		return http.StatusOK, nil
	}

	if r.Log == nil {
		r.Log = logging.WithName("client|nutanix")
	}

	if base.GetInsecureSkipVerifyFlag(r.Secret) {
		tlsClientConfig = &tls.Config{InsecureSkipVerify: true}
	} else if cacert, found := util.GetCACert(r.Secret); found {
		roots := x509.NewCertPool()
		ok := roots.AppendCertsFromPEM(cacert)
		if !ok {
			err = liberr.New("failed to parse CA certificate")
			return http.StatusBadRequest, err
		}
		tlsClientConfig = &tls.Config{RootCAs: roots}
	} else {
		tlsClientConfig = &tls.Config{InsecureSkipVerify: false}
	}

	r.URL = strings.TrimRight(r.URL, "/")

	responseHeaderTimeout := r.Timeout
	if responseHeaderTimeout <= 0 {
		responseHeaderTimeout = ConnectionTimeout
	}

	r.client = &libweb.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 10 * time.Second,
			}).DialContext,
			MaxIdleConns:          10,
			IdleConnTimeout:       10 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: responseHeaderTimeout,
			TLSClientConfig:       tlsClientConfig,
		},
	}

	status, err = r.testConnection()
	if err != nil {
		r.client = nil
		return status, err
	}

	r.Log.Info("Successfully connected to Nutanix", "url", r.URL)

	return http.StatusOK, nil
}

// testConnection verifies the URL and credentials with a minimal request.
func (r *Client) testConnection() (status int, err error) {
	url := fmt.Sprintf("%s/api/nutanix/v3/clusters/list", r.URL)
	body := map[string]interface{}{
		"kind":   "cluster",
		"offset": 0,
		"length": 1,
	}

	status, err = r.Post(url, body, nil)
	if err != nil {
		return status, liberr.Wrap(err, "connection test failed")
	}
	if status != http.StatusOK {
		return status, liberr.New("connection test failed", "status", status)
	}

	return http.StatusOK, nil
}

// Get issues an authenticated GET request.
func (r *Client) Get(url string, object interface{}, params ...libweb.Param) (status int, err error) {
	status, err = r.Connect()
	if err != nil {
		return
	}
	r.client.Header = r.createAuthHeader()
	return r.client.Get(url, object, params...)
}

// GetNoRedirect issues an authenticated GET without following redirects,
// returning the raw response status and headers instead of decoding a
// body. This exists for endpoints that hand back a redirect carrying
// caller-specific instructions in its headers (e.g. a token that must be
// replayed as a cookie on the next request) -- something a normal
// redirect-following client can't act on, since it never gets to see
// that intermediate response.
func (r *Client) GetNoRedirect(url string) (status int, header http.Header, err error) {
	status, err = r.Connect()
	if err != nil {
		return
	}

	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, liberr.Wrap(err, "failed to build request", "url", url)
	}
	request.Header = r.createAuthHeader()

	client := http.Client{
		Transport: r.client.Transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, liberr.Wrap(err, "request failed", "url", url)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	return response.StatusCode, response.Header, nil
}

// Post issues an authenticated POST request. Nutanix v3 uses POST for list
// operations as well as creation. Mutating v4 endpoints require a unique
// NTNX-Request-Id; it is harmless on v3, so every Post carries one.
func (r *Client) Post(url string, body interface{}, object interface{}) (status int, err error) {
	status, err = r.Connect()
	if err != nil {
		return
	}
	r.client.Header = r.createMutatingHeader()
	return r.client.Post(url, body, object)
}

// Put issues an authenticated PUT request (Nutanix v3's update pattern:
// GET the current spec, modify it, PUT the full spec back).
func (r *Client) Put(url string, body interface{}, object interface{}) (status int, err error) {
	status, err = r.Connect()
	if err != nil {
		return
	}
	r.client.Header = r.createMutatingHeader()
	return r.client.Put(url, body, object)
}

// Delete issues an authenticated DELETE request. `object`, if non-nil,
// receives the decoded response body (Nutanix v3 deletes return a task
// reference).
func (r *Client) Delete(url string, object interface{}) (status int, err error) {
	status, err = r.Connect()
	if err != nil {
		return
	}
	r.client.Header = r.createMutatingHeader()
	return r.client.Delete(url, object)
}

// List resources using the Nutanix v3 API pattern: POST with an
// offset/length body.
func (r *Client) List(resourceKind string, filter map[string]interface{}, offset, length int) (result map[string]interface{}, err error) {
	url := fmt.Sprintf("%s/api/nutanix/v3/%ss/list", r.URL, resourceKind)

	body := map[string]interface{}{
		"kind":   resourceKind,
		"offset": offset,
		"length": length,
	}
	if filter != nil {
		body["filter"] = filter
	}

	result = make(map[string]interface{})
	status, err := r.Post(url, body, &result)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, liberr.New(fmt.Sprintf("unexpected status: %d", status))
	}

	return result, nil
}

// ListAll pages through a v3 list endpoint, following the response's
// total_matches, and returns every entity across all pages.
func (r *Client) ListAll(resourceKind string, filter map[string]interface{}, pageSize int) (entities []map[string]interface{}, err error) {
	offset := 0
	entities = make([]map[string]interface{}, 0)

	for {
		result, err := r.List(resourceKind, filter, offset, pageSize)
		if err != nil {
			return nil, err
		}

		entitiesList, ok := result["entities"].([]interface{})
		if !ok {
			break
		}
		for _, e := range entitiesList {
			if entity, ok := e.(map[string]interface{}); ok {
				entities = append(entities, entity)
			}
		}

		if len(entitiesList) == 0 {
			break
		}

		metadata, ok := result["metadata"].(map[string]interface{})
		if !ok {
			break
		}
		totalMatches, ok := metadata["total_matches"].(float64)
		if !ok {
			break
		}

		offset += len(entitiesList)
		if offset >= int(totalMatches) {
			break
		}
	}

	return entities, nil
}

// ListAllV4 pages through a v4 "config"/"content" namespace list endpoint,
// following the response's metadata.totalAvailableResults, and returns
// every raw entity across all pages. v4 endpoints paginate via $page/$limit
// query params rather than v3's offset/length body fields.
func (r *Client) ListAllV4(path string, pageSize int) (entities []map[string]interface{}, err error) {
	url := fmt.Sprintf("%s%s", r.URL, path)
	page := 0
	entities = make([]map[string]interface{}, 0)

	for {
		result := make(map[string]interface{})
		status, err := r.Get(url, &result,
			libweb.Param{Key: "$page", Value: strconv.Itoa(page)},
			libweb.Param{Key: "$limit", Value: strconv.Itoa(pageSize)},
		)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, liberr.New(fmt.Sprintf("unexpected status listing %s: %d", path, status))
		}

		rawEntities, err := extractMapList(result, "data")
		if err != nil {
			return nil, err
		}
		entities = append(entities, rawEntities...)

		if len(rawEntities) == 0 {
			break
		}

		total := getIntPath(result, "metadata.totalAvailableResults")
		if len(entities) >= total {
			break
		}

		page++
	}

	return entities, nil
}

// createAuthHeader builds a Basic Auth header from Secret's user/password.
func (r *Client) createAuthHeader() http.Header {
	user := string(r.Secret.Data["user"])
	password := string(r.Secret.Data["password"])

	header := http.Header{}
	header.Set("Content-Type", "application/json")
	header.Set("Authorization", "Basic "+basicAuth(user, password))

	return header
}

// createMutatingHeader is createAuthHeader plus a fresh NTNX-Request-Id.
// Prism Central's v4 mutating APIs reject requests that omit it
// ("Failed to perform the operation as the request ID is missing.").
func (r *Client) createMutatingHeader() http.Header {
	header := r.createAuthHeader()
	header.Set("NTNX-Request-Id", uuid.NewString())
	return header
}

func basicAuth(username, password string) string {
	auth := username + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(auth))
}

// extractMapList reads a []interface{} of map[string]interface{} at `key`
// from a decoded JSON response, tolerating a missing or empty key.
func extractMapList(result map[string]interface{}, key string) ([]map[string]interface{}, error) {
	raw, ok := result[key]
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil, liberr.New(fmt.Sprintf("expected %s to be a list", key))
	}
	entities := make([]map[string]interface{}, 0, len(list))
	for _, e := range list {
		if entity, ok := e.(map[string]interface{}); ok {
			entities = append(entities, entity)
		}
	}
	return entities, nil
}

// getIntPath reads an int from a dotted path (e.g. "metadata.totalAvailableResults")
// within a decoded JSON response, tolerating a missing path or a JSON number
// decoded as float64.
func getIntPath(m map[string]interface{}, path string) int {
	var cur interface{} = m
	for _, part := range strings.Split(path, ".") {
		asMap, ok := cur.(map[string]interface{})
		if !ok {
			return 0
		}
		cur, ok = asMap[part]
		if !ok {
			return 0
		}
	}
	switch v := cur.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}
