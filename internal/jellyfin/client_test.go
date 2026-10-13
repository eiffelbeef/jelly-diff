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

func TestGetItemForUser_RequestsOverview(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), `Token="my-user-token"`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.Contains(r.URL.RawQuery, "Overview") {
			t.Errorf("expected query params to request Overview in Fields, got: %s", r.URL.RawQuery)
		}
		if r.URL.Path == "/Users/user-42/Items/movie-100" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Item{
				ID:       "movie-100",
				Name:     "Interstellar",
				Overview: "Mankind was born on Earth. It was never meant to die here.",
				Genres:   []string{"Adventure", "Drama", "Sci-Fi"},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "server-key")
	item, err := c.GetItemForUser(context.Background(), "my-user-token", "user-42", "movie-100")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item == nil {
		t.Fatal("expected item, got nil")
	}
	if item.Overview != "Mankind was born on Earth. It was never meant to die here." {
		t.Errorf("unexpected overview: %q", item.Overview)
	}
}

func TestGetItemForUser_FallbackToItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), `Token="my-user-token"`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/Users/user-42/Items/movie-100" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/Items/movie-100" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Item{
				ID:       "movie-100",
				Name:     "Interstellar",
				Overview: "Fallback overview from /Items/{id}",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "server-key")
	item, err := c.GetItemForUser(context.Background(), "my-user-token", "user-42", "movie-100")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item == nil {
		t.Fatal("expected item from fallback, got nil")
	}
	if item.Overview != "Fallback overview from /Items/{id}" {
		t.Errorf("unexpected overview: %q", item.Overview)
	}
}

func TestGetItem_ServerKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), `Token="server-key"`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.Contains(r.URL.RawQuery, "Overview") {
			t.Errorf("expected query params to request Overview, got: %s", r.URL.RawQuery)
		}
		if r.URL.Path == "/Items/movie-200" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Item{
				ID:       "movie-200",
				Name:     "Oppenheimer",
				Overview: "The story of J. Robert Oppenheimer.",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "server-key")
	item, err := c.GetItem(context.Background(), "movie-200")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item == nil {
		t.Fatal("expected item, got nil")
	}
	if item.Overview != "The story of J. Robert Oppenheimer." {
		t.Errorf("unexpected overview: %q", item.Overview)
	}
}

