package main
import (
"bytes"
"context"
"encoding/json"
"fmt"
"io"
"net/http"
"os"
"strings"
"time"
)
type rpcRequest struct {
JSONRPC string json:"jsonrpc"
ID      int    json:"id,omitempty"
Method  string json:"method"
Params  any    json:"params,omitempty"
}
type rpcResponse struct {
JSONRPC string          json:"jsonrpc"
ID      int             json:"id"
Result  json.RawMessage json:"result"
Error   any             json:"error"
}
type toolResult struct {
IsError           bool            json:"isError"
StructuredContent json.RawMessage json:"structuredContent"
Content           []struct {
Type string json:"type"
Text string json:"text"
} json:"content"
}
type mcpClient struct {
url     string
key     string
session string
nextID  int
client  *http.Client
}
func fail(format string, args ...any) {
fmt.Fprintf(os.Stderr, "stitchgen: "+format+"\n", args...)
os.Exit(1)
}
func parseMessage(raw []byte) rpcResponse {
text := strings.TrimSpace(string(raw))
if text == "" {
fail("empty response from MCP server")
}
var msg rpcResponse
if strings.HasPrefix(text, "{") {
if err := json.Unmarshal([]byte(text), &msg); err != nil {
fail("failed to parse JSON response: %v", err)
}
return msg
}
found := false
for _, line := range strings.Split(text, "\n") {
line = strings.TrimSpace(line)
if strings.HasPrefix(line, "data:") {
data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
if data != "" && data != "[DONE]" {
var candidate rpcResponse
if err := json.Unmarshal([]byte(data), &candidate); err == nil {
msg = candidate
found = true
}
}
}
}
if !found {
fail("no JSON-RPC message in SSE response")
}
return msg
}
func (m *mcpClient) post(body any, timeout time.Duration) []byte {
payload, err := json.Marshal(body)
if err != nil {
fail("marshal request: %v", err)
}
ctx, cancel := context.WithTimeout(context.Background(), timeout)
defer cancel()
req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.url, bytes.NewReader(payload))
if err != nil {
fail("create request: %v", err)
}
req.Header.Set("Content-Type", "application/json")
req.Header.Set("Accept", "application/json, text/event-stream")
req.Header.Set("X-Goog-Api-Key", m.key)
if m.session != "" {
req.Header.Set("Mcp-Session-Id", m.session)
}
resp, err := m.client.Do(req)
if err != nil {
fail("POST %s: %v", m.url, err)
}
defer resp.Body.Close()
if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
m.session = sid
}
data, err := io.ReadAll(resp.Body)
if err != nil {
fail("read response from %s: %v", m.url, err)
}
if resp.StatusCode >= 400 {
snippet := data
if len(snippet) > 500 {
snippet = snippet[:500]
}
fail("POST %s: HTTP %d: %q", m.url, resp.StatusCode, string(snippet))
}
return data
}
func (m *mcpClient) rpc(method string, params any, timeout time.Duration) json.RawMessage {
m.nextID++
msg := parseMessage(m.post(rpcRequest{
JSONRPC: "2.0",
ID:      m.nextID,
Method:  method,
Params:  params,
}, timeout))
if msg.Error != nil {
fail("%s: RPC error %v", method, msg.Error)
}
if len(msg.Result) == 0 {
fail("%s: no result in response", method)
}
return msg.Result
}
func (m mcpClient) notify(method string) {
m.post(rpcRequest{
JSONRPC: "2.0",
Method:  method,
}, 60time.Second)
}
func (m *mcpClient) callTool(name string, args any, timeout time.Duration) map[string]any {
raw := m.rpc("tools/call", map[string]any{
"name":      name,
"arguments": args,
}, timeout)
var res toolResult
if err := json.Unmarshal(raw, &res); err != nil {
fail("tool %s: invalid tool result JSON: %v", name, err)
}
var texts []string
for _, item := range res.Content {
if item.Type == "text" {
texts = append(texts, item.Text)
}
}
combinedText := strings.Join(texts, "")
if res.IsError {
fail("tool %s failed: %s", name, combinedText)
}
out := make(map[string]any)
trimmedStruct := bytes.TrimSpace(res.StructuredContent)
if bytes.HasPrefix(trimmedStruct, []byte("{")) {
if err := json.Unmarshal(trimmedStruct, &out); err != nil {
fail("tool %s: invalid structuredContent JSON: %v", name, err)
}
return out
}
if combinedText == "" {
fail("tool %s returned no content", name)
}
if err := json.Unmarshal([]byte(combinedText), &out); err != nil {
snippet := combinedText
if len(snippet) > 500 {
snippet = snippet[:500]
}
fail("tool %s returned non-JSON content: %q", name, snippet)
}
return out
}
func stringField(m map[string]any, key string) string {
if v, ok := m[key].(string); ok {
return v
}
return ""
}
func extractDownloadURL(screen map[string]any) string {
if htmlCode, ok := screen["htmlCode"].(map[string]any); ok {
return stringField(htmlCode, "downloadUrl")
}
return ""
}
func fetchPage(client *http.Client, url, apiKey string, timeout time.Duration) (int, []byte) {
ctx, cancel := context.WithTimeout(context.Background(), timeout)
defer cancel()
req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
if err != nil {
fail("download request: %v", err)
}
if apiKey != "" {
req.Header.Set("X-Goog-Api-Key", apiKey)
}
resp, err := client.Do(req)
if err != nil {
fail("download: %v", err)
}
defer resp.Body.Close()
body, err := io.ReadAll(resp.Body)
if err != nil {
fail("download read: %v", err)
}
return resp.StatusCode, body
}
func main() {
mcpURL := strings.TrimSpace(os.Getenv("STITCH_MCP_URL"))
if mcpURL == "" {
mcpURL = "googleapis.com"
}
key := strings.TrimSpace(os.Getenv("STITCH_AI_KEY"))
if key == "" {
fail("STITCH_AI_KEY is not set")
}
prompt := strings.TrimSpace(strings.ReplaceAll(os.Getenv("PROMPT"), "@stitch", ""))
if prompt == "" {
fail("PROMPT is empty (nothing to generate)")
}
issue := strings.TrimSpace(os.Getenv("ISSUE_NUMBER"))
if issue == "" {
fail("ISSUE_NUMBER is not set")
}
mcp := &mcpClient{
url:    mcpURL,
key:    key,
client: &http.Client{},
}
mcp.rpc("initialize", map[string]any{
"protocolVersion": "2024-11-05",
"capabilities":    map[string]any{},
"clientInfo": map[string]any{
"name":    "stitchgen",
"version": "0.1.0",
},
}, 60*time.Second)
mcp.notify("notifications/initialized")
project := mcp.callTool("create_project", map[string]any{
"title": fmt.Sprintf("MateflowLab-Issue-%s", issue),
}, 60*time.Second)
projectID := strings.TrimPrefix(stringField(project, "name"), "projects/")
if projectID == "" {
projectID = stringField(project, "projectId")
}
if projectID == "" {
fail("create_project: no project id in response: %v", project)
}
fmt.Printf("project: %s\n", projectID)
gen := mcp.callTool("generate_screen_from_text", map[string]any{
"projectId": projectID,
"prompt":    prompt,
}, 900*time.Second)
var screen map[string]any
if components, ok := gen["outputComponents"].([]any); ok {
for _, c := range components {
cMap, ok := c.(map[string]any)
if !ok {
continue
}
design, ok := cMap["design"].(map[string]any)
if !ok {
continue
}
screens, ok := design["screens"].([]any)
if !ok {
continue
}
for _, item := range screens {
if sMap, ok := item.(map[string]any); ok {
screen = sMap
break
}
}
if screen != nil {
break
}
}
}
if screen == nil {
fail("generate_screen_from_text: no screen in response")
}
downloadURL := extractDownloadURL(screen)
if downloadURL == "" {
name := stringField(screen, "name")
parts := strings.Split(strings.Trim(name, "/"), "/")
if len(parts) != 4 {
fail("generate response has no download URL and no screen name to look up")
}
var projID, scrID string
for idx, val := range parts {
if idx == 1 {
projID = val
}
if idx == 3 {
scrID = val
}
}
detail := mcp.callTool("get_screen", map[string]any{
"name":      name,
"projectId": projID,
"screenId":  scrID,
}, 60*time.Second)
downloadURL = extractDownloadURL(detail)
}
if downloadURL == "" {
fail("no HTML download URL for the generated screen")
}
status, html := fetchPage(mcp.client, downloadURL, key, 120time.Second)
if status >= 400 {
status, html = fetchPage(mcp.client, downloadURL, "", 120time.Second)
}
if status >= 400 {
snippet := html
if len(snippet) > 500 {
snippet = snippet[:500]
}
fail("download: HTTP %d: %q", status, string(snippet))
}
var errPayload struct {
Error *struct {
Message string json:"message"
} json:"error"
}
if json.Unmarshal(html, &errPayload) == nil && errPayload.Error != nil {
fail("download: server returned an error, not a page: %s", errPayload.Error.Message)
}
trimmed := bytes.TrimSpace(html)
if !bytes.HasPrefix(trimmed, []byte("<")) {
snippet := html
if len(snippet) > 100 {
snippet = snippet[:100]
}
fail("downloaded content is not HTML (first bytes: %q)", string(snippet))
}
filename := fmt.Sprintf("stitch-generated-%d.html", time.Now().UnixMilli())
if err := os.WriteFile(filename, html, 0644); err != nil {
fail("write file %s: %v", filename, err)
}
fmt.Printf("Saved HTML page from Google Stitch: %s (%d bytes)\n", filename, len(html))
}
Once main.go is committed in the root directory, comment @stitch on an issue to trigger the workflow, and share the Actions log if anything needs adjustment.
Your repository root (MateflowlabsDesginIndexpages) is now ready for main.go—click Add file > Create new file right on this screen to add it.

Exact Steps From Your Current Screen

1. Click the Add file dropdown button (next to the green <> Code button in your screenshot) and select Create new file.
2. In the Name your file... box at the top, type main.go.
3. Paste the complete main.go code from the message above into the editor.
4. Click Commit changes... in the top right.
Once committed, main.go will appear directly in this file list right under .github/workflows and node_modules.
After you commit main.go, share a screenshot of this root file list or your Actions run log if you'd like me to verify that the workflow executes cleanly.

