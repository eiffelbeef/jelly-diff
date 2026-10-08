package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetLibraries(t *testing.T) {
	expected := []Library{{ID: "lib1", Name: "Movies"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), `Token="testkey"`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/Users" {
			users := []map[string]any{
				{"Id": "user1", "Policy": map[string]any{"IsAdministrator": true}},
			}
			json.NewEncoder(w).Encode(users)
			return
		}
		if r.URL.Path == "/Users/user1/Views" {
			json.NewEncoder(w).Encode(viewsResponse{Items: expected})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "testkey")
	libs, err := c.GetLibraries(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(libs) != 1 || libs[0].ID != "lib1" {
		t.Fatalf("unexpected libraries: %+v", libs)
	}
}

func TestGetItems_Pagination(t *testing.T) {
	itemsCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), `Token="key"`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/Users" {
			users := []map[string]any{
				{"Id": "user1", "Policy": map[string]any{"IsAdministrator": true}},
			}
			json.NewEncoder(w).Encode(users)
			return
		}
		if r.URL.Path == "/Users/user1/Items" {
			itemsCalls++
			var items []Item
			if itemsCalls == 1 {
				// First page: return 1000 items
				items = make([]Item, 1000)
				for i := range items {
					items[i].ID = fmt.Sprintf("item%d", i)
				}
				json.NewEncoder(w).Encode(itemsResponse{Items: items, TotalRecordCount: 1200})
			} else {
				// Second page: return 200 items
				items = make([]Item, 200)
				for i := range items {
					items[i].ID = fmt.Sprintf("item%d", 1000+i)
				}
				json.NewEncoder(w).Encode(itemsResponse{Items: items, TotalRecordCount: 1200})
			}
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "key")
	result, err := c.GetItems(context.Background(), "libID")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1200 {
		t.Fatalf("expected 1200 items, got %d", len(result))
	}
	if itemsCalls != 2 {
		t.Fatalf("expected 2 items HTTP calls, got %d", itemsCalls)
	}
}
