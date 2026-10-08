package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const pageSize = 1000

// Client talks to the Jellyfin HTTP API using only an API key.
// The user ID is resolved automatically on first use.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client

	mu     sync.RWMutex
	userID string
}

// NewClient constructs a Client.
func NewClient(baseURL, apiKey string) *Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          50,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}
}

// resolveUserID fetches and caches the first admin user ID from the server.
// If an attempt fails, it does not cache the error, allowing subsequent retries.
func (c *Client) resolveUserID(ctx context.Context) (string, error) {
	c.mu.RLock()
	if c.userID != "" {
		uid := c.userID
		c.mu.RUnlock()
		return uid, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userID != "" {
		return c.userID, nil
	}

	var users []struct {
		ID     string `json:"Id"`
		Policy struct {
			IsAdministrator bool `json:"IsAdministrator"`
		} `json:"Policy"`
	}
	if err := c.get(ctx, fmt.Sprintf("%s/Users", c.baseURL), nil, &users); err != nil {
		return "", fmt.Errorf("list users: %w", err)
	}
	// Prefer an admin account; fall back to first user found.
	for _, u := range users {
		if u.Policy.IsAdministrator && u.ID != "" {
			c.userID = u.ID
			return c.userID, nil
		}
	}
	if len(users) > 0 && users[0].ID != "" {
		c.userID = users[0].ID
		return c.userID, nil
	}
	return "", fmt.Errorf("no users returned by Jellyfin /Users — check your API key")
}

// GetLibraries returns all media libraries visible to the resolved user.
func (c *Client) GetLibraries(ctx context.Context) ([]Library, error) {
	uid, err := c.resolveUserID(ctx)
	if err != nil {
		return nil, err
	}
	var resp viewsResponse
	if err := c.get(ctx, fmt.Sprintf("%s/Users/%s/Views", c.baseURL, uid), nil, &resp); err != nil {
		return nil, fmt.Errorf("get libraries: %w", err)
	}
	return resp.Items, nil
}

// GetItems returns all items in the given library using paginated requests.
func (c *Client) GetItems(ctx context.Context, libraryID string) ([]Item, error) {
	uid, err := c.resolveUserID(ctx)
	if err != nil {
		return nil, err
	}
	var all []Item
	for start := 0; ; {
		params := url.Values{
			"ParentId":         {libraryID},
			"Recursive":        {"true"},
			"StartIndex":       {strconv.Itoa(start)},
			"Fields":           {"SortName,DateCreated,ImageTags,ProductionYear,SeriesId,SeasonId,SeriesName,SeasonName,ParentIndexNumber,IndexNumber,SeriesPrimaryImageTag,AlbumId,Album,AlbumPrimaryImageTag"},
			"IncludeItemTypes": {"Movie,Series,Episode,MusicAlbum,Book,Audio"},
		}
		var resp itemsResponse
		if err := c.get(ctx, fmt.Sprintf("%s/Users/%s/Items", c.baseURL, uid), params, &resp); err != nil {
			return nil, fmt.Errorf("get items (start=%d): %w", start, err)
		}
		all = append(all, resp.Items...)
		start += len(resp.Items)
		if start >= resp.TotalRecordCount || len(resp.Items) == 0 {
			break
		}
	}
	return all, nil
}

// get performs an authenticated GET and JSON-decodes the body into out.
// BaseURL returns the configured Jellyfin server base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// Authenticate verifies user credentials against Jellyfin /Users/AuthenticateByName.
func (c *Client) Authenticate(ctx context.Context, username, password string) (*AuthResult, error) {
	reqBody, err := json.Marshal(authRequest{
		Username: username,
		Pw:       password,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal auth request: %w", err)
	}

	rawURL := fmt.Sprintf("%s/Users/AuthenticateByName", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", `MediaBrowser Client="jelly-diff", Device="web", DeviceId="jelly-diff-client", Version="1.0.0"`)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jellyfin auth: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("invalid username or password")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jellyfin auth unexpected status: %d", resp.StatusCode)
	}

	var authResp authResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return nil, fmt.Errorf("decode auth response: %w", err)
	}

	return &AuthResult{
		UserID:      authResp.User.ID,
		Username:    authResp.User.Name,
		AccessToken: authResp.AccessToken,
	}, nil
}

// getUserRequest executes an authenticated request on behalf of a Jellyfin user.
func (c *Client) getUserRequest(ctx context.Context, rawURL, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", userAuthHeader(token))
	req.Header.Set("Accept", "application/json")
	return c.http.Do(req)
}

// GetLibrariesForUser returns all media libraries visible to the authenticated user.
func (c *Client) GetLibrariesForUser(ctx context.Context, token, userID string) ([]Library, error) {
	resp, err := c.getUserRequest(ctx, fmt.Sprintf("%s/Users/%s/Views", c.baseURL, userID), token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get libraries for user: unexpected status %d", resp.StatusCode)
	}

	var views viewsResponse
	if err := json.NewDecoder(resp.Body).Decode(&views); err != nil {
		return nil, fmt.Errorf("decode views: %w", err)
	}
	return views.Items, nil
}

// GetItemForUser fetches rich metadata for a single item using the authenticated user's token.
// If the item does not exist or the user has no permission, it returns (nil, nil).
func (c *Client) GetItemForUser(ctx context.Context, token, userID, itemID string) (*Item, error) {
	resp, err := c.getUserRequest(ctx, fmt.Sprintf("%s/Users/%s/Items/%s", c.baseURL, userID, itemID), token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get item for user: unexpected status %d", resp.StatusCode)
	}

	var item Item
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return nil, fmt.Errorf("decode item: %w", err)
	}
	return &item, nil
}

// ProxyImage requests a primary image from Jellyfin as the authenticated user.
// The caller is responsible for closing the returned Response.Body.
func (c *Client) ProxyImage(ctx context.Context, token, itemID string, query url.Values) (*http.Response, error) {
	rawURL := fmt.Sprintf("%s/Items/%s/Images/Primary", c.baseURL, itemID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if query != nil {
		req.URL.RawQuery = query.Encode()
	}

	req.Header.Set("Authorization", userAuthHeader(token))

	return c.http.Do(req)
}

// Uses the full MediaBrowser Authorization header that Jellyfin requires.
func (c *Client) get(ctx context.Context, rawURL string, params url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if params != nil {
		req.URL.RawQuery = params.Encode()
	}

	// Jellyfin requires the full MediaBrowser scheme — X-Emby-Token alone is
	// rejected by many server versions.
	req.Header.Set("Authorization", authHeader("jelly-diff-server", "jelly-diff-001", c.apiKey))
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jellyfin %s: unexpected status %d", rawURL, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func authHeader(device, deviceID, token string) string {
	return fmt.Sprintf(`MediaBrowser Client="jelly-diff", Device="%s", DeviceId="%s", Version="1.0.0", Token="%s"`, device, deviceID, token)
}

func userAuthHeader(token string) string {
	return authHeader("web", "jelly-diff-client", token)
}


