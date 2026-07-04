package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type mockGateway struct {
	mu            sync.Mutex
	token         string
	authErr       error
	getListByDate map[string]*meetListResult
	payResponse   *proxyResponse
	getListCalls  []string
	initiateCalls []map[string]interface{}
	payCalls      []int64
}

func (m *mockGateway) Auth(username, password, serviceURL string) (string, error) {
	if m.authErr != nil {
		return "", m.authErr
	}
	return m.token, nil
}

func (m *mockGateway) GetList(token, meetDate string) (*meetListResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getListCalls = append(m.getListCalls, meetDate)
	if result, ok := m.getListByDate[meetDate]; ok {
		return result, nil
	}
	return &meetListResult{
		StatusCode:   200,
		RequestURL:   "mock://getList",
		ResponseJSON: map[string]interface{}{"code": 200, "data": []interface{}{}},
		ResponseText: `{"code":200,"data":[]}`,
		Sites:        []meetSite{},
	}, nil
}

func (m *mockGateway) Initiate(token string, payload map[string]interface{}) (*proxyResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initiateCalls = append(m.initiateCalls, payload)
	return &proxyResponse{
		StatusCode:   200,
		RequestURL:   "mock://initiate",
		ResponseJSON: map[string]interface{}{"code": 200, "data": map[string]interface{}{"recordId": float64(9988)}},
		ResponseText: `{"code":200,"data":{"recordId":9988}}`,
	}, nil
}

func (m *mockGateway) Pay(token string, recordID int64) (*proxyResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.payCalls = append(m.payCalls, recordID)
	if m.payResponse != nil {
		return m.payResponse, nil
	}
	return &proxyResponse{
		StatusCode:   200,
		RequestURL:   "mock://pay",
		ResponseJSON: map[string]interface{}{"code": 200, "msg": "ok"},
		ResponseText: `{"code":200,"msg":"ok"}`,
	}, nil
}

func (m *mockGateway) UserAgent() string {
	return "mock-agent"
}

func TestStateInitialSnapshot(t *testing.T) {
	mock := &mockGateway{token: "mock-token", getListByDate: map[string]*meetListResult{}}
	restore := replaceAppService(newBookingAppWithPersistence(mock, false))
	defer restore()

	response := performAppRequest(t, http.MethodGet, "/api/state", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}

	assertJSONField(t, response.Body.Bytes(), "ok", true)
	assertNestedJSONField(t, response.Body.Bytes(), []string{"snapshot", "auth", "loggedIn"}, false)
}

func TestAppAuthReturnsSnapshot(t *testing.T) {
	mock := &mockGateway{token: "mock-token", getListByDate: weekData("2026-07-06")}
	app := newBookingAppWithPersistence(mock, false)
	app.now = func() time.Time { return mustTime("2026-07-06T09:00:00") }
	restore := replaceAppService(app)
	defer restore()

	response := performAppRequest(t, http.MethodPost, "/api/auth", map[string]interface{}{
		"username": "20260001",
		"password": "secret",
	})

	assertJSONField(t, response.Body.Bytes(), "ok", true)
	assertNestedJSONField(t, response.Body.Bytes(), []string{"snapshot", "auth", "loggedIn"}, true)
	assertNestedJSONField(t, response.Body.Bytes(), []string{"snapshot", "auth", "token"}, "mock-token")
}

func TestWeekSelectionEndpoints(t *testing.T) {
	mock := &mockGateway{token: "mock-token", getListByDate: weekData("2026-07-06")}
	app := newBookingAppWithPersistence(mock, false)
	app.now = func() time.Time { return mustTime("2026-07-06T09:00:00") }
	restore := replaceAppService(app)
	defer restore()

	_, _, err := app.login("20260001", "secret", defaultKJYYServiceURL)
	if err != nil {
		t.Fatalf("login error: %v", err)
	}

	weekResp := performAppRequest(t, http.MethodPost, "/api/week", map[string]interface{}{"weekStart": "2026-07-06"})
	assertJSONField(t, weekResp.Body.Bytes(), "ok", true)
	assertNestedJSONField(t, weekResp.Body.Bytes(), []string{"snapshot", "week", "sites", "0", "siteName"}, "1号场")

	toggleResp := performAppRequest(t, http.MethodPost, "/api/selection/toggle", map[string]interface{}{
		"siteId":  239,
		"weekday": 0,
		"startHm": "18:00",
		"endHm":   "18:30",
		"checked": true,
	})
	assertJSONField(t, toggleResp.Body.Bytes(), "ok", true)
	assertNestedJSONField(t, toggleResp.Body.Bytes(), []string{"snapshot", "patterns", "0", "siteName"}, "1号场")

	removeResp := performAppRequest(t, http.MethodPost, "/api/selection/remove", map[string]interface{}{
		"keys": []string{"239|0|18:00|18:30"},
	})
	assertJSONField(t, removeResp.Body.Bytes(), "ok", true)
	assertNestedArrayLength(t, removeResp.Body.Bytes(), []string{"snapshot", "patterns"}, 0)
}

