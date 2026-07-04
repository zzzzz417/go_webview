package api

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	defaultSalt           = "apkzIdhpPJhoplen"
	defaultRequestTimeout = 10 * time.Second
)

var (
	defaultBaseURL        = "https://auth.dgut.edu.cn/authserver"
	defaultKJYYServiceURL = "https://kjyy.dgut.edu.cn/prod-api/dgut/login/pcWx?type=2"
	kjyyListURL           = "https://kjyy.dgut.edu.cn/prod-api/yuyue/api/siteMeetList"
	kjyyInitiateURL       = "https://kjyy.dgut.edu.cn/prod-api/yuyue/api/initiate"
	kjyyPayURL            = "https://kjyy.dgut.edu.cn/prod-api/dgut/card/pay"
)

const aesChars = "ABCDEFGHJKMNPQRSTWXYZabcdefhijkmnprstwxyz2345678"

var (
	ltPattern       = regexp.MustCompile(`name=["']lt["'][^>]*value=["']([^"']*)["']`)
	execPattern     = regexp.MustCompile(`name=["']execution["'][^>]*value=["']([^"']*)["']`)
	saltIDPattern   = regexp.MustCompile(`id=["']pwdEncryptSalt["'][^>]*value=["']([^"']*)["']`)
	saltNamePattern = regexp.MustCompile(`name=["']pwdEncryptSalt["'][^>]*value=["']([^"']*)["']`)
	errorTipPattern = regexp.MustCompile(`id=["']showErrorTip["'][^>]*>\s*([^<]+?)\s*<`)
)

type casLoginState struct {
	LT             string
	Execution      string
	PwdEncryptSalt string
}

type dgutAuth struct {
	client  *http.Client
	baseURL string
	state   casLoginState
}

type kjyyClient struct {
	client     *http.Client
	serviceURL string
	casAuth    *dgutAuth
	token      string
}

type authRequest struct {
	Username   string `json:"username" binding:"required"`
	Password   string `json:"password" binding:"required"`
	ServiceURL string `json:"serviceUrl"`
}

type getListRequest struct {
	Token      string `json:"token" binding:"required"`
	SiteAssort string `json:"siteAssort" binding:"required"`
	SiteType   string `json:"siteType" binding:"required"`
	TypeID     int    `json:"typeId" binding:"required"`
	MeetDate   string `json:"meetDate" binding:"required"`
}

type payRequest struct {
	Token    string `json:"token" binding:"required"`
	RecordID int64  `json:"recordId" binding:"required"`
}

type apiError struct {
	Error string `json:"error"`
}

type listDisplayItem struct {
	SiteName          string              `json:"siteName"`
	SiteID            interface{}         `json:"siteId,omitempty"`
	MeetDate          string              `json:"meetDate"`
	FreeTimes         []listDisplayPeriod `json:"freeTimes"`
	HasAvailableSlots bool                `json:"hasAvailableSlots"`
}

type listDisplayPeriod struct {
	Index             int         `json:"index"`
	StartTime         string      `json:"startTime"`
	EndTime           string      `json:"endTime"`
	OrderMoney        interface{} `json:"orderMoney,omitempty"`
	AdditionalCharges interface{} `json:"additionalCharges,omitempty"`
}

func RegisterKJYYRoutes(group *gin.RouterGroup) {
	group.POST("/auth", authHandler)
	group.POST("/getList", getListHandler)
	group.POST("/req", initiateHandler)
	group.POST("/pay", payHandler)
	RegisterAppRoutes(group)
}

func authHandler(c *gin.Context) {
	var req authRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAppError(c, "bad request", nil)
		return
	}

	serviceURL := req.ServiceURL
	if serviceURL == "" {
		serviceURL = defaultKJYYServiceURL
	}

	snapshot, token, err := appService.login(req.Username, req.Password, serviceURL)
	if err != nil {
		writeAppError(c, err.Error(), appService.snapshotPtr())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":       true,
		"message":  "",
		"snapshot": snapshot,
		"token":    token,
	})
}

