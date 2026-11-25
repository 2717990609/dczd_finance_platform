package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const organizationSyncPrefix = "/internal/v1/organization-sync/"
const organizationMessagePath = organizationSyncPrefix + "messages"
const organizationLookupPath = organizationSyncPrefix + "accounts/lookup"

var organizationDispatchSystems = []string{"DCZD", "DCJK", "DCAI"}

type organizationSyncClient struct {
	base, keyID string
	secret      []byte
	http        *http.Client
}

func dispatchTargetEnabled(target string) bool {
	return validDispatchTarget(target) && os.Getenv("ORG_SYNC_"+target+"_ENABLED") == "true"
}

func (s workspace) organizationDispatchChannels(w http.ResponseWriter, _ *http.Request) {
	enabled := make(map[string]bool, len(organizationDispatchSystems))
	for _, target := range organizationDispatchSystems {
		enabled[target] = dispatchTargetEnabled(target)
	}
	respond(w, enabled, nil)
}

func newOrganizationSyncClient(target string) (*organizationSyncClient, error) {
	if !validDispatchTarget(target) {
		return nil, invalid
	}
	prefix := "ORG_SYNC_" + target + "_"
	if !dispatchTargetEnabled(target) {
		return nil, errSourceDisabled
	}
	endpoint, err := url.Parse(strings.TrimRight(strings.TrimSpace(os.Getenv(prefix+"BASE_URL")), "/"))
	key, secret := os.Getenv(prefix+"KEY_ID"), os.Getenv(prefix+"SECRET")
	testHTTP := err == nil && os.Getenv("ORG_SYNC_ENVIRONMENT") == "test" && target == "DCZD" && endpoint.String() == "http://dachun-test-admin-shop-japi.xadazhihui.cn/api"
	if err != nil || endpoint.Host == "" || endpoint.Scheme != "https" && !testHTTP || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.EscapedPath() != "" && endpoint.EscapedPath() != "/api") || strings.TrimSpace(key) == "" || len(secret) < 32 {
		return nil, fmt.Errorf("%s 的组织同步 HTTPS 地址或凭据不完整", target)
	}
	return &organizationSyncClient{base: endpoint.String(), keyID: key, secret: []byte(secret), http: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func validDispatchTarget(s string) bool { return s == "DCZD" || s == "DCJK" || s == "DCAI" }
func (c *organizationSyncClient) Post(ctx context.Context, path string, value, result any) error {
	switch path {
	case organizationMessagePath, organizationLookupPath, organizationSyncPrefix + "health/changes", organizationSyncPrefix + "health/ack":
	default:
		return invalid
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	timestamp, nonce := strconv.FormatInt(time.Now().Unix(), 10), uuid.NewString()
	sum := sha256.Sum256(raw)
	mac := hmac.New(sha256.New, c.secret)
	_, _ = io.WriteString(mac, strings.Join([]string{"POST", path, c.keyID, timestamp, nonce, hex.EncodeToString(sum[:])}, "\n"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Sync-Key-Id", c.keyID)
	req.Header.Set("X-Org-Sync-Timestamp", timestamp)
	req.Header.Set("X-Org-Sync-Nonce", nonce)
	req.Header.Set("X-Org-Sync-Signature", hex.EncodeToString(mac.Sum(nil)))
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("组织同步请求失败")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("组织同步 HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(data) > 65536 {
		return errors.New("组织同步响应无效")
	}
	return json.Unmarshal(data, result)
}