func TestCanSelectUnavailableAndEmptyCells(t *testing.T) {
	mock := &mockGateway{token: "mock-token", getListByDate: sparseWeekData("2026-07-06")}
	app := newBookingAppWithPersistence(mock, false)
	app.now = func() time.Time { return mustTime("2026-07-06T09:00:00") }
	restore := replaceAppService(app)
	defer restore()

	_, _, err := app.login("20260001", "secret", defaultKJYYServiceURL)
	if err != nil {
		t.Fatalf("login error: %v", err)
	}
	if _, err := app.setWeek("2026-07-06"); err != nil {
		t.Fatalf("setWeek error: %v", err)
	}

	occupiedResp := performAppRequest(t, http.MethodPost, "/api/selection/toggle", map[string]interface{}{
		"siteId":  239,
		"weekday": 0,
		"startHm": "18:00",
		"endHm":   "18:30",
		"checked": true,
	})
	assertJSONField(t, occupiedResp.Body.Bytes(), "ok", true)

	emptyResp := performAppRequest(t, http.MethodPost, "/api/selection/toggle", map[string]interface{}{
		"siteId":  239,
		"weekday": 0,
		"startHm": "19:00",
		"endHm":   "19:30",
		"checked": true,
	})
	assertJSONField(t, emptyResp.Body.Bytes(), "ok", true)
	assertNestedArrayLength(t, emptyResp.Body.Bytes(), []string{"snapshot", "patterns"}, 2)
}

func TestBookingStartStopAndWorker(t *testing.T) {
	mock := &mockGateway{token: "mock-token", getListByDate: weekData("2026-07-06")}
	app := newBookingAppWithPersistence(mock, false)
	app.now = func() time.Time { return mustTime("2026-07-06T09:00:00") }
	app.pollInterval = 20 * time.Millisecond
	restore := replaceAppService(app)
	defer restore()

	_, _, err := app.login("20260001", "secret", defaultKJYYServiceURL)
	if err != nil {
		t.Fatalf("login error: %v", err)
	}
	if _, err := app.setWeek("2026-07-06"); err != nil {
		t.Fatalf("setWeek error: %v", err)
	}

	if _, err := app.setManualDate("2026-07-06"); err != nil {
		t.Fatalf("setManualDate error: %v", err)
	}
	if _, err := app.setMode("manual"); err != nil {
		t.Fatalf("setMode error: %v", err)
	}
	if _, err := app.toggleSelection(selectionToggleRequest{
		SiteID:  239,
		Weekday: 0,
		StartHM: "18:00",
		EndHM:   "18:30",
		Checked: true,
	}); err != nil {
		t.Fatalf("toggleSelection error: %v", err)
	}

	startResp := performAppRequest(t, http.MethodPost, "/api/booking/start", map[string]interface{}{})
	assertJSONField(t, startResp.Body.Bytes(), "ok", true)
	assertNestedJSONField(t, startResp.Body.Bytes(), []string{"snapshot", "settings", "running"}, true)

	waitFor(t, time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.initiateCalls) == 1 && len(mock.payCalls) == 1
	})

	stateResp := performAppRequest(t, http.MethodGet, "/api/state", nil)
	assertNestedJSONField(t, stateResp.Body.Bytes(), []string{"snapshot", "patterns", "0", "statusKey"}, "purchased")

	stopResp := performAppRequest(t, http.MethodPost, "/api/booking/stop", map[string]interface{}{})
	assertJSONField(t, stopResp.Body.Bytes(), "ok", true)
	assertNestedJSONField(t, stopResp.Body.Bytes(), []string{"snapshot", "settings", "running"}, false)
}