func getListHandler(c *gin.Context) {
	var req getListRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}

	params := url.Values{}
	params.Set("siteAssort", req.SiteAssort)
	params.Set("siteType", req.SiteType)
	params.Set("typeId", fmt.Sprintf("%d", req.TypeID))
	params.Set("params[meetDate]", req.MeetDate)

	resp, body, err := doKJYYRequest(http.MethodGet, kjyyListURL, req.Token, params, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, apiError{Error: err.Error()})
		return
	}
	defer resp.Body.Close()

	data, err := parseJSONBody(body)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"statusCode":   resp.StatusCode,
			"requestURL":   resp.Request.URL.String(),
			"responseText": string(body),
		})
		return
	}

	c.JSON(http.StatusOK, buildGetListDisplay(resp, req.MeetDate, data))
}

func initiateHandler(c *gin.Context) {
	var payload map[string]interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}

	token, _ := payload["token"].(string)
	if strings.TrimSpace(token) == "" {
		c.JSON(http.StatusBadRequest, apiError{Error: "token is required"})
		return
	}
	delete(payload, "token")

	resp, body, err := doKJYYRequest(http.MethodPost, kjyyInitiateURL, token, nil, payload)
	if err != nil {
		c.JSON(http.StatusBadGateway, apiError{Error: err.Error()})
		return
	}
	defer resp.Body.Close()

	writeRequestResult(c, resp, body)
}

func payHandler(c *gin.Context) {
	var req payRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}

	params := url.Values{}
	params.Set("recordId", fmt.Sprintf("%d", req.RecordID))

	resp, body, err := doKJYYRequest(http.MethodGet, kjyyPayURL, req.Token, params, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, apiError{Error: err.Error()})
		return
	}
	defer resp.Body.Close()

	writeRequestResult(c, resp, body)
}

func newKJYYClient(serviceURL string) (*kjyyClient, error) {
	client, err := newHTTPClient()
	if err != nil {
		return nil, err
	}

	casClient, err := newHTTPClient()
	if err != nil {
		return nil, err
	}

	return &kjyyClient{
		client:     client,
		serviceURL: serviceURL,
		casAuth: &dgutAuth{
			client:  casClient,
			baseURL: defaultBaseURL,
			state: casLoginState{
				Execution:      "e1s1",
				PwdEncryptSalt: defaultSalt,
			},
		},
	}, nil
}

