package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/prometheus/prometheus/model/labels"

	mcpgrafana "github.com/grafana/mcp-grafana"
)

const (
	defaultTimeout               = 30 * time.Second
	rulesEndpointPath            = "/api/prometheus/grafana/api/v1/rules"
	defaultGrafanaAADResource    = "ce34e7e5-485f-4d76-964f-b3d2b16d1e4f"
)

type alertingClient struct {
	baseURL       *url.URL
	accessToken   string
	idToken       string
	apiKey        string
	httpClient    *http.Client
	aadCredential *azidentity.DefaultAzureCredential
}

func newAlertingClientFromContext(ctx context.Context) (*alertingClient, error) {
	cfg := mcpgrafana.GrafanaConfigFromContext(ctx)
	baseURL := strings.TrimRight(cfg.URL, "/")
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Grafana base URL %q: %w", baseURL, err)
	}

	client := &alertingClient{
		baseURL:       parsedBaseURL,
		accessToken:   cfg.AccessToken,
		idToken:       cfg.IDToken,
		apiKey:        cfg.APIKey,
		aadCredential: cfg.AADCredential,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}

	// Create custom transport with TLS configuration if available
	if tlsConfig := mcpgrafana.GrafanaConfigFromContext(ctx).TLSConfig; tlsConfig != nil {
		client.httpClient.Transport, err = tlsConfig.HTTPTransport(http.DefaultTransport.(*http.Transport))
		if err != nil {
			return nil, fmt.Errorf("failed to create custom transport: %w", err)
		}
	}

	return client, nil
}

func (c *alertingClient) makeRequest(ctx context.Context, path string) (*http.Response, error) {
	p := c.baseURL.JoinPath(path).String()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request to %s: %w", p, err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	// If accessToken is set we use that first and fall back to normal Authorization.
	if c.accessToken != "" && c.idToken != "" {
		req.Header.Set("X-Access-Token", c.accessToken)
		req.Header.Set("X-Grafana-Id", c.idToken)
	} else if c.aadCredential != nil {
		// Use AAD authentication
		token, err := c.aadCredential.GetToken(ctx, policy.TokenRequestOptions{
			Scopes: []string{defaultGrafanaAADResource},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get AAD token for alerting client: %w", err)
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.Token))
	} else if c.apiKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request to %s: %w", p, err)
	}
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("Grafana API returned status code %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return resp, nil
}

func (c *alertingClient) GetRules(ctx context.Context) (*rulesResponse, error) {
	resp, err := c.makeRequest(ctx, rulesEndpointPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get alert rules from Grafana API: %w", err)
	}
	defer resp.Body.Close()

	var rulesResponse rulesResponse
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&rulesResponse); err != nil {
		return nil, fmt.Errorf("failed to decode rules response from %s: %w", rulesEndpointPath, err)
	}

	return &rulesResponse, nil
}

type rulesResponse struct {
	Data struct {
		RuleGroups []ruleGroup      `json:"groups"`
		NextToken  string           `json:"groupNextToken,omitempty"`
		Totals     map[string]int64 `json:"totals,omitempty"`
	} `json:"data"`
}

type ruleGroup struct {
	Name           string         `json:"name"`
	FolderUID      string         `json:"folderUid"`
	Rules          []alertingRule `json:"rules"`
	Interval       float64        `json:"interval"`
	LastEvaluation time.Time      `json:"lastEvaluation"`
	EvaluationTime float64        `json:"evaluationTime"`
}

type alertingRule struct {
	State          string           `json:"state,omitempty"`
	Name           string           `json:"name,omitempty"`
	Query          string           `json:"query,omitempty"`
	Duration       float64          `json:"duration,omitempty"`
	KeepFiringFor  float64          `json:"keepFiringFor,omitempty"`
	Annotations    labels.Labels    `json:"annotations,omitempty"`
	ActiveAt       *time.Time       `json:"activeAt,omitempty"`
	Alerts         []alert          `json:"alerts,omitempty"`
	Totals         map[string]int64 `json:"totals,omitempty"`
	TotalsFiltered map[string]int64 `json:"totalsFiltered,omitempty"`
	UID            string           `json:"uid"`
	FolderUID      string           `json:"folderUid"`
	Labels         labels.Labels    `json:"labels,omitempty"`
	Health         string           `json:"health"`
	LastError      string           `json:"lastError,omitempty"`
	Type           string           `json:"type"`
	LastEvaluation time.Time        `json:"lastEvaluation"`
	EvaluationTime float64          `json:"evaluationTime"`
}

type alert struct {
	Labels      labels.Labels `json:"labels"`
	Annotations labels.Labels `json:"annotations"`
	State       string        `json:"state"`
	ActiveAt    *time.Time    `json:"activeAt"`
	Value       string        `json:"value"`
}