func TestPingAndLoginAlias(t *testing.T) {
	mock := &mockGateway{token: "mock-token", getListByDate: weekData("2026-07-06")}
	app := newBookingAppWithPersistence(mock, false)
	app.now = func() time.Time { return mustTime("2026-07-06T09:00:00") }
	restore := replaceAppService(app)
	defer restore()

	pingResp := performAppRequest(t, http.MethodGet, "/api/ping", nil)
	assertJSONField(t, pingResp.Body.Bytes(), "ok", true)

	loginResp := performAppRequest(t, http.MethodPost, "/api/login", map[string]interface{}{
		"username": "20260001",
		"password": "secret",
	})
	assertJSONField(t, loginResp.Body.Bytes(), "ok", true)
	assertNestedJSONField(t, loginResp.Body.Bytes(), []string{"snapshot", "auth", "loggedIn"}, true)
}

func TestRunningBlocksMutations(t *testing.T) {
	mock := &mockGateway{token: "mock-token", getListByDate: weekData("2026-07-06")}
	app := newBookingAppWithPersistence(mock, false)
	app.now = func() time.Time { return mustTime("2026-07-06T09:00:00") }
	restore := replaceAppService(app)
	defer restore()

	_, _, err := app.login("20260001", "secret", defaultKJYYServiceURL)
	if err != nil {
		t.Fatalf("login error: %v", err)
	}
	if _, err := app.setWeek("2026-07-06"); err != nil {
		t.Fatalf("setWeek error: %v", err)
	}
	if _, err := app.toggleSelection(selectionToggleRequest{
		SiteID:  239,
		Weekday: 0,
		StartHM: "18:00",
		EndHM:   "18:30",
		Checked: true,
	}); err != nil {
		t.Fatalf("toggleSelection error: %v", err)
	}

	if _, err := app.startBooking(); err != nil {
		t.Fatalf("startBooking error: %v", err)
	}
	defer app.stopBooking()

	resp := performAppRequest(t, http.MethodPost, "/api/settings/mode", map[string]interface{}{"mode": "manual"})
	assertJSONField(t, resp.Body.Bytes(), "ok", false)
	assertJSONField(t, resp.Body.Bytes(), "message", "请先停止预约程序，再进行更改")
}

func TestResolvePatternTargetDateKeepsTodaySelection(t *testing.T) {
	now := mustTime("2026-07-10T09:00:00")
	pattern := selectionPattern{
		SiteID:       239,
		SiteName:     "1号场",
		WeekdayIndex: 4,
		WeekdayLabel: "周五",
		MeetDate:     "2026-07-10",
		StartHM:      "18:00",
		EndHM:        "18:30",
	}

	got := resolvePatternTargetDate(pattern, "rule", "", "2026-07-13", now)
	if got != "2026-07-10" {
		t.Fatalf("expected current selected date to be kept, got %s", got)
	}
}

func TestResolvePatternTargetDateRollsPastSelectionByWeek(t *testing.T) {
	now := mustTime("2026-07-10T09:00:00")
	pattern := selectionPattern{
		SiteID:       239,
		SiteName:     "1号场",
		WeekdayIndex: 0,
		WeekdayLabel: "周一",
		MeetDate:     "2026-07-06",
		StartHM:      "18:00",
		EndHM:        "18:30",
	}

	got := resolvePatternTargetDate(pattern, "rule", "", "2026-07-13", now)
	if got != "2026-07-13" {
		t.Fatalf("expected past selection to roll by 7 days, got %s", got)
	}
}