func newHTTPClient() (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}

	return &http.Client{
		Timeout: defaultRequestTimeout,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func (k *kjyyClient) login(username, password string) (string, error) {
	ticket, err := k.casAuth.loginAndGetTicket(username, password, true, k.serviceURL)
	if err != nil {
		return "", err
	}

	callbackURL := k.serviceURL + "&ticket=" + url.QueryEscape(ticket)
	token, err := k.followRedirectsForToken(callbackURL)
	if err != nil {
		return "", err
	}

	k.token = token
	return token, nil
}

func (k *kjyyClient) followRedirectsForToken(callbackURL string) (string, error) {
	currentURL := callbackURL
	finalLocation := ""

	for range 5 {
		req, err := http.NewRequest(http.MethodGet, currentURL, nil)
		if err != nil {
			return "", err
		}
		applyKJYYHeaders(req, "")

		resp, err := k.client.Do(req)
		if err != nil {
			return "", err
		}
		resp.Body.Close()

		if resp.StatusCode < 300 || resp.StatusCode > 399 {
			break
		}

		location := resp.Header.Get("Location")
		if location == "" {
			break
		}

		finalLocation = location
		nextURL, err := url.Parse(currentURL)
		if err != nil {
			return "", err
		}
		relativeURL, err := url.Parse(location)
		if err != nil {
			return "", err
		}
		currentURL = nextURL.ResolveReference(relativeURL).String()
	}

	if finalLocation == "" {
		return "", fmt.Errorf("token not found in redirect location")
	}

	token := extractTokenFromLocation(finalLocation)
	if token == "" {
		return "", fmt.Errorf("token not found in redirect location")
	}

	return token, nil
}

func extractTokenFromLocation(location string) string {
	if !strings.Contains(location, "token=") {
		return ""
	}

	start := strings.Index(location, "token=") + len("token=")
	end := len(location)
	for _, sep := range []string{"&", "#"} {
		if idx := strings.Index(location[start:], sep); idx >= 0 && start+idx < end {
			end = start + idx
		}
	}

	token, err := url.QueryUnescape(location[start:end])
	if err != nil {
		return ""
	}
	return token
}

func (d *dgutAuth) loginAndGetTicket(username, password string, rememberMe bool, service string) (string, error) {
	if err := d.getLoginPage(service); err != nil {
		return "", err
	}

	form := url.Values{}
	for key, value := range d.buildLoginData(username, password, rememberMe) {
		form.Set(key, value)
	}

	loginURL := d.buildLoginURL(service)
	req, err := http.NewRequest(http.MethodPost, loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	applyCASHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		if msg := extractFirstMatch(errorTipPattern, string(body)); msg != "" {
			return "", errors.New(msg)
		}
		return "", fmt.Errorf("cas login failed with status %d", resp.StatusCode)
	}

	location := resp.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("missing redirect location")
	}

	redirectURL, err := url.Parse(location)
	if err != nil {
		return "", err
	}

	ticket := redirectURL.Query().Get("ticket")
	if ticket == "" {
		return "", fmt.Errorf("ticket not found in redirect location")
	}

	return ticket, nil
}

func (d *dgutAuth) getLoginPage(service string) error {
	loginURL := d.buildLoginURL(service)
	req, err := http.NewRequest(http.MethodGet, loginURL, nil)
	if err != nil {
		return err
	}
	applyCASHeaders(req)

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get login page failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	html := string(body)
	if v := extractFirstMatch(ltPattern, html); v != "" {
		d.state.LT = v
	}
	if v := extractFirstMatch(execPattern, html); v != "" {
		d.state.Execution = v
	}
	if v := extractFirstMatch(saltIDPattern, html); v != "" {
		d.state.PwdEncryptSalt = v
	} else if v := extractFirstMatch(saltNamePattern, html); v != "" {
		d.state.PwdEncryptSalt = v
	}

	return nil
}

func (d *dgutAuth) buildLoginData(username, password string, rememberMe bool) map[string]string {
	data := map[string]string{
		"username":  username,
		"password":  encryptPassword(password, d.state.PwdEncryptSalt),
		"_eventId":  "submit",
		"cllt":      "userNameLogin",
		"dllt":      "generalLogin",
		"lt":        d.state.LT,
		"execution": d.state.Execution,
	}
	if rememberMe {
		data["rememberMe"] = "true"
	}
	return data
}

func (d *dgutAuth) buildLoginURL(service string) string {
	if service == "" {
		return d.baseURL + "/login"
	}
	return d.baseURL + "/login?service=" + url.QueryEscape(service)
}

func doKJYYRequest(method, rawURL, token string, params url.Values, payload interface{}) (*http.Response, []byte, error) {
	if params != nil && len(params) > 0 {
		rawURL += "?" + params.Encode()
	}

	var bodyReader io.Reader
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, err
		}
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, rawURL, bodyReader)
	if err != nil {
		return nil, nil, err
	}
	applyKJYYHeaders(req, token)

	client, err := newHTTPClient()
	if err != nil {
		return nil, nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		return nil, nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	return resp, body, nil
}

func writeRequestResult(c *gin.Context, resp *http.Response, body []byte) {
	c.JSON(http.StatusOK, buildProxyResponse(resp, body))
}

type proxyResponse struct {
	StatusCode   int         `json:"statusCode"`
	RequestURL   string      `json:"requestURL,omitempty"`
	ResponseJSON interface{} `json:"responseJson,omitempty"`
	ResponseText string      `json:"responseText,omitempty"`
}

