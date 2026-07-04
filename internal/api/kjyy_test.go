package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 这里集中放 4 个 API 的测试数据，直接改这些字段就行。
var apiTestData = struct {
	Auth struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	GetList map[string]interface{}
	Req     map[string]interface{}
	Pay     map[string]interface{}
}{
	Auth: struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{
		Username: "20260001",
		Password: "test-password",
	},
	GetList: map[string]interface{}{
		"token":      "test-token",
		"siteAssort": "室外",
		"siteType":   "松山湖网球场",
		"typeId":     2,
		"meetDate":   "2026-07-03",
	},
	Req: map[string]interface{}{
		"token":             "test-token",
		"orderMoney":        "7.50",
		"additionalCharges": 12.5,
		"recordPurpose":     "1",
		"typeId":            2,
		"orderList": []map[string]interface{}{
			{
				"startTime": "2026-07-03 18:00",
				"endTime":   "2026-07-03 18:30",
			},
		},
		"params":    map[string]interface{}{},
		"siteId":    239,
		"ischanrge": true,
		"isReview":  false,
		"flowKey":   nil,
		"siteName":  "1号场",
		"mode":      2,
		"meetDate":  "2026-07-03",
	},
	Pay: map[string]interface{}{
		"token":    "test-token",
		"recordId": 115863,
	},
}

func TestAuthAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	kjyyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/callback":
			http.Redirect(w, r, "/landing?token=mock-token-123", http.StatusFound)
		case "/landing":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"msg":"landing ok"}`)
		default:
			t.Fatalf("unexpected kjyy path: %s", r.URL.Path)
		}
	}))
	defer kjyyServer.Close()

	casServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/login":
			_, _ = io.WriteString(w, `
				<html>
					<input name="lt" value="lt-token" />
					<input name="execution" value="e1s1" />
					<input id="pwdEncryptSalt" value="apkzIdhpPJhoplen" />
				</html>
			`)
		case r.Method == http.MethodPost && r.URL.Path == "/login":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm error: %v", err)
			}
			if got := r.Form.Get("username"); got != apiTestData.Auth.Username {
				t.Fatalf("unexpected username: %s", got)
			}
			if got := r.Form.Get("password"); got == "" {
				t.Fatal("expected encrypted password")
			}
			http.Redirect(w, r, kjyyServer.URL+"/callback?ticket=ST-12345", http.StatusFound)
		default:
			t.Fatalf("unexpected cas request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer casServer.Close()

	restore := overrideAPIEndpoints(casServer.URL, "", "", "", "")
	defer restore()

	body := map[string]interface{}{
		"username":   apiTestData.Auth.Username,
		"password":   apiTestData.Auth.Password,
		"serviceUrl": kjyyServer.URL + "/callback?type=2",
	}

	response := performAPIRequest(t, "/api/auth", body)
	logAPIResponse(t, "/api/auth", response.Body.String())

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.Code)
	}

	assertJSONField(t, response.Body.Bytes(), "ok", true)
	assertJSONField(t, response.Body.Bytes(), "token", "mock-token-123")
	assertNestedJSONField(t, response.Body.Bytes(), []string{"snapshot", "auth", "token"}, "mock-token-123")
}

func TestGetListAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var capturedQuery string
	var capturedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"data":[{"site":{"siteName":"1号场"},"item":[{"status":"10","startTime":"18:00","endTime":"18:30"}]}]}`)
	}))
	defer server.Close()

	restore := overrideAPIEndpoints("", server.URL, "", "", "")
	defer restore()

	response := performAPIRequest(t, "/api/getList", apiTestData.GetList)
	logAPIResponse(t, "/api/getList", response.Body.String())

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.Code)
	}
	if capturedAuth != "Bearer "+apiTestData.GetList["token"].(string) {
		t.Fatalf("unexpected authorization header: %q", capturedAuth)
	}
	if !strings.Contains(capturedQuery, "typeId=2") || !strings.Contains(capturedQuery, "params%5BmeetDate%5D=2026-07-03") {
		t.Fatalf("unexpected query string: %s", capturedQuery)
	}
	assertJSONField(t, response.Body.Bytes(), "statusCode", float64(200))
	assertJSONField(t, response.Body.Bytes(), "meetDate", "2026-07-03")
	assertNestedJSONField(t, response.Body.Bytes(), []string{"results", "0", "siteName"}, "1号场")
	assertNestedJSONField(t, response.Body.Bytes(), []string{"results", "0", "freeTimes", "0", "startTime"}, "18:00")
}

func TestReqAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var capturedAuth string
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll error: %v", err)
		}
		if err := json.Unmarshal(body, &capturedBody); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"msg":"initiate ok","data":{"recordId":115863}}`)
	}))
	defer server.Close()

	restore := overrideAPIEndpoints("", "", server.URL, "", "")
	defer restore()

	response := performAPIRequest(t, "/api/req", apiTestData.Req)
	logAPIResponse(t, "/api/req", response.Body.String())

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.Code)
	}
	if capturedAuth != "Bearer "+apiTestData.Req["token"].(string) {
		t.Fatalf("unexpected authorization header: %q", capturedAuth)
	}
	if _, exists := capturedBody["token"]; exists {
		t.Fatal("token should not be forwarded to upstream payload")
	}
	if capturedBody["siteId"] != float64(239) {
		t.Fatalf("unexpected siteId: %#v", capturedBody["siteId"])
	}
	assertJSONField(t, response.Body.Bytes(), "statusCode", float64(200))
	assertJSONField(t, response.Body.Bytes(), "requestURL", server.URL)
	assertNestedJSONField(t, response.Body.Bytes(), []string{"responseJson", "msg"}, "initiate ok")
}

func TestPayAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var capturedQuery string
	var capturedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"msg":"pay ok","data":{"paid":true}}`)
	}))
	defer server.Close()

	restore := overrideAPIEndpoints("", "", "", server.URL, "")
	defer restore()

	response := performAPIRequest(t, "/api/pay", apiTestData.Pay)
	logAPIResponse(t, "/api/pay", response.Body.String())

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.Code)
	}
	if capturedAuth != "Bearer "+apiTestData.Pay["token"].(string) {
		t.Fatalf("unexpected authorization header: %q", capturedAuth)
	}
	if !strings.Contains(capturedQuery, "recordId=115863") {
		t.Fatalf("unexpected query string: %s", capturedQuery)
	}
	assertJSONField(t, response.Body.Bytes(), "statusCode", float64(200))
	assertJSONField(t, response.Body.Bytes(), "requestURL", server.URL+"?recordId=115863")
	assertNestedJSONField(t, response.Body.Bytes(), []string{"responseJson", "msg"}, "pay ok")
}

func performAPIRequest(t *testing.T, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()

	engine := gin.New()
	RegisterKJYYRoutes(engine.Group("/api"))

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal request body error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

func logAPIResponse(t *testing.T, path, responseBody string) {
	t.Helper()
	t.Logf("%s response:\n%s", path, prettyJSON(responseBody))
}

func prettyJSON(raw string) string {
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, []byte(raw), "", "  "); err != nil {
		return raw
	}
	return formatted.String()
}

func assertJSONField(t *testing.T, body []byte, key string, want interface{}) {
	t.Helper()

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("Unmarshal response error: %v", err)
	}

	got, ok := decoded[key]
	if !ok {
		t.Fatalf("response missing key %q: %s", key, string(body))
	}
	if got != want {
		t.Fatalf("unexpected %s: got %#v want %#v", key, got, want)
	}
}

func assertNestedJSONField(t *testing.T, body []byte, path []string, want interface{}) {
	t.Helper()

	var decoded interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("Unmarshal response error: %v", err)
	}

	current := decoded
	for _, part := range path {
		switch node := current.(type) {
		case map[string]interface{}:
			var ok bool
			current, ok = node[part]
			if !ok {
				t.Fatalf("response missing key %q in path %v: %s", part, path, string(body))
			}
		case []interface{}:
			index := int(part[0] - '0')
			if index < 0 || index >= len(node) {
				t.Fatalf("response missing index %q in path %v: %s", part, path, string(body))
			}
			current = node[index]
		default:
			t.Fatalf("unexpected node type at path %v: %#v", path, current)
		}
	}

	if current != want {
		t.Fatalf("unexpected value at path %v: got %#v want %#v", path, current, want)
	}
}

func overrideAPIEndpoints(baseURL, listURL, reqURL, payURL, defaultServiceURL string) func() {
	oldBaseURL := defaultBaseURL
	oldListURL := kjyyListURL
	oldReqURL := kjyyInitiateURL
	oldPayURL := kjyyPayURL
	oldServiceURL := defaultKJYYServiceURL

	if baseURL != "" {
		defaultBaseURL = baseURL
	}
	if listURL != "" {
		kjyyListURL = listURL
	}
	if reqURL != "" {
		kjyyInitiateURL = reqURL
	}
	if payURL != "" {
		kjyyPayURL = payURL
	}
	if defaultServiceURL != "" {
		defaultKJYYServiceURL = defaultServiceURL
	}

	return func() {
		defaultBaseURL = oldBaseURL
		kjyyListURL = oldListURL
		kjyyInitiateURL = oldReqURL
		kjyyPayURL = oldPayURL
		defaultKJYYServiceURL = oldServiceURL
	}
}