func TestExtractRecordIDSupportsDifferentShapes(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want int64
	}{
		{
			name: "nested recordId",
			in:   map[string]interface{}{"data": map[string]interface{}{"recordId": float64(115863)}},
			want: 115863,
		},
		{
			name: "root recordId",
			in:   map[string]interface{}{"recordId": "9988"},
			want: 9988,
		},
		{
			name: "data is number",
			in:   map[string]interface{}{"data": float64(5566)},
			want: 5566,
		},
	}

	for _, tc := range cases {
		if got := extractRecordID(tc.in); got != tc.want {
			t.Fatalf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}

func TestPatternDisplayStateMapping(t *testing.T) {
	app := newBookingAppWithPersistence(&mockGateway{token: "mock-token", getListByDate: map[string]*meetListResult{}}, false)

	app.state.settings.Running = false
	if status, key := app.patternDisplayStateLocked(&selectionPattern{StatusKey: "polling"}); status != "未预约" || key != "unbooked" {
		t.Fatalf("expected non-running polling rule to display 未预约/unbooked, got %s/%s", status, key)
	}
	if status, key := app.patternDisplayStateLocked(&selectionPattern{StatusKey: "sold_out"}); status != "未预约" || key != "unbooked" {
		t.Fatalf("expected non-running sold_out rule to display 未预约/unbooked, got %s/%s", status, key)
	}
	if status, key := app.patternDisplayStateLocked(&selectionPattern{StatusKey: "pending_release"}); status != "预开售" || key != "pending_release" {
		t.Fatalf("expected pending_release rule to display 预开售/pending_release, got %s/%s", status, key)
	}
	if status, key := app.patternDisplayStateLocked(&selectionPattern{StatusKey: "purchased"}); status != "已预约" || key != "purchased" {
		t.Fatalf("expected purchased rule to display 已预约/purchased, got %s/%s", status, key)
	}

	app.state.settings.Running = true
	if status, key := app.patternDisplayStateLocked(&selectionPattern{StatusKey: "high_priority"}); status != "轮询中" || key != "polling" {
		t.Fatalf("expected running high_priority rule to display 轮询中/polling, got %s/%s", status, key)
	}
}

func TestRestoreSavedSessionWithPersistedToken(t *testing.T) {
	mock := &mockGateway{token: "mock-token", getListByDate: weekData("2026-07-06")}
	app := newBookingAppWithPersistence(mock, false)
	app.now = func() time.Time { return mustTime("2026-07-06T09:00:00") }
	app.persist = true
	app.configPath = filepath.Join(t.TempDir(), "config.json")

	cfg := persistedConfig{
		Username:   "20260001",
		Password:   "secret",
		ServiceURL: defaultKJYYServiceURL,
		Token:      "persisted-token",
		WeekStart:  "2026-07-06",
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config error: %v", err)
	}
	if err := os.WriteFile(app.configPath, data, 0o644); err != nil {
		t.Fatalf("write config error: %v", err)
	}

	app.loadConfig()
	app.restoreSavedSession()

	if !app.state.auth.LoggedIn {
		t.Fatal("expected saved token session to be restored as logged in")
	}
	if app.state.auth.Token != "persisted-token" {
		t.Fatalf("expected persisted token to be kept, got %s", app.state.auth.Token)
	}
}

func TestRestoreSavedSessionWithCredentialsOnly(t *testing.T) {
	mock := &mockGateway{token: "fresh-token", getListByDate: weekData("2026-07-06")}
	app := newBookingAppWithPersistence(mock, false)
	app.now = func() time.Time { return mustTime("2026-07-06T09:00:00") }
	app.persist = true
	app.configPath = filepath.Join(t.TempDir(), "config.json")

	cfg := persistedConfig{
		Username:   "20260001",
		Password:   "secret",
		ServiceURL: defaultKJYYServiceURL,
		WeekStart:  "2026-07-06",
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config error: %v", err)
	}
	if err := os.WriteFile(app.configPath, data, 0o644); err != nil {
		t.Fatalf("write config error: %v", err)
	}

	app.loadConfig()
	app.restoreSavedSession()

	if !app.state.auth.LoggedIn {
		t.Fatal("expected saved credentials to auto login on startup")
	}
	if app.state.auth.Token != "fresh-token" {
		t.Fatalf("expected refreshed token, got %s", app.state.auth.Token)
	}
}

func TestReloginClearsPersistedSession(t *testing.T) {
	mock := &mockGateway{token: "mock-token", getListByDate: weekData("2026-07-06")}
	app := newBookingAppWithPersistence(mock, false)
	app.now = func() time.Time { return mustTime("2026-07-06T09:00:00") }
	restore := replaceAppService(app)
	defer restore()

	if _, _, err := app.login("20260001", "secret", defaultKJYYServiceURL); err != nil {
		t.Fatalf("login error: %v", err)
	}

	resp := performAppRequest(t, http.MethodPost, "/api/relogin", map[string]interface{}{})
	assertJSONField(t, resp.Body.Bytes(), "ok", true)
	assertNestedJSONField(t, resp.Body.Bytes(), []string{"snapshot", "auth", "loggedIn"}, false)
	assertNestedJSONField(t, resp.Body.Bytes(), []string{"snapshot", "auth", "hasSavedCredentials"}, false)
	assertNestedArrayLength(t, resp.Body.Bytes(), []string{"snapshot", "patterns"}, 0)
}

func TestMarkAttemptWithNoticeLockedGeneratesPerPatternPollingNotice(t *testing.T) {
	app := newBookingAppWithPersistence(&mockGateway{token: "mock-token", getListByDate: map[string]*meetListResult{}}, false)
	now := mustTime("2026-07-03T09:00:00")
	openAt := mustTime("2026-07-03T08:00:00")

	patternA := &selectionPattern{
		SiteName:          "网1号场",
		StartHM:           "18:00",
		EndHM:             "18:30",
		CurrentTargetDate: "2026-07-09",
	}
	patternB := &selectionPattern{
		SiteName:          "网2号场",
		StartHM:           "19:00",
		EndHM:             "19:30",
		CurrentTargetDate: "2026-07-09",
	}

	noticeA := app.markAttemptWithNoticeLocked(patternA, now, openAt)
	noticeB := app.markAttemptWithNoticeLocked(patternB, now, openAt)
	noticeA2 := app.markAttemptWithNoticeLocked(patternA, now.Add(regularRetryInterval), openAt)

	if noticeA == "" || noticeB == "" {
		t.Fatal("expected each pattern to emit its own initial polling notice")
	}
	if noticeA2 != "" {
		t.Fatalf("expected repeated polling for same pattern/date to avoid duplicate notice, got %q", noticeA2)
	}
}

func TestCalculateOpenAtUsesConfiguredMinute(t *testing.T) {
	openAt := calculateOpenAt("2026-07-09")
	want := time.Date(2026, 7, 3, openHour, openMinute, 0, 0, time.Local).Format("2006-01-02 15:04:05")
	if openAt.Format("2006-01-02 15:04:05") != want {
		t.Fatalf("unexpected openAt: %s", openAt.Format("2006-01-02 15:04:05"))
	}
}

func TestValidateProxyBusinessOKRejectsBusinessFailure(t *testing.T) {
	err := validateProxyBusinessOK(&proxyResponse{
		StatusCode:   200,
		ResponseJSON: map[string]interface{}{"code": float64(500), "msg": "余额不足"},
	})
	if err == nil {
		t.Fatal("expected business failure to be rejected")
	}
}

func TestBookingWorkerMarksPayBusinessFailure(t *testing.T) {
	mock := &mockGateway{
		token:         "mock-token",
		getListByDate: weekData("2026-07-06"),
		payResponse: &proxyResponse{
			StatusCode:   200,
			RequestURL:   "mock://pay",
			ResponseJSON: map[string]interface{}{"code": float64(500), "msg": "余额不足"},
			ResponseText: `{"code":500,"msg":"余额不足","data":"500"}`,
		},
	}
	app := newBookingAppWithPersistence(mock, false)
	app.now = func() time.Time { return mustTime("2026-07-06T09:00:00") }
	app.pollInterval = 20 * time.Millisecond
	restore := replaceAppService(app)
	defer restore()

	_, _, err := app.login("20260001", "secret", defaultKJYYServiceURL)
	if err != nil {
		t.Fatalf("login error: %v", err)
	}
	if _, err := app.setWeek("2026-07-06"); err != nil {
		t.Fatalf("setWeek error: %v", err)
	}
	if _, err := app.setManualDate("2026-07-06"); err != nil {
		t.Fatalf("setManualDate error: %v", err)
	}
	if _, err := app.setMode("manual"); err != nil {
		t.Fatalf("setMode error: %v", err)
	}
	if _, err := app.toggleSelection(selectionToggleRequest{
		SiteID:  239,
		Weekday: 0,
		StartHM: "18:00",
		EndHM:   "18:30",
		Checked: true,
	}); err != nil {
		t.Fatalf("toggleSelection error: %v", err)
	}

	if _, err := app.startBooking(); err != nil {
		t.Fatalf("startBooking error: %v", err)
	}
	defer app.stopBooking()

	waitFor(t, time.Second, func() bool {
		app.mu.Lock()
		defer app.mu.Unlock()
		pattern := app.state.patterns["239|0|18:00|18:30"]
		return pattern != nil && pattern.Status == "支付失败"
	})
}

func TestHighPriorityLabelUsesConfiguredTime(t *testing.T) {
	openAt := mustTime("2026-07-03T18:22:00")
	if label := highPriorityLabel(openAt); label != "18:22" {
		t.Fatalf("unexpected high priority label: %s", label)
	}
}

func performAppRequest(t *testing.T, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	RegisterKJYYRoutes(engine.Group("/api"))

	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

func replaceAppService(app *bookingApp) func() {
	old := appService
	appService = app
	return func() {
		appService.stopBooking()
		appService = old
	}
}

func weekData(weekStart string) map[string]*meetListResult {
	start := mustDate(weekStart)
	results := map[string]*meetListResult{}
	for i := 0; i < 7; i++ {
		meetDate := start.AddDate(0, 0, i).Format("2006-01-02")
		results[meetDate] = &meetListResult{
			StatusCode:   200,
			RequestURL:   "mock://getList?date=" + meetDate,
			ResponseJSON: map[string]interface{}{"code": 200},
			ResponseText: `{"code":200}`,
			Sites: []meetSite{
				{
					SiteID:   239,
					SiteName: "1号场",
					Periods: []meetPeriod{
						{
							SiteID:            239,
							SiteName:          "1号场",
							MeetDate:          meetDate,
							Status:            "10",
							StartHM:           "18:00",
							EndHM:             "18:30",
							HourCharge:        float64(7.5),
							AdditionalCharges: float64(0),
						},
					},
				},
			},
		}
	}
	return results
}

func sparseWeekData(weekStart string) map[string]*meetListResult {
	start := mustDate(weekStart)
	results := map[string]*meetListResult{}
	for i := 0; i < 7; i++ {
		meetDate := start.AddDate(0, 0, i).Format("2006-01-02")
		sites := []meetSite{
			{
				SiteID:   239,
				SiteName: "1号场",
				Periods:  []meetPeriod{},
			},
		}
		if i == 0 {
			sites[0].Periods = append(sites[0].Periods, meetPeriod{
				SiteID:            239,
				SiteName:          "1号场",
				MeetDate:          meetDate,
				Status:            "20",
				StartHM:           "18:00",
				EndHM:             "18:30",
				HourCharge:        float64(7.5),
				AdditionalCharges: float64(0),
			})
		}
		if i == 1 {
			sites[0].Periods = append(sites[0].Periods, meetPeriod{
				SiteID:            239,
				SiteName:          "1号场",
				MeetDate:          meetDate,
				Status:            "10",
				StartHM:           "19:00",
				EndHM:             "19:30",
				HourCharge:        float64(7.5),
				AdditionalCharges: float64(0),
			})
		}
		results[meetDate] = &meetListResult{
			StatusCode:   200,
			RequestURL:   "mock://getList?date=" + meetDate,
			ResponseJSON: map[string]interface{}{"code": 200},
			ResponseText: `{"code":200}`,
			Sites:        sites,
		}
	}
	return results
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not satisfied before timeout")
}

func mustDate(value string) time.Time {
	result, err := time.Parse("2006-01-02", value)
	if err != nil {
		panic(err)
	}
	return result
}

func mustTime(value string) time.Time {
	result, err := time.Parse("2006-01-02T15:04:05", value)
	if err != nil {
		panic(err)
	}
	return result
}

func assertNestedArrayLength(t *testing.T, body []byte, path []string, want int) {
	t.Helper()
	var decoded interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("Unmarshal response error: %v", err)
	}

	current := decoded
	for _, part := range path {
		node := current.(map[string]interface{})
		current = node[part]
	}

	array, ok := current.([]interface{})
	if !ok {
		t.Fatalf("value at path %v is not an array", path)
	}
	if len(array) != want {
		t.Fatalf("unexpected array length at path %v: got %d want %d", path, len(array), want)
	}
}
