package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type BrowserCheckResult struct {
	URL               string   `json:"url"`
	HTTPStatus        int      `json:"http_status"`
	FinalURL          string   `json:"final_url"`
	Title             string   `json:"title"`
	DOMLength         int      `json:"dom_length"`
	TextSnippet       string   `json:"text_snippet"`
	DoubleSchemeLinks []string `json:"double_scheme_links"`
	Error             string   `json:"error,omitempty"`
}

type APICheckResult struct {
	URL              string   `json:"url"`
	Status           int      `json:"status"`
	Length           int      `json:"length"`
	DoubleSchemeHits []string `json:"double_scheme_hits"`
	BodyPreview      string   `json:"body_preview"`
	Error            string   `json:"error,omitempty"`
}

type AuditTelemetry struct {
	BrowserChecks map[string][]BrowserCheckResult `json:"browser_checks"`
	APIDataChecks map[string]APICheckResult       `json:"api_data_checks"`
}

var (
	doubleSchemeRegex = regexp.MustCompile(`https?://://[^\s"'<>]+`)
	titleRegex        = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	tagRegex          = regexp.MustCompile(`(?s)<[^>]*>`)
)

func u(host, path string) string {
	return (&url.URL{Scheme: "https", Host: host, Path: path}).String()
}

func buildTargets() map[string][]string {
	return map[string][]string{
		"malformed_and_split_surfaces": {
			u("mateflowlabs.com", "/az-hub.html"),
			u("://mateflowlabs.com", "/az-hub.html"),
			u("://mateflowlabs.com", "/az-hub.html"),
			u("://mateflowlabs.com", "/az-hub.html"),
			u("mateflowlabs.com", "/god-mode-admin/index.html"),
			u("mateflowlabs.com", "/GodModeAdmin.html"),
			u("mateflowlabs.com", "/god-mode-admin/GodModeAdmin.html"),
		},
		"gated_and_gateway_doors": {
			u("://mateflowlabs.com", "/"),
			u("://mateflowlabs.com", "/"),
			u("://mateflowlabs.com", "/"),
		},
		"unknown_and_rate_limited": {
			u("://mateflowlabs.com", "/ledger/"),
			u("://mateflowlabs.com", "/bidding/"),
			u("mateflowlabs.com", "/dropbox-cb/"),
			u("://mateflowlabs.com", "/dropbox-cb/"),
		},
		"dashboard_stubs_and_subpages": {
			u("://mateflowlabs.com", "/"),
			u("://mateflowlabs.com", "/"),
			u("://mateflowlabs.com", "/dashboard.html"),
			u("://mateflowlabs.com", "/manage.html"),
			u("://mateflowlabs.com", "/features.html"),
			u("://mateflowlabs.com", "/labels.html"),
		},
		"duplicate_surfaces": {
			u("://mateflowlabs.com", "/studio/"),
			u("://mateflowlabs.com", "/ops/"),
			u("mateflowlabs.com", "/Owner/"),
			u("mateflowlabs.com", "/Seller/"),
			u("mateflowlabs.com", "/nexustrust/"),
			u("mateflowlabs.com", "/enterprise.html"),
			u("mateflowlabs.com", "/mateflow-enterprise-v4.html"),
		},
	}
}

func buildEndpoints() map[string]string {
	return map[string]string{
		"chat_orders_live": u("://mateflowlabs.com", "/api/v1/orders/live"),
		"hub_orders_json":  u("://mateflowlabs.com", "/orders.json"),
		"hub_catalog_json": u("://mateflowlabs.com", "/catalog.json"),
		"hub_feed_json":    u("://mateflowlabs.com", "/feed.json"),
	}
}