func buildProxyResponse(resp *http.Response, body []byte) *proxyResponse {
	result := &proxyResponse{
		StatusCode:   resp.StatusCode,
		RequestURL:   resp.Request.URL.String(),
		ResponseText: string(body),
	}

	var data interface{}
	if err := json.Unmarshal(body, &data); err == nil {
		result.ResponseJSON = data
	}

	return result
}

func parseJSONBody(body []byte) (map[string]interface{}, error) {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func buildGetListDisplay(resp *http.Response, meetDate string, data map[string]interface{}) gin.H {
	results := make([]listDisplayItem, 0)
	courts, _ := data["data"].([]interface{})

	for _, courtValue := range courts {
		court, ok := courtValue.(map[string]interface{})
		if !ok {
			continue
		}

		site, _ := court["site"].(map[string]interface{})
		item := listDisplayItem{
			SiteName:          stringValue(site["siteName"]),
			SiteID:            site["siteId"],
			MeetDate:          meetDate,
			FreeTimes:         make([]listDisplayPeriod, 0),
			HasAvailableSlots: false,
		}

		periods, _ := court["item"].([]interface{})
		for _, periodValue := range periods {
			period, ok := periodValue.(map[string]interface{})
			if !ok || stringValue(period["status"]) != "10" {
				continue
			}

			item.HasAvailableSlots = true
			item.FreeTimes = append(item.FreeTimes, listDisplayPeriod{
				Index:             len(item.FreeTimes) + 1,
				StartTime:         stringValue(period["startTime"]),
				EndTime:           stringValue(period["endTime"]),
				OrderMoney:        firstNonNil(period["hourCharge"], period["orderMoney"]),
				AdditionalCharges: firstNonNil(period["itemTeaching"], period["additionalCharges"], float64(0)),
			})
		}

		results = append(results, item)
	}

	return gin.H{
		"statusCode":   resp.StatusCode,
		"requestURL":   resp.Request.URL.String(),
		"meetDate":     meetDate,
		"results":      results,
		"responseJson": data,
	}
}

func stringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

func firstNonNil(values ...interface{}) interface{} {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func applyCASHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
}

func applyKJYYHeaders(req *http.Request, token string) {
	req.Header.Set("Host", "kjyy.dgut.edu.cn")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36 MicroMessenger/7.0.20.1781(0x6700143B) NetType/WIFI MiniProgramEnv/Windows WindowsWechat/WMPF WindowsWechat(0x63090a13) UnifiedPCWindowsWechat(0xf2541a1f) XWEB/19921")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Referer", "https://servicewechat.com/wx4c31527472b00a49/77/page-frame.html")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("xweb_xhr", "1")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func extractFirstMatch(pattern *regexp.Regexp, input string) string {
	matches := pattern.FindStringSubmatch(input)
	if len(matches) < 2 {
		return ""
	}
	return htmlUnescape(matches[1])
}

func htmlUnescape(s string) string {
	replacer := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'")
	return replacer.Replace(s)
}

func encryptPassword(password, salt string) string {
	randomPrefix := randomString(64)
	iv := randomString(16)
	plaintext := []byte(randomPrefix + password)

	block, err := aes.NewCipher([]byte(salt))
	if err != nil {
		return ""
	}

	padded := pkcs7Pad(plaintext, aes.BlockSize)
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(iv)).CryptBlocks(encrypted, padded)
	return base64.StdEncoding.EncodeToString(encrypted)
}

func randomString(length int) string {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	var builder strings.Builder
	builder.Grow(length)
	for i := 0; i < length; i++ {
		builder.WriteByte(aesChars[rng.Intn(len(aesChars))])
	}
	return builder.String()
}

func pkcs7Pad(src []byte, blockSize int) []byte {
	padding := blockSize - len(src)%blockSize
	return append(src, bytes.Repeat([]byte{byte(padding)}, padding)...)
}
