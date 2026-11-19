package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"://github.com"
	"://github.com"
)

type BrowserCheckResult struct {
	URL               string   `json:"url"`
	HTTPStatus        int64    `json:"http_status"`
	FinalURL          string   `json:"final_url"`
	Title             string   `json:"title"`
	VisibleLines      int      `json:"visible_lines"`
	VisibleSnippet    string   `json:"visible_snippet"`
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

var doubleSchemeRegex = regexp.MustCompile(`https?://://[^\s"'<>]+`)

var auditTargets = map[string][]string{
	"malformed_and_split_surfaces": {
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
	},
	"gated_and_gateway_doors": {
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
	},
	"unknown_and_rate_limited": {
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
	},
	"dashboard_stubs_and_subpages": {
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
		"https://mateflowlabs.comdashboard.html",
		"https://mateflowlabs.commanage.html",
		"https://mateflowlabs.comfeatures.html",
		"https://mateflowlabs.comlabels.html",
	},
	"duplicate_surfaces": {
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
		"https://mateflowlabs.com",
	},
}

var dataEndpoints = map[string]string{
	"chat_orders_live": "https://mateflowlabs.com",
	"hub_orders_json":  "https://mateflowlabs.com",
	"hub_catalog_json": "https://mateflowlabs.com",
	"hub_feed_json":    "https://mateflowlabs.com",
}

func fetchWithBackoff(targetURL string) APICheckResult {
	client := &http.Client{Timeout: 15 * time.Second}
	for attempt := 0; attempt < 3; attempt++ {
		req, _ := http.NewRequest("GET", targetURL, nil)
		req.Header.Set("User-Agent", "Mateflow-Go-Audit/1.0")
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

func inspectPage(parentCtx context.Context, targetURL string) BrowserCheckResult {
	ctx, cancel := context.WithTimeout(parentCtx, 25*time.Second)
	defer cancel()

	tabCtx, tabCancel := chromedp.NewContext(ctx)
	defer tabCancel()

	var statusCode int64
	chromedp.ListenTarget(tabCtx, func(ev interface{}) {
		if respEv, ok := ev.(*network.EventResponseReceived); ok {
			if respEv.Type == network.ResourceTypeDocument && statusCode == 0 {
				statusCode = respEv.Response.Status
			}
		}
	})

	var finalURL, title, htmlContent, bodyText string
	err := chromedp.Run(tabCtx,
		network.Enable(),
		chromedp.Navigate(targetURL),
		chromedp.Sleep(2*time.Second),
		chromedp.Location(&finalURL),
		chromedp.Title(&title),
		chromedp.OuterHTML("html", &htmlContent),
		chromedp.Text("body", &bodyText),
	)
	if err != nil {
		return BrowserCheckResult{URL: targetURL, HTTPStatus: statusCode, Error: err.Error()}
	}

	var nonEmpty []string
	for _, line := range strings.Split(bodyText, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}
	snippetCount := len(nonEmpty)
	if snippetCount > 6 {
		snippetCount = 6
	}

	return BrowserCheckResult{
		URL:               targetURL,
		HTTPStatus:        statusCode,
		FinalURL:          finalURL,
		Title:             title,
		VisibleLines:      len(nonEmpty),
		VisibleSnippet:    strings.Join(nonEmpty[:snippetCount], " | "),
		DoubleSchemeLinks: doubleSchemeRegex.FindAllString(htmlContent, -1),
	}
}

func analyzeWithClaudeOpus(telemetry AuditTelemetry) (string, error) {
	apiKey := os.Getenv("POE_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("POE_API_KEY is not set")
	}

	rawJSON, _ := json.MarshalIndent(telemetry, "", "  ")
	prompt := fmt.Sprintf(`Audit the Mateflow Labs infrastructure using this live headless Chromium and API telemetry.
Address all 8 issues:
1. Double-scheme malformed links (https://://) in az-hub.html and god-mode-admin paths.
2. az-hub.html 401 vs 200 split across apex, hub, go, and dashboard in a real browser.
3. ://mateflowlabs.com vs ://mateflowlabs.com sign-in gating and missing OpenClaw gateway UI.
4. ://mateflowlabs.com and /bidding/ rendered DOM vs empty shell.
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

	req, _ := http.NewRequest("POST", "https://poe.com", bytes.NewBuffer(reqBody))
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
	if err := json.Unmarshal(respBytes, &parsed); err != nil || len(parsed.Choices) == 0 {
		return "", fmt.Errorf("failed to parse Poe API response: %s", string(respBytes))
	}
	return parsed.Choices[0].Message.Content, nil
}

func main() {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36"),
	)
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	telemetry := AuditTelemetry{
		BrowserChecks: make(map[string][]BrowserCheckResult),
		APIDataChecks: make(map[string]APICheckResult),
	}

	for group, urls := range auditTargets {
		for _, u := range urls {
			telemetry.BrowserChecks[group] = append(telemetry.BrowserChecks[group], inspectPage(allocCtx, u))
			time.Sleep(1500 * time.Millisecond)
		}
	}

	for key, u := range dataEndpoints {
		telemetry.APIDataChecks[key] = fetchWithBackoff(u)
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
