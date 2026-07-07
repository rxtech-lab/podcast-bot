package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
)

type IAPProductSyncResult struct{}

type IAPProductSyncer interface {
	SyncIAPProduct(ctx context.Context, p IAPProduct) (IAPProductSyncResult, error)
	DeleteIAPProduct(ctx context.Context, p IAPProduct) error
}

type defaultIAPProductSyncer struct {
	revenueCat *revenueCatProductClient
}

func NewIAPProductSyncer(env *config.Env, httpClient *http.Client) IAPProductSyncer {
	if env == nil {
		return nil
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	rc := newRevenueCatProductClient(env, httpClient)
	if rc == nil {
		return nil
	}
	return &defaultIAPProductSyncer{
		revenueCat: rc,
	}
}

func (s *defaultIAPProductSyncer) SyncIAPProduct(ctx context.Context, p IAPProduct) (IAPProductSyncResult, error) {
	if s == nil || s.revenueCat == nil {
		return IAPProductSyncResult{}, errors.New("iap product sync is not configured")
	}
	if !p.Enabled {
		return IAPProductSyncResult{}, nil
	}
	if err := normalizeIAPProduct(&p); err != nil {
		return IAPProductSyncResult{}, err
	}
	if err := s.revenueCat.SyncProduct(ctx, p); err != nil {
		return IAPProductSyncResult{}, err
	}
	return IAPProductSyncResult{}, nil
}

func (s *defaultIAPProductSyncer) DeleteIAPProduct(ctx context.Context, p IAPProduct) error {
	if s == nil || s.revenueCat == nil {
		return errors.New("iap product sync is not configured")
	}
	return s.revenueCat.DeleteProduct(ctx, p)
}

type revenueCatProductClient struct {
	baseURL   string
	apiKey    string
	projectID string
	appID     string
	client    *http.Client
}

func newRevenueCatProductClient(env *config.Env, httpClient *http.Client) *revenueCatProductClient {
	if strings.TrimSpace(env.RevenueCatRESTAPIKey) == "" ||
		strings.TrimSpace(env.RevenueCatProjectID) == "" ||
		strings.TrimSpace(env.RevenueCatAppID) == "" {
		return nil
	}
	return &revenueCatProductClient{
		baseURL:   strings.TrimRight(env.RevenueCatAPIBaseURL, "/"),
		apiKey:    strings.TrimSpace(env.RevenueCatRESTAPIKey),
		projectID: strings.TrimSpace(env.RevenueCatProjectID),
		appID:     strings.TrimSpace(env.RevenueCatAppID),
		client:    httpClient,
	}
}

func (c *revenueCatProductClient) SyncProduct(ctx context.Context, p IAPProduct) error {
	err := c.CreateProduct(ctx, p)
	if err != nil {
		return err
	}
	return nil
}

func (c *revenueCatProductClient) CreateProduct(ctx context.Context, p IAPProduct) error {
	body := map[string]any{
		"store_identifier": p.ProductID,
		"app_id":           c.appID,
		"type":             revenueCatProductType(p.ProductType),
	}
	if p.DisplayName != "" {
		body["display_name"] = p.DisplayName
	}
	var resp struct {
		ID string `json:"id"`
	}
	status, err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/v2/projects/%s/products", c.projectID), body, &resp)
	if status == http.StatusConflict {
		return c.UpdateProduct(ctx, p)
	}
	if err != nil {
		return err
	}
	return nil
}

func (c *revenueCatProductClient) UpdateProduct(ctx context.Context, p IAPProduct) error {
	id, err := c.FindProductID(ctx, p)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	body := map[string]any{}
	if p.DisplayName != "" {
		body["display_name"] = p.DisplayName
	}
	if len(body) == 0 {
		return nil
	}
	status, err := c.doJSON(ctx, http.MethodPatch, fmt.Sprintf("/v2/projects/%s/products/%s", c.projectID, id), body, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

func (c *revenueCatProductClient) DeleteProduct(ctx context.Context, p IAPProduct) error {
	id, err := c.FindProductID(ctx, p)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	status, err := c.doJSON(ctx, http.MethodDelete, fmt.Sprintf("/v2/projects/%s/products/%s", c.projectID, id), nil, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

func (c *revenueCatProductClient) FindProductID(ctx context.Context, p IAPProduct) (string, error) {
	storeIdentifier := strings.TrimSpace(p.ProductID)
	if storeIdentifier == "" {
		return "", nil
	}
	var resp revenueCatProductsResponse
	_, err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/v2/projects/%s/products", c.projectID), nil, &resp)
	if err != nil {
		return "", err
	}
	for _, item := range resp.allProducts() {
		if item.storeIdentifier() != storeIdentifier {
			continue
		}
		if appID := item.appID(); appID != "" && appID != c.appID {
			continue
		}
		return item.ID, nil
	}
	return "", nil
}

func (c *revenueCatProductClient) doJSON(ctx context.Context, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusConflict {
		io.Copy(io.Discard, res.Body)
		return res.StatusCode, nil
	}
	if res.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, res.Body)
		return res.StatusCode, nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return res.StatusCode, fmt.Errorf("revenuecat product sync failed: status %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
			return res.StatusCode, err
		}
	}
	return res.StatusCode, nil
}

type revenueCatProductsResponse struct {
	Items []revenueCatProductResponse `json:"items"`
	Data  []revenueCatProductResponse `json:"data"`
}

func (r revenueCatProductsResponse) allProducts() []revenueCatProductResponse {
	if len(r.Items) > 0 {
		return r.Items
	}
	return r.Data
}

type revenueCatProductResponse struct {
	ID              string `json:"id"`
	StoreIdentifier string `json:"store_identifier"`
	AppID           string `json:"app_id"`
	Attributes      struct {
		StoreIdentifier string `json:"store_identifier"`
		AppID           string `json:"app_id"`
	} `json:"attributes"`
}

func (p revenueCatProductResponse) storeIdentifier() string {
	if strings.TrimSpace(p.StoreIdentifier) != "" {
		return strings.TrimSpace(p.StoreIdentifier)
	}
	return strings.TrimSpace(p.Attributes.StoreIdentifier)
}

func (p revenueCatProductResponse) appID() string {
	if strings.TrimSpace(p.AppID) != "" {
		return strings.TrimSpace(p.AppID)
	}
	return strings.TrimSpace(p.Attributes.AppID)
}

func revenueCatProductType(productType string) string {
	switch normalizeIAPProductType(productType) {
	case IAPProductTypeNonConsumable:
		return "non_consumable"
	case IAPProductTypeSubscription:
		return "subscription"
	default:
		return "consumable"
	}
}