func fetchWithBackoff(targetURL string) APICheckResult {
	client := &http.Client{Timeout: 15 * time.Second}
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequest("GET", targetURL, nil)
		if err != nil {
			return APICheckResult{URL: targetURL, Error: err.Error()}
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36")
		resp, err := client.Do(req)
		if err != nil {
			if attempt == 2 {
				return APICheckResult{URL: targetURL, Error: err.Error()}
			}
			time.Sleep(2 * time.Second)
			continue
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 429 {
			time.Sleep(time.Duration((attempt+1)*5) * time.Second)
			continue
		}

		bodyStr := string(bodyBytes)
		preview := bodyStr
		if len(preview) > 400 {
			preview = preview[:400]
		}
		return APICheckResult{
			URL:              targetURL,
			Status:           resp.StatusCode,
			Length:           len(bodyStr),
			DoubleSchemeHits: doubleSchemeRegex.FindAllString(bodyStr, -1),
			BodyPreview:      preview,
		}
	}
	return APICheckResult{URL: targetURL, Status: 429, Error: "Persisted 429 after retries"}
}

func inspectWithHeadlessChrome(targetURL string) BrowserCheckResult {
	res := BrowserCheckResult{URL: targetURL, FinalURL: targetURL}

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", targetURL, nil)
	if err == nil {
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36")
		if resp, doErr := client.Do(req); doErr == nil {
			res.HTTPStatus = resp.StatusCode
			if resp.Request != nil && resp.Request.URL != nil {
				res.FinalURL = resp.Request.URL.String()
			}
			resp.Body.Close()
		} else {
			res.Error = doErr.Error()
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "google-chrome",
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--virtual-time-budget=4000",
		"--dump-dom",
		targetURL,
	)
	out, cmdErr := cmd.Output()
	if cmdErr != nil && len(out) == 0 {
		if res.Error == "" {
			res.Error = cmdErr.Error()
		}
		return res
	}

	dom := string(out)
	res.DOMLength = len(dom)
	res.DoubleSchemeLinks = doubleSchemeRegex.FindAllString(dom, -1)

	matches := titleRegex.FindStringSubmatch(dom)
	for i, m := range matches {
		if i == 1 {
			res.Title = strings.TrimSpace(m)
		}
	}

	plain := tagRegex.ReplaceAllString(dom, " ")
	fields := strings.Fields(plain)
	if len(fields) > 60 {
		fields = fields[:60]
	}
	res.TextSnippet = strings.Join(fields, " ")
	return res
}

func analyzeWithClaudeOpus(telemetry AuditTelemetry) (string, error) {
	apiKey := os.Getenv("POE_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("POE_API_KEY is not set")
	}

	rawJSON, _ := json.MarshalIndent(telemetry, "", "  ")
	prompt := fmt.Sprintf(`Audit the Mateflow Labs infrastructure using this live headless Chrome DOM and HTTP telemetry.
Address all 8 issues:
1. Double-scheme malformed links in az-hub.html and god-mode-admin paths.
2. az-hub.html 401 vs 200 split across apex, hub, go, and dashboard in a real browser.
3. ://mateflowlabs.com vs ://mateflowlabs.com sign-in gating and missing OpenClaw gateway UI.
4. ://mateflowlabs.com/ledger/ and /bidding/ rendered DOM vs empty shell.
5. chat /api/v1/orders/live stub (open_unfulfilled: 0) vs hub orders.json.
6. ://mateflowlabs.com and ://mateflowlabs.com 2-line stub root vs working subpages.
7. /dropbox-cb/ rate-limit retry outcomes on apex and chat.
8. Content comparison for duplicate surfaces (/studio/, /ops/, /Owner/, /Seller/, /nexustrust/, enterprise.html).
Provide exact Nginx reverse-proxy/routing fixes and HTML patches, including mapping the 16 /var/www/mateflow-* portal roots.

TELEMETRY:
%s`, string(rawJSON))

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": "Claude-Opus-4.7",
		"messages": []map[string]string{
			{"role": "system", "content": "You are a principal infrastructure engineer and full-stack auditor. Provide exact Nginx and HTML patches."},
			{"role": "user", "content": prompt},
		},
	})

	poeURL := u("://poe.com", "/v1/chat/completions")
	req, err := http.NewRequest("POST", poeURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Poe API returned %d: %s", resp.StatusCode, string(respBytes))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse Poe API response: %s", string(respBytes))
	}
	for _, choice := range parsed.Choices {
		if choice.Message.Content != "" {
			return choice.Message.Content, nil
		}
	}
	return "", fmt.Errorf("empty choices returned from Poe API: %s", string(respBytes))
}

func main() {
	telemetry := AuditTelemetry{
		BrowserChecks: make(map[string][]BrowserCheckResult),
		APIDataChecks: make(map[string]APICheckResult),
	}

	for group, urls := range buildTargets() {
		for _, target := range urls {
			telemetry.BrowserChecks[group] = append(telemetry.BrowserChecks[group], inspectWithHeadlessChrome(target))
			time.Sleep(1500 * time.Millisecond)
		}
	}

	for key, target := range buildEndpoints() {
		telemetry.APIDataChecks[key] = fetchWithBackoff(target)
	}

	rawTelemetry, _ := json.MarshalIndent(telemetry, "", "  ")
	_ = os.WriteFile("audit_telemetry.json", rawTelemetry, 0644)

	report, err := analyzeWithClaudeOpus(telemetry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Opus analysis error: %v\n", err)
		os.Exit(1)
	}
	_ = os.WriteFile("AUDIT_REPORT.md", []byte(report), 0644)
	fmt.Println(report)
}
