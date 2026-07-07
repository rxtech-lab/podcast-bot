package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestIAPProductSyncerSyncsAppStoreProductToRevenueCatOnly(t *testing.T) {
	paths := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if got := r.Header.Get("Authorization"); got == "" {
			t.Errorf("%s missing Authorization header", r.URL.Path)
		}
		switch r.URL.Path {
		case "/v2/projects/proj_123/products":
			var body struct {
				StoreIdentifier string `json:"store_identifier"`
				AppID           string `json:"app_id"`
				Type            string `json:"type"`
				DisplayName     string `json:"display_name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode revenuecat body: %v", err)
			}
			if body.StoreIdentifier != "app.rxlab.points1000" || body.AppID != "app_rc_123" || body.Type != "consumable" {
				t.Errorf("revenuecat body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"prod_rc_1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	syncer := NewIAPProductSyncer(testIAPSyncEnv(t, srv.URL), srv.Client())
	if syncer == nil {
		t.Fatal("NewIAPProductSyncer returned nil")
	}
	if _, err := syncer.SyncIAPProduct(context.Background(), IAPProduct{
		ProductID:        "app.rxlab.points1000",
		StoreEnvironment: IAPStoreEnvironmentAppStore,
		ProductType:      IAPProductTypeConsumable,
		DisplayName:      "1000 points",
		PointsGrant:      1000,
		Enabled:          true,
	}); err != nil {
		t.Fatalf("SyncIAPProduct: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/v2/projects/proj_123/products" {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestIAPProductSyncerTestStoreSkipsAppStoreConnect(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/v2/projects/proj_123/products" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"prod_test_1"}`))
	}))
	defer srv.Close()

	syncer := NewIAPProductSyncer(testIAPSyncEnv(t, srv.URL), srv.Client())
	if _, err := syncer.SyncIAPProduct(context.Background(), IAPProduct{
		ProductID:        "points_test",
		StoreEnvironment: IAPStoreEnvironmentTest,
		ProductType:      IAPProductTypeConsumable,
		PointsGrant:      1000,
		Enabled:          true,
	}); err != nil {
		t.Fatalf("SyncIAPProduct: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/v2/projects/proj_123/products" {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestIAPProductSyncerSyncsSubscriptionToRevenueCatOnly(t *testing.T) {
	paths := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if got := r.Header.Get("Authorization"); got == "" {
			t.Errorf("%s missing Authorization header", r.URL.Path)
		}
		switch r.URL.Path {
		case "/v2/projects/proj_123/products":
			var body struct {
				StoreIdentifier string `json:"store_identifier"`
				AppID           string `json:"app_id"`
				Type            string `json:"type"`
				DisplayName     string `json:"display_name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode revenuecat body: %v", err)
			}
			if body.StoreIdentifier != "app.rxlab.pro.monthly" || body.Type != "subscription" {
				t.Errorf("revenuecat body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"prod_rc_sub_1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	syncer := NewIAPProductSyncer(testIAPSyncEnv(t, srv.URL), srv.Client())
	if _, err := syncer.SyncIAPProduct(context.Background(), IAPProduct{
		ProductID:          "app.rxlab.pro.monthly",
		StoreEnvironment:   IAPStoreEnvironmentAppStore,
		ProductType:        IAPProductTypeSubscription,
		DisplayName:        "Podcaster Pro Monthly",
		SubscriptionPeriod: "one-month",
		Enabled:            true,
	}); err != nil {
		t.Fatalf("SyncIAPProduct: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/v2/projects/proj_123/products" {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestIAPProductSyncerUpdatesRevenueCatProductWhenCreateConflicts(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /v2/projects/proj_123/products":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"already exists"}`))
		case "GET /v2/projects/proj_123/products":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"items":[{"id":"prod_rc_existing","store_identifier":"app.rxlab.pro.monthly","app_id":"app_rc_123"}]}`))
		case "PATCH /v2/projects/proj_123/products/prod_rc_existing":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode revenuecat patch body: %v", err)
			}
			if body["display_name"] != "Podcaster Pro Monthly" {
				t.Errorf("revenuecat patch body = %#v", body)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	syncer := NewIAPProductSyncer(testIAPSyncEnv(t, srv.URL), srv.Client())
	if _, err := syncer.SyncIAPProduct(context.Background(), IAPProduct{
		ProductID:        "app.rxlab.pro.monthly",
		StoreEnvironment: IAPStoreEnvironmentAppStore,
		ProductType:      IAPProductTypeSubscription,
		DisplayName:      "Podcaster Pro Monthly",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("SyncIAPProduct: %v", err)
	}
	want := []string{
		"POST /v2/projects/proj_123/products",
		"GET /v2/projects/proj_123/products",
		"PATCH /v2/projects/proj_123/products/prod_rc_existing",
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %#v, want %#v", paths, want)
		}
	}
}

func TestIAPProductSyncerUpdatesExistingRevenueCatProductOnly(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /v2/projects/proj_123/products":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"already exists"}`))
		case "GET /v2/projects/proj_123/products":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"items":[{"id":"prod_rc_sub_1","store_identifier":"app.rxlab.pro.monthly","app_id":"app_rc_123"}]}`))
		case "PATCH /v2/projects/proj_123/products/prod_rc_sub_1":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode revenuecat patch body: %v", err)
			}
			if body["display_name"] != "Podcaster Pro Monthly Updated" {
				t.Errorf("revenuecat patch body = %#v", body)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	syncer := NewIAPProductSyncer(testIAPSyncEnv(t, srv.URL), srv.Client())
	if _, err := syncer.SyncIAPProduct(context.Background(), IAPProduct{
		ProductID:          "app.rxlab.pro.monthly",
		StoreEnvironment:   IAPStoreEnvironmentAppStore,
		ProductType:        IAPProductTypeSubscription,
		DisplayName:        "Podcaster Pro Monthly Updated",
		SubscriptionPeriod: "ONE_MONTH",
		Enabled:            true,
	}); err != nil {
		t.Fatalf("SyncIAPProduct: %v", err)
	}
	want := []string{
		"POST /v2/projects/proj_123/products",
		"GET /v2/projects/proj_123/products",
		"PATCH /v2/projects/proj_123/products/prod_rc_sub_1",
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %#v, want %#v", paths, want)
		}
	}
}

func TestIAPProductSyncerDeletesRevenueCatProductBySharedProductID(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /v2/projects/proj_123/products":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"items":[{"id":"prod_rc_1","store_identifier":"app.rxlab.points1000","app_id":"app_rc_123"}]}`))
		case "DELETE /v2/projects/proj_123/products/prod_rc_1":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	syncer := NewIAPProductSyncer(testIAPSyncEnv(t, srv.URL), srv.Client())
	if err := syncer.DeleteIAPProduct(context.Background(), IAPProduct{ProductID: "app.rxlab.points1000"}); err != nil {
		t.Fatalf("DeleteIAPProduct: %v", err)
	}
	want := []string{
		"GET /v2/projects/proj_123/products",
		"DELETE /v2/projects/proj_123/products/prod_rc_1",
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %#v, want %#v", paths, want)
		}
	}
}

func testIAPSyncEnv(t *testing.T, baseURL string) *config.Env {
	t.Helper()
	return &config.Env{
		RevenueCatRESTAPIKey: "rc_secret",
		RevenueCatProjectID:  "proj_123",
		RevenueCatAppID:      "app_rc_123",
		RevenueCatAPIBaseURL: baseURL,
	}
}
