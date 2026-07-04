package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	defaultSiteAssort = "室外"
	defaultSiteType   = "松山湖网球场"
	defaultTypeID     = 2
	defaultPollDelay  = 1 * time.Second
	defaultCooldown   = 90 * time.Second
	maxLogEntries     = 200

	openAdvanceDays         = 6
	openHour                = 8
	openMinute              = 0
	highFreqWindow          = 10 * time.Minute
	highFreqInterval        = 3 * time.Second
	highFreqMaxAttempts     = 200
	regularRetryInterval    = 15 * time.Second
	maxAttemptsPer30Minutes = 10
	rateLimitWindow         = 30 * time.Minute
	failuresToMarkSoldOut   = 2
)

var appService = newBookingApp(realGateway{})
var appBootstrapOnce sync.Once

var errAuthExpired = errors.New("auth expired")

type gateway interface {
	Auth(username, password, serviceURL string) (string, error)
	GetList(token, meetDate string) (*meetListResult, error)
	Initiate(token string, payload map[string]interface{}) (*proxyResponse, error)
	Pay(token string, recordID int64) (*proxyResponse, error)
	UserAgent() string
}

type bookingApp struct {
	mu            sync.Mutex
	gateway       gateway
	now           func() time.Time
	pollInterval  time.Duration
	cooldownDelay time.Duration
	configPath    string
	persist       bool
	state         appState
	stopCh        chan struct{}
	doneCh        chan struct{}
}

type appState struct {
	username   string
	password   string
	serviceURL string

	auth     authSnapshot
	settings settingsSnapshot
	week     weekSnapshot
	patterns map[string]*selectionPattern
	logs     []logEntry
	status   string
}

type authSnapshot struct {
	LoggedIn            bool   `json:"loggedIn"`
	HasSavedCredentials bool   `json:"hasSavedCredentials"`
	Username            string `json:"username"`
	Token               string `json:"token"`
	Bearer              string `json:"bearer"`
	UserAgent           string `json:"userAgent"`
}

type settingsSnapshot struct {
	Mode            string `json:"mode"`
	ManualDate      string `json:"manualDate"`
	RuleHint        string `json:"ruleHint"`
	SoldOutStrategy string `json:"soldOutStrategy"`
	Running         bool   `json:"running"`
}

type weekSnapshot struct {
	Sites          []siteOption        `json:"sites"`
	Timeslots      [][]string          `json:"timeslots"`
	Cells          map[string]weekCell `json:"cells"`
	SelectedSiteID int                 `json:"selectedSiteId"`
	WeekStart      string              `json:"weekStart"`
}

type siteOption struct {
	SiteID   int    `json:"siteId"`
	SiteName string `json:"siteName"`
}

type weekCell struct {
	Checked           bool        `json:"checked"`
	Disabled          bool        `json:"disabled"`
	StatusKey         string      `json:"statusKey"`
	RawStatus         string      `json:"rawStatus"`
	SiteID            int         `json:"siteId"`
	SiteName          string      `json:"siteName"`
	Weekday           int         `json:"weekday"`
	MeetDate          string      `json:"meetDate"`
	StartHM           string      `json:"startHm"`
	EndHM             string      `json:"endHm"`
	OrderMoney        interface{} `json:"orderMoney,omitempty"`
	AdditionalCharges interface{} `json:"additionalCharges,omitempty"`
}

type selectionPattern struct {
	Key               string
	SiteID            int
	SiteName          string
	WeekdayIndex      int
	WeekdayLabel      string
	MeetDate          string
	StartHM           string
	EndHM             string
	OrderMoney        interface{}
	AdditionalCharges interface{}
	Status            string
	StatusKey         string
	Message           string
	FailureCount      int
	CooldownUntil     time.Time
	LastAttemptAt     time.Time
	WindowStartedAt   time.Time
	AttemptsInWindow  int
	DisabledForCycle  string
	CurrentTargetDate string
	LastNoticeKey     string
}

type patternSnapshot struct {
	Key       string `json:"key"`
	SiteName  string `json:"siteName"`
	Weekday   string `json:"weekday"`
	TimeRange string `json:"timeRange"`
	Status    string `json:"status"`
	StatusKey string `json:"statusKey"`
	Message   string `json:"message"`
}

type logEntry struct {
	Time    string `json:"time"`
	Message string `json:"message"`
}

type appSnapshot struct {
	Auth       authSnapshot      `json:"auth"`
	Settings   settingsSnapshot  `json:"settings"`
	Week       weekSnapshot      `json:"week"`
	Patterns   []patternSnapshot `json:"patterns"`
	Logs       []logEntry        `json:"logs"`
	StatusText string            `json:"statusText"`
}

type stateResponse struct {
	OK       bool         `json:"ok"`
	Message  string       `json:"message"`
	Snapshot *appSnapshot `json:"snapshot"`
}

type siteSelectionRequest struct {
	SiteID int `json:"siteId"`
}

type weekRequest struct {
	WeekStart string `json:"weekStart"`
}

type pingResponse struct {
	OK bool `json:"ok"`
}

type selectionToggleRequest struct {
	SiteID  int    `json:"siteId"`
	Weekday int    `json:"weekday"`
	StartHM string `json:"startHm"`
	EndHM   string `json:"endHm"`
	Checked bool   `json:"checked"`
}

type selectionRemoveRequest struct {
	Keys []string `json:"keys"`
}

type modeRequest struct {
	Mode string `json:"mode"`
}

type manualDateRequest struct {
	ManualDate string `json:"manualDate"`
}

type soldOutStrategyRequest struct {
	SoldOutStrategy string `json:"soldOutStrategy"`
}

func newBookingApp(g gateway) *bookingApp {
	return newBookingAppWithPersistence(g, true)
}

func newBookingAppWithPersistence(g gateway, persist bool) *bookingApp {
	now := time.Now
	weekStart := mondayOf(now()).Format("2006-01-02")
	manualDate := now().Format("2006-01-02")

	app := &bookingApp{
		gateway:       g,
		now:           now,
		pollInterval:  defaultPollDelay,
		cooldownDelay: defaultCooldown,
		configPath:    discoverConfigPath(),
		persist:       persist,
		state: appState{
			auth: authSnapshot{
				UserAgent: g.UserAgent(),
			},
			settings: settingsSnapshot{
				Mode:            "rule",
				ManualDate:      manualDate,
				SoldOutStrategy: "disable",
			},
			week: weekSnapshot{
				Sites:          []siteOption{},
				Timeslots:      [][]string{},
				Cells:          map[string]weekCell{},
				SelectedSiteID: 0,
				WeekStart:      weekStart,
			},
			patterns: map[string]*selectionPattern{},
		},
	}
	app.state.settings.RuleHint = app.ruleHintLocked()
	app.state.status = "未登录"
	app.loadConfig()
	return app
}

func StartAppBootstrap() {
	appBootstrapOnce.Do(func() {
		go appService.restoreSavedSession()
	})
}

func RegisterAppRoutes(group *gin.RouterGroup) {
	group.GET("/ping", pingHandler)
	group.GET("/state", stateHandler)
	group.POST("/login", authHandler)
	group.POST("/week", weekHandler)
	group.POST("/site", siteHandler)
	group.POST("/selection/toggle", selectionToggleHandler)
	group.POST("/selection/remove", selectionRemoveHandler)
	group.POST("/selection/clear", selectionClearHandler)
	group.POST("/settings/mode", settingsModeHandler)
	group.POST("/settings/manual-date", settingsManualDateHandler)
	group.POST("/settings/sold-out-strategy", soldOutStrategyHandler)
	group.POST("/booking/start", bookingStartHandler)
	group.POST("/booking/stop", bookingStopHandler)
	group.POST("/token/refresh", tokenRefreshHandler)
	group.POST("/relogin", reloginHandler)
}

func pingHandler(c *gin.Context) {
	c.JSON(200, pingResponse{OK: true})
}

func stateHandler(c *gin.Context) {
	writeAppSuccess(c, appService.snapshot())
}

func weekHandler(c *gin.Context) {
	var req weekRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAppError(c, "bad request", nil)
		return
	}

	snapshot, err := appService.setWeek(req.WeekStart)
	if err != nil {
		writeAppError(c, err.Error(), appService.snapshotPtr())
		return
	}
	writeAppSuccess(c, snapshot)
}

func siteHandler(c *gin.Context) {
	var req siteSelectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAppError(c, "bad request", nil)
		return
	}
	snapshot, err := appService.setSite(req.SiteID)
	if err != nil {
		writeAppError(c, err.Error(), appService.snapshotPtr())
		return
	}
	writeAppSuccess(c, snapshot)
}

func selectionToggleHandler(c *gin.Context) {
	var req selectionToggleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAppError(c, "bad request", nil)
		return
	}
	snapshot, err := appService.toggleSelection(req)
	if err != nil {
		writeAppError(c, err.Error(), appService.snapshotPtr())
		return
	}
	writeAppSuccess(c, snapshot)
}

func selectionRemoveHandler(c *gin.Context) {
	var req selectionRemoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAppError(c, "bad request", nil)
		return
	}
	snapshot, err := appService.removeSelections(req.Keys)
	if err != nil {
		writeAppError(c, err.Error(), appService.snapshotPtr())
		return
	}
	writeAppSuccess(c, snapshot)
}

func selectionClearHandler(c *gin.Context) {
	snapshot, err := appService.clearSelections()
	if err != nil {
		writeAppError(c, err.Error(), appService.snapshotPtr())
		return
	}
	writeAppSuccess(c, snapshot)
}

func settingsModeHandler(c *gin.Context) {
	var req modeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAppError(c, "bad request", nil)
		return
	}
	snapshot, err := appService.setMode(req.Mode)
	if err != nil {
		writeAppError(c, err.Error(), appService.snapshotPtr())
		return
	}
	writeAppSuccess(c, snapshot)
}

func settingsManualDateHandler(c *gin.Context) {
	var req manualDateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAppError(c, "bad request", nil)
		return
	}
	snapshot, err := appService.setManualDate(req.ManualDate)
	if err != nil {
		writeAppError(c, err.Error(), appService.snapshotPtr())
		return
	}
	writeAppSuccess(c, snapshot)
}

func soldOutStrategyHandler(c *gin.Context) {
	var req soldOutStrategyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAppError(c, "bad request", nil)
		return
	}
	snapshot, err := appService.setSoldOutStrategy(req.SoldOutStrategy)
	if err != nil {
		writeAppError(c, err.Error(), appService.snapshotPtr())
		return
	}
	writeAppSuccess(c, snapshot)
}

func bookingStartHandler(c *gin.Context) {
	snapshot, err := appService.startBooking()
	if err != nil {
		writeAppError(c, err.Error(), appService.snapshotPtr())
		return
	}
	writeAppSuccess(c, snapshot)
}

func bookingStopHandler(c *gin.Context) {
	writeAppSuccess(c, appService.stopBooking())
}

func tokenRefreshHandler(c *gin.Context) {
	snapshot, _, err := appService.refreshToken()
	if err != nil {
		writeAppError(c, err.Error(), appService.snapshotPtr())
		return
	}
	writeAppSuccess(c, snapshot)
}

func reloginHandler(c *gin.Context) {
	writeAppSuccess(c, appService.resetLoginSession())
}

func writeAppSuccess(c *gin.Context, snapshot appSnapshot) {
	c.JSON(200, stateResponse{
		OK:       true,
		Message:  "",
		Snapshot: &snapshot,
	})
}

func writeAppError(c *gin.Context, message string, snapshot *appSnapshot) {
	c.JSON(200, stateResponse{
		OK:       false,
		Message:  message,
		Snapshot: snapshot,
	})
}

func (a *bookingApp) login(username, password, serviceURL string) (appSnapshot, string, error) {
	token, err := a.gateway.Auth(username, password, serviceURL)
	if err != nil {
		return appSnapshot{}, "", err
	}

	a.mu.Lock()
	a.state.username = username
	a.state.password = password
	a.state.serviceURL = serviceURL
	a.state.auth = authSnapshot{
		LoggedIn:            true,
		HasSavedCredentials: true,
		Username:            username,
		Token:               token,
		Bearer:              "Bearer " + token,
		UserAgent:           a.gateway.UserAgent(),
	}
	a.state.status = "已登录"
	a.appendLogLocked("登录成功，已获取预约 token")
	a.saveConfigLocked()
	snapshot := a.snapshotLocked()
	a.mu.Unlock()
	return snapshot, token, nil
}

func (a *bookingApp) refreshToken() (appSnapshot, string, error) {
	a.mu.Lock()
	username := a.state.username
	password := a.state.password
	serviceURL := a.state.serviceURL
	a.mu.Unlock()

	if username == "" || password == "" {
		return appSnapshot{}, "", fmt.Errorf("当前没有可复用的登录信息")
	}
	if serviceURL == "" {
		serviceURL = defaultKJYYServiceURL
	}
	return a.login(username, password, serviceURL)
}

func (a *bookingApp) resetLoginSession() appSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.state.auth.LoggedIn = false
	a.state.auth.Token = ""
	a.state.auth.Bearer = ""
	a.state.auth.HasSavedCredentials = false
	a.state.username = ""
	a.state.password = ""
	a.state.serviceURL = ""
	a.state.settings.Running = false
	a.state.patterns = map[string]*selectionPattern{}
	a.state.logs = nil
	a.state.status = "未登录"
	a.syncCellChecksLocked()
	a.saveConfigLocked()
	return a.snapshotLocked()
}

func (a *bookingApp) setWeek(weekStart string) (appSnapshot, error) {
	return a.refreshWeek(weekStart)
}

func (a *bookingApp) setSite(siteID int) (appSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if siteID == 0 {
		return appSnapshot{}, fmt.Errorf("请选择场地")
	}

	found := false
	for _, site := range a.state.week.Sites {
		if site.SiteID == siteID {
			found = true
			break
		}
	}
	if !found {
		return appSnapshot{}, fmt.Errorf("所选场地不存在")
	}

	a.state.week.SelectedSiteID = siteID
	a.saveConfigLocked()
	return a.snapshotLocked(), nil
}

func (a *bookingApp) toggleSelection(req selectionToggleRequest) (appSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.state.settings.Running {
		return appSnapshot{}, fmt.Errorf("请先停止预约程序，再进行更改")
	}

	key := cellKey(req.SiteID, req.Weekday, req.StartHM)
	cell, ok := a.state.week.Cells[key]
	if !ok || cell.StartHM == "" {
		return appSnapshot{}, fmt.Errorf("当前时段不可选")
	}

	patternKey := selectionKey(req.SiteID, req.Weekday, req.StartHM, req.EndHM)
	if !req.Checked {
		delete(a.state.patterns, patternKey)
		a.syncCellChecksLocked()
		a.saveConfigLocked()
		return a.snapshotLocked(), nil
	}

	a.state.patterns[patternKey] = &selectionPattern{
		Key:               patternKey,
		SiteID:            req.SiteID,
		SiteName:          cell.SiteName,
		WeekdayIndex:      req.Weekday,
		WeekdayLabel:      weekdayLabel(req.Weekday),
		MeetDate:          cell.MeetDate,
		StartHM:           req.StartHM,
		EndHM:             req.EndHM,
		OrderMoney:        cell.OrderMoney,
		AdditionalCharges: cell.AdditionalCharges,
		Status:            "轮询中",
		StatusKey:         "polling",
		Message:           "已加入预约列表",
	}
	a.refreshPatternStatusLocked(a.state.patterns[patternKey])
	a.syncCellChecksLocked()
	a.saveConfigLocked()
	return a.snapshotLocked(), nil
}

func (a *bookingApp) removeSelections(keys []string) (appSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.settings.Running {
		return appSnapshot{}, fmt.Errorf("请先停止预约程序，再进行更改")
	}
	for _, key := range keys {
		delete(a.state.patterns, key)
	}
	a.syncCellChecksLocked()
	a.saveConfigLocked()
	return a.snapshotLocked(), nil
}

func (a *bookingApp) clearSelections() (appSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.settings.Running {
		return appSnapshot{}, fmt.Errorf("请先停止预约程序，再进行更改")
	}
	a.state.patterns = map[string]*selectionPattern{}
	a.syncCellChecksLocked()
	a.saveConfigLocked()
	return a.snapshotLocked(), nil
}

func (a *bookingApp) setMode(mode string) (appSnapshot, error) {
	if mode != "rule" && mode != "manual" {
		return appSnapshot{}, fmt.Errorf("不支持的预约模式")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.settings.Running {
		return appSnapshot{}, fmt.Errorf("请先停止预约程序，再进行更改")
	}
	a.state.settings.Mode = mode
	a.state.settings.RuleHint = a.ruleHintLocked()
	a.saveConfigLocked()
	return a.snapshotLocked(), nil
}

func (a *bookingApp) setManualDate(manualDate string) (appSnapshot, error) {
	if !isDateString(manualDate) {
		return appSnapshot{}, fmt.Errorf("日期格式错误")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.settings.Running {
		return appSnapshot{}, fmt.Errorf("请先停止预约程序，再进行更改")
	}
	a.state.settings.ManualDate = manualDate
	a.saveConfigLocked()
	return a.snapshotLocked(), nil
}

func (a *bookingApp) setSoldOutStrategy(strategy string) (appSnapshot, error) {
	if strategy != "disable" && strategy != "cooldown" {
		return appSnapshot{}, fmt.Errorf("不支持的策略")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.settings.Running {
		return appSnapshot{}, fmt.Errorf("请先停止预约程序，再进行更改")
	}
	a.state.settings.SoldOutStrategy = strategy
	a.saveConfigLocked()
	return a.snapshotLocked(), nil
}

func (a *bookingApp) startBooking() (appSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.state.auth.LoggedIn || a.state.auth.Token == "" {
		return appSnapshot{}, fmt.Errorf("请先登录")
	}
	if len(a.state.patterns) == 0 {
		return appSnapshot{}, fmt.Errorf("请先选择至少一个预约时段")
	}
	if a.state.settings.Mode == "manual" && !isDateString(a.state.settings.ManualDate) {
		return appSnapshot{}, fmt.Errorf("请先设置有效的手动日期")
	}
	if a.stopCh != nil {
		return a.snapshotLocked(), nil
	}

	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	a.stopCh = stopCh
	a.doneCh = doneCh
	a.state.settings.Running = true
	a.state.status = "预约中"
	a.appendLogLocked("预约程序已启动")

	go a.bookingLoop(stopCh, doneCh)
	return a.snapshotLocked(), nil
}

func (a *bookingApp) stopBooking() appSnapshot {
	a.mu.Lock()
	stopCh := a.stopCh
	doneCh := a.doneCh
	a.stopCh = nil
	a.doneCh = nil
	a.state.settings.Running = false
	if a.state.auth.LoggedIn {
		a.state.status = "已登录"
	} else {
		a.state.status = "未登录"
	}
	a.appendLogLocked("预约程序已停止")
	snapshot := a.snapshotLocked()
	a.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(2 * time.Second):
		}
	}
	return snapshot
}

func (a *bookingApp) refreshWeek(weekStart string) (appSnapshot, error) {
	a.mu.Lock()
	if !a.state.auth.LoggedIn || a.state.auth.Token == "" {
		if weekStart != "" && isDateString(weekStart) {
			a.state.week.WeekStart = weekStart
		}
		snapshot := a.snapshotLocked()
		a.mu.Unlock()
		return snapshot, nil
	}
	token := a.state.auth.Token
	currentWeekStart := a.state.week.WeekStart
	a.mu.Unlock()

	if weekStart == "" {
		weekStart = currentWeekStart
	}
	if weekStart == "" {
		weekStart = mondayOf(a.now()).Format("2006-01-02")
	}
	if !isDateString(weekStart) {
		return appSnapshot{}, fmt.Errorf("周起始日期格式错误")
	}

	startDate, _ := time.Parse("2006-01-02", weekStart)
	results := make([]*meetListResult, 0, 7)
	for i := 0; i < 7; i++ {
		meetDate := startDate.AddDate(0, 0, i).Format("2006-01-02")
		result, err := a.getListWithRetry(token, meetDate)
		if err != nil {
			return appSnapshot{}, err
		}
		results = append(results, result)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.rebuildWeekLocked(weekStart, results)
	a.saveConfigLocked()
	return a.snapshotLocked(), nil
}

func (a *bookingApp) bookingLoop(stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)

	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()

	a.runBookingCycle()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			a.runBookingCycle()
		}
	}
}

func (a *bookingApp) runBookingCycle() {
	a.mu.Lock()
	if !a.state.settings.Running {
		a.mu.Unlock()
		return
	}
	token := a.state.auth.Token
	mode := a.state.settings.Mode
	manualDate := a.state.settings.ManualDate
	strategy := a.state.settings.SoldOutStrategy
	weekStart := a.state.week.WeekStart
	patterns := make([]selectionPattern, 0, len(a.state.patterns))
	for _, pattern := range a.state.patterns {
		patterns = append(patterns, *pattern)
	}
	a.mu.Unlock()

	dateMap := map[string][]selectionPattern{}
	for _, pattern := range patterns {
		if pattern.StatusKey == "purchased" {
			continue
		}
		targetDate := resolvePatternTargetDate(pattern, mode, manualDate, weekStart, a.now())
		if !isDateString(targetDate) {
			continue
		}
		dateMap[targetDate] = append(dateMap[targetDate], pattern)
	}

	for meetDate, targetPatterns := range dateMap {
		listResult, err := a.getListWithRetry(token, meetDate)
		if err != nil {
			a.mu.Lock()
			a.appendLogLocked("刷新 " + meetDate + " 场地数据失败: " + err.Error())
			a.mu.Unlock()
			continue
		}

		lookup := buildPeriodLookup(listResult)
		for _, pattern := range targetPatterns {
			a.mu.Lock()
			livePattern := a.state.patterns[pattern.Key]
			if livePattern == nil || livePattern.StatusKey == "purchased" {
				a.mu.Unlock()
				continue
			}
			a.preparePatternForTargetLocked(livePattern, meetDate)
			openAt := calculateOpenAt(meetDate)
			now := a.now()
			if now.Before(openAt) {
				noticeKey := "pending_release:" + openAt.Format(time.RFC3339)
				if livePattern.LastNoticeKey != noticeKey {
					livePattern.LastNoticeKey = noticeKey
					a.appendLogLocked(fmt.Sprintf("%s %s-%s 等待开放时间 %s",
						livePattern.SiteName, livePattern.StartHM, livePattern.EndHM, openAt.Format("2006-01-02 15:04:05")))
				}
				livePattern.Status = "预开售"
				livePattern.StatusKey = "pending_release"
				livePattern.Message = "尚未到开放时间"
				a.mu.Unlock()
				continue
			}
			if strategy == "cooldown" && a.now().Before(livePattern.CooldownUntil) {
				livePattern.Status = "暂不可约"
				livePattern.StatusKey = "sold_out"
				livePattern.Message = "冷却中，稍后继续尝试"
				a.mu.Unlock()
				continue
			}
			if livePattern.DisabledForCycle == meetDate {
				livePattern.Status = "已售罄"
				livePattern.StatusKey = "sold_out"
				livePattern.Message = "本周期内已停止重试"
				a.mu.Unlock()
				continue
			}
			if !a.canAttemptLocked(livePattern, now, openAt) {
				a.mu.Unlock()
				continue
			}
			notice := a.markAttemptLocked(livePattern, now, openAt)
			if notice != "" {
				a.appendLogLocked(notice)
			}
			a.mu.Unlock()

			period, ok := lookup[selectionLookupKey(pattern.SiteID, pattern.StartHM)]
			if !ok || period.Status != "10" {
				a.mu.Lock()
				livePattern = a.state.patterns[pattern.Key]
				if livePattern != nil && livePattern.StatusKey != "purchased" {
					livePattern.FailureCount++
					livePattern.Status = "已售罄"
					livePattern.StatusKey = "sold_out"
					livePattern.Message = "当前轮询未发现空闲时段"
					if livePattern.FailureCount >= failuresToMarkSoldOut && strategy == "cooldown" {
						livePattern.CooldownUntil = a.now().Add(a.cooldownDelay)
						livePattern.Message = "进入冷却后继续尝试"
					}
					if livePattern.FailureCount >= failuresToMarkSoldOut && strategy == "disable" {
						livePattern.DisabledForCycle = meetDate
						livePattern.Message = "本轮已关闭，等待下个开放周期"
					}
				}
				a.mu.Unlock()
				continue
			}

			a.mu.Lock()
			livePattern = a.state.patterns[pattern.Key]
			if livePattern != nil {
				livePattern.Status = "准备预约"
				livePattern.StatusKey = "high_priority"
				livePattern.Message = "命中空闲时段，准备发起预约"
			}
			a.mu.Unlock()

			payload := buildInitiatePayload(meetDate, period)
			reqResult, err := a.initiateWithRetry(token, payload)
			if err != nil {
				a.mu.Lock()
				livePattern = a.state.patterns[pattern.Key]
				if livePattern != nil {
					livePattern.Status = "预约失败"
					livePattern.StatusKey = "sold_out"
					livePattern.Message = err.Error()
				}
				a.appendLogLocked(pattern.SiteName + " " + pattern.StartHM + "-" + pattern.EndHM + " 预约失败: " + err.Error())
				a.mu.Unlock()
				continue
			}

			recordID := extractRecordID(reqResult.ResponseJSON)
			if recordID == 0 {
				a.mu.Lock()
				livePattern = a.state.patterns[pattern.Key]
				if livePattern != nil {
					livePattern.Status = "预约失败"
					livePattern.StatusKey = "sold_out"
					livePattern.Message = "响应中未找到 recordId"
				}
				a.appendLogLocked(pattern.SiteName + " " + pattern.StartHM + "-" + pattern.EndHM + " 预约失败: 未返回 recordId")
				if reqResult.ResponseText != "" {
					a.appendLogLocked("下单响应: " + summarizeResponseText(reqResult.ResponseText))
				}
				a.mu.Unlock()
				continue
			}

			payResult, err := a.payWithRetry(token, recordID)
			if err != nil {
				a.mu.Lock()
				livePattern = a.state.patterns[pattern.Key]
				if livePattern != nil {
					livePattern.Status = "支付失败"
					livePattern.StatusKey = "sold_out"
					livePattern.Message = err.Error()
				}
				a.appendLogLocked(pattern.SiteName + " " + pattern.StartHM + "-" + pattern.EndHM + " 支付失败: " + err.Error())
				a.mu.Unlock()
				continue
			}
			if err := validateProxyBusinessOK(payResult); err != nil {
				a.mu.Lock()
				livePattern = a.state.patterns[pattern.Key]
				if livePattern != nil {
					livePattern.Status = "支付失败"
					livePattern.StatusKey = "sold_out"
					livePattern.Message = err.Error()
				}
				a.appendLogLocked(pattern.SiteName + " " + pattern.StartHM + "-" + pattern.EndHM + " 支付失败: " + err.Error())
				if payResult.ResponseText != "" {
					a.appendLogLocked("支付响应: " + summarizeResponseText(payResult.ResponseText))
				}
				a.mu.Unlock()
				continue
			}

			a.mu.Lock()
			livePattern = a.state.patterns[pattern.Key]
			if livePattern != nil {
				livePattern.Status = "已预约"
				livePattern.StatusKey = "purchased"
				livePattern.Message = fmt.Sprintf("预约成功，recordId=%d", recordID)
				livePattern.CooldownUntil = time.Time{}
				livePattern.DisabledForCycle = ""
			}
			a.appendLogLocked(pattern.SiteName + " " + pattern.StartHM + "-" + pattern.EndHM + " 预约并支付成功")
			if payResult.ResponseText != "" {
				a.appendLogLocked("支付响应: " + payResult.ResponseText)
			}
			a.saveConfigLocked()
			a.mu.Unlock()
		}
	}
}

func (a *bookingApp) ruleHintLocked() string {
	if a.state.settings.Mode == "manual" {
		return "手动日期模式：已选时段会在指定日期尝试预约。"
	}
	return "按周模式：只保存场地、周几和时间段；当前页面日期仅用于查看，本规则会在后续每一周自动复用。"
}

func (a *bookingApp) refreshPatternStatusLocked(pattern *selectionPattern) {
	targetDate := resolvePatternTargetDate(*pattern, a.state.settings.Mode, a.state.settings.ManualDate, a.state.week.WeekStart, a.now())
	if !isDateString(targetDate) {
		pattern.Status = "轮询中"
		pattern.StatusKey = "polling"
		pattern.Message = "等待规则映射"
		return
	}

	openAt := calculateOpenAt(targetDate)
	now := a.now()
	if !openAt.IsZero() && now.Before(openAt) {
		pattern.Status = "预开售"
		pattern.StatusKey = "pending_release"
		pattern.Message = "尚未到开放时间"
		return
	}

	if pattern.StatusKey == "purchased" {
		return
	}

	pattern.Status = "轮询中"
	pattern.StatusKey = "polling"
	pattern.Message = "已到可尝试预约阶段"
}

func (a *bookingApp) rebuildWeekLocked(weekStart string, results []*meetListResult) {
	sitesMap := map[int]string{}
	timeslotMap := map[string][2]string{}
	cells := map[string]weekCell{}

	for _, result := range results {
		for _, site := range result.Sites {
			sitesMap[site.SiteID] = site.SiteName
			for _, period := range site.Periods {
				timeslotMap[period.StartHM+"|"+period.EndHM] = [2]string{period.StartHM, period.EndHM}
			}
		}
	}

	sites := make([]siteOption, 0, len(sitesMap))
	for siteID, siteName := range sitesMap {
		sites = append(sites, siteOption{SiteID: siteID, SiteName: siteName})
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].SiteID < sites[j].SiteID })

	timeslots := make([][]string, 0, len(timeslotMap))
	for _, slot := range timeslotMap {
		timeslots = append(timeslots, []string{slot[0], slot[1]})
	}
	sort.Slice(timeslots, func(i, j int) bool { return timeslots[i][0] < timeslots[j][0] })

	for siteID, siteName := range sitesMap {
		for weekday := 0; weekday < 7; weekday++ {
			meetDate := ""
			if parsedWeekStart, err := time.Parse("2006-01-02", weekStart); err == nil {
				meetDate = parsedWeekStart.AddDate(0, 0, weekday).Format("2006-01-02")
			}
			for _, slot := range timeslots {
				key := cellKey(siteID, weekday, slot[0])
				cells[key] = weekCell{
					Checked:           false,
					Disabled:          false,
					StatusKey:         "none",
					RawStatus:         "none",
					SiteID:            siteID,
					SiteName:          siteName,
					Weekday:           weekday,
					MeetDate:          meetDate,
					StartHM:           slot[0],
					EndHM:             slot[1],
					OrderMoney:        nil,
					AdditionalCharges: nil,
				}
			}
		}
	}

	for _, result := range results {
		for _, site := range result.Sites {
			for _, period := range site.Periods {
				weekday := -1
				if parsedWeekStart, err := time.Parse("2006-01-02", weekStart); err == nil {
					if parsedMeetDate, err := time.Parse("2006-01-02", period.MeetDate); err == nil {
						weekday = int(parsedMeetDate.Sub(parsedWeekStart).Hours() / 24)
					}
				}
				if weekday < 0 || weekday > 6 {
					continue
				}
				key := cellKey(site.SiteID, weekday, period.StartHM)
				cells[key] = weekCell{
					Checked:           false,
					Disabled:          false,
					StatusKey:         period.Status,
					RawStatus:         period.Status,
					SiteID:            site.SiteID,
					SiteName:          site.SiteName,
					Weekday:           weekday,
					MeetDate:          period.MeetDate,
					StartHM:           period.StartHM,
					EndHM:             period.EndHM,
					OrderMoney:        period.HourCharge,
					AdditionalCharges: period.AdditionalCharges,
				}
			}
		}
	}

	selectedSiteID := a.state.week.SelectedSiteID
	if selectedSiteID == 0 && len(sites) > 0 {
		selectedSiteID = sites[0].SiteID
	}
	if selectedSiteID != 0 {
		found := false
		for _, site := range sites {
			if site.SiteID == selectedSiteID {
				found = true
				break
			}
		}
		if !found && len(sites) > 0 {
			selectedSiteID = sites[0].SiteID
		}
	}

	a.state.week = weekSnapshot{
		Sites:          sites,
		Timeslots:      timeslots,
		Cells:          cells,
		SelectedSiteID: selectedSiteID,
		WeekStart:      weekStart,
	}
	a.syncCellChecksLocked()
}

func (a *bookingApp) syncCellChecksLocked() {
	for key, cell := range a.state.week.Cells {
		cell.Checked = false
		a.state.week.Cells[key] = cell
	}
	for _, pattern := range a.state.patterns {
		key := cellKey(pattern.SiteID, pattern.WeekdayIndex, pattern.StartHM)
		if cell, ok := a.state.week.Cells[key]; ok {
			cell.Checked = true
			a.state.week.Cells[key] = cell
		}
	}
}

func (a *bookingApp) appendLogLocked(message string) {
	a.state.logs = append(a.state.logs, logEntry{
		Time:    a.now().Format("15:04:05"),
		Message: message,
	})
	if len(a.state.logs) > maxLogEntries {
		a.state.logs = a.state.logs[len(a.state.logs)-maxLogEntries:]
	}
}

func (a *bookingApp) preparePatternForTargetLocked(pattern *selectionPattern, targetDate string) {
	if pattern.CurrentTargetDate != targetDate {
		pattern.CurrentTargetDate = targetDate
		pattern.FailureCount = 0
		pattern.CooldownUntil = time.Time{}
		pattern.DisabledForCycle = ""
		pattern.LastAttemptAt = time.Time{}
		pattern.WindowStartedAt = time.Time{}
		pattern.AttemptsInWindow = 0
		pattern.LastNoticeKey = ""
		if pattern.StatusKey != "purchased" {
			pattern.Status = "预开售"
			pattern.StatusKey = "pending_release"
			pattern.Message = "等待新的开放周期"
		}
	}
}

func (a *bookingApp) canAttemptLocked(pattern *selectionPattern, now, openAt time.Time) bool {
	if isHighPriorityWindow(now, openAt) {
		return now.Sub(pattern.LastAttemptAt) >= highFreqInterval && pattern.AttemptsInWindow < highFreqMaxAttempts
	}

	if pattern.WindowStartedAt.IsZero() || now.Sub(pattern.WindowStartedAt) >= rateLimitWindow {
		return true
	}
	if pattern.AttemptsInWindow >= maxAttemptsPer30Minutes {
		pattern.Status = "轮询中"
		pattern.StatusKey = "polling"
		pattern.Message = "已触发安全轮询限流，等待下个窗口"
		return false
	}
	if now.Sub(pattern.LastAttemptAt) < regularRetryInterval {
		pattern.Status = "轮询中"
		pattern.StatusKey = "polling"
		pattern.Message = "等待下一次安全轮询"
		return false
	}
	return true
}

func (a *bookingApp) markAttemptLocked(pattern *selectionPattern, now, openAt time.Time) string {
	notice := ""
	if isHighPriorityWindow(now, openAt) {
		windowKey := "high_priority:" + openAt.Format(time.RFC3339)
		if pattern.WindowStartedAt.IsZero() || !isHighPriorityWindow(pattern.WindowStartedAt, openAt) {
			pattern.WindowStartedAt = now
			pattern.AttemptsInWindow = 0
		}
		if pattern.LastNoticeKey != windowKey {
			pattern.LastNoticeKey = windowKey
			notice = fmt.Sprintf("%s %s-%s 进入 08:00 高频窗口，开放时间 %s",
				pattern.SiteName, pattern.StartHM, pattern.EndHM, openAt.Format("2006-01-02 15:04:05"))
		}
		pattern.Status = "高频抢票"
		pattern.StatusKey = "high_priority"
		pattern.Message = "处于新开放高频窗口"
	} else {
		if pattern.WindowStartedAt.IsZero() || now.Sub(pattern.WindowStartedAt) >= rateLimitWindow || isHighPriorityWindow(pattern.WindowStartedAt, openAt) {
			pattern.WindowStartedAt = now
			pattern.AttemptsInWindow = 0
		}
		pattern.Status = "轮询中"
		pattern.StatusKey = "polling"
		pattern.Message = "按安全频率轮询"
	}
	pattern.LastAttemptAt = now
	pattern.AttemptsInWindow++
	return notice
}

func (a *bookingApp) markAttemptWithNoticeLocked(pattern *selectionPattern, now, openAt time.Time) string {
	if isHighPriorityWindow(now, openAt) {
		return a.markAttemptLocked(pattern, now, openAt)
	}

	if pattern.WindowStartedAt.IsZero() || now.Sub(pattern.WindowStartedAt) >= rateLimitWindow || isHighPriorityWindow(pattern.WindowStartedAt, openAt) {
		pattern.WindowStartedAt = now
		pattern.AttemptsInWindow = 0
	}
	pattern.Status = "轮询中"
	pattern.StatusKey = "polling"
	pattern.Message = "按安全频率轮询"
	pattern.LastAttemptAt = now
	pattern.AttemptsInWindow++

	noticeKey := "polling:" + pattern.CurrentTargetDate
	if pattern.LastNoticeKey == noticeKey {
		return ""
	}
	pattern.LastNoticeKey = noticeKey
	return fmt.Sprintf("%s %s-%s 开始轮询目标日期 %s", pattern.SiteName, pattern.StartHM, pattern.EndHM, pattern.CurrentTargetDate)
}

func (a *bookingApp) snapshot() appSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshotLocked()
}

func (a *bookingApp) getListWithRetry(token, meetDate string) (*meetListResult, error) {
	result, err := a.gateway.GetList(token, meetDate)
	if !errors.Is(err, errAuthExpired) {
		return result, err
	}

	_, refreshedToken, refreshErr := a.refreshToken()
	if refreshErr != nil {
		return nil, refreshErr
	}
	return a.gateway.GetList(refreshedToken, meetDate)
}

func (a *bookingApp) initiateWithRetry(token string, payload map[string]interface{}) (*proxyResponse, error) {
	result, err := a.gateway.Initiate(token, payload)
	if !errors.Is(err, errAuthExpired) {
		return result, err
	}

	_, refreshedToken, refreshErr := a.refreshToken()
	if refreshErr != nil {
		return nil, refreshErr
	}
	return a.gateway.Initiate(refreshedToken, payload)
}

func (a *bookingApp) payWithRetry(token string, recordID int64) (*proxyResponse, error) {
	result, err := a.gateway.Pay(token, recordID)
	if !errors.Is(err, errAuthExpired) {
		return result, err
	}

	_, refreshedToken, refreshErr := a.refreshToken()
	if refreshErr != nil {
		return nil, refreshErr
	}
	return a.gateway.Pay(refreshedToken, recordID)
}

func (a *bookingApp) snapshotPtr() *appSnapshot {
	snapshot := a.snapshot()
	return &snapshot
}

func (a *bookingApp) snapshotLocked() appSnapshot {
	patterns := make([]patternSnapshot, 0, len(a.state.patterns))
	keys := make([]string, 0, len(a.state.patterns))
	for key := range a.state.patterns {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		pattern := a.state.patterns[key]
		displayStatus, displayStatusKey := a.patternDisplayStateLocked(pattern)
		patterns = append(patterns, patternSnapshot{
			Key:       pattern.Key,
			SiteName:  pattern.SiteName,
			Weekday:   pattern.WeekdayLabel,
			TimeRange: pattern.StartHM + "-" + pattern.EndHM,
			Status:    displayStatus,
			StatusKey: displayStatusKey,
			Message:   pattern.Message,
		})
	}

	logs := append([]logEntry(nil), a.state.logs...)
	weekCells := make(map[string]weekCell, len(a.state.week.Cells))
	for key, cell := range a.state.week.Cells {
		weekCells[key] = cell
	}

	return appSnapshot{
		Auth:     a.state.auth,
		Settings: a.state.settings,
		Week: weekSnapshot{
			Sites:          append([]siteOption(nil), a.state.week.Sites...),
			Timeslots:      append([][]string(nil), a.state.week.Timeslots...),
			Cells:          weekCells,
			SelectedSiteID: a.state.week.SelectedSiteID,
			WeekStart:      a.state.week.WeekStart,
		},
		Patterns:   patterns,
		Logs:       logs,
		StatusText: a.state.status,
	}
}

func (a *bookingApp) patternDisplayStateLocked(pattern *selectionPattern) (string, string) {
	switch pattern.StatusKey {
	case "purchased":
		return "已预约", "purchased"
	case "pending_release":
		return "预开售", "pending_release"
	}

	if a.state.settings.Running {
		return "轮询中", "polling"
	}
	return "未预约", "unbooked"
}

func weekdayLabel(index int) string {
	labels := []string{"周一", "周二", "周三", "周四", "周五", "周六", "周日"}
	if index < 0 || index >= len(labels) {
		return "未知"
	}
	return labels[index]
}

func mondayOf(now time.Time) time.Time {
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	return time.Date(now.Year(), now.Month(), now.Day()-(weekday-1), 0, 0, 0, 0, now.Location())
}

func isDateString(value string) bool {
	if value == "" {
		return false
	}
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

func resolveRuleDate(weekStart string, weekdayIndex int, now time.Time) string {
	if !isDateString(weekStart) {
		weekStart = mondayOf(now).Format("2006-01-02")
	}
	start, err := time.Parse("2006-01-02", weekStart)
	if err != nil {
		return ""
	}
	target := start.AddDate(0, 0, weekdayIndex)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if target.Before(today) {
		target = target.AddDate(0, 0, 7)
	}
	return target.Format("2006-01-02")
}

func resolvePatternTargetDate(pattern selectionPattern, mode, manualDate, weekStart string, now time.Time) string {
	if mode == "manual" {
		if isDateString(manualDate) {
			return manualDate
		}
		return ""
	}

	if isDateString(pattern.MeetDate) {
		target, err := time.Parse("2006-01-02", pattern.MeetDate)
		if err == nil {
			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			for target.Before(today) {
				target = target.AddDate(0, 0, 7)
			}
			return target.Format("2006-01-02")
		}
	}

	return resolveRuleDate(weekStart, pattern.WeekdayIndex, now)
}

func calculateOpenAt(targetDate string) time.Time {
	target, err := time.Parse("2006-01-02", targetDate)
	if err != nil {
		return time.Time{}
	}
	openDay := target.AddDate(0, 0, -openAdvanceDays)
	return time.Date(openDay.Year(), openDay.Month(), openDay.Day(), openHour, openMinute, 0, 0, time.Local)
}

func isHighPriorityWindow(now, openAt time.Time) bool {
	if openAt.IsZero() {
		return false
	}
	return !now.Before(openAt) && now.Before(openAt.Add(highFreqWindow))
}

func cellKey(siteID, weekday int, startHM string) string {
	return fmt.Sprintf("%d|%d|%s", siteID, weekday, startHM)
}

func selectionKey(siteID, weekday int, startHM, endHM string) string {
	return fmt.Sprintf("%d|%d|%s|%s", siteID, weekday, startHM, endHM)
}

func selectionLookupKey(siteID int, startHM string) string {
	return fmt.Sprintf("%d|%s", siteID, startHM)
}

func buildInitiatePayload(meetDate string, period meetPeriod) map[string]interface{} {
	return map[string]interface{}{
		"orderMoney":        formatMoney(period.HourCharge),
		"additionalCharges": formatNumber(period.AdditionalCharges),
		"recordPurpose":     "1",
		"typeId":            defaultTypeID,
		"orderList": []map[string]interface{}{
			{
				"startTime": meetDate + " " + period.StartHM,
				"endTime":   meetDate + " " + period.EndHM,
			},
		},
		"params":    map[string]interface{}{},
		"siteId":    period.SiteID,
		"ischanrge": true,
		"isReview":  false,
		"flowKey":   nil,
		"siteName":  period.SiteName,
		"mode":      2,
		"meetDate":  meetDate,
	}
}

func formatMoney(value interface{}) string {
	switch v := value.(type) {
	case float64:
		return fmt.Sprintf("%.2f", v)
	case float32:
		return fmt.Sprintf("%.2f", v)
	case int:
		return fmt.Sprintf("%.2f", float64(v))
	case int64:
		return fmt.Sprintf("%.2f", float64(v))
	case string:
		if v == "" {
			return "0.00"
		}
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			return fmt.Sprintf("%.2f", parsed)
		}
		return v
	default:
		return "0.00"
	}
}

func formatNumber(value interface{}) interface{} {
	switch v := value.(type) {
	case nil:
		return 0
	case string:
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			return parsed
		}
		return v
	default:
		return v
	}
}

func extractRecordID(responseJSON interface{}) int64 {
	root, ok := responseJSON.(map[string]interface{})
	if !ok {
		return 0
	}

	if recordID := parseRecordIDValue(root["recordId"]); recordID != 0 {
		return recordID
	}
	if recordID := parseRecordIDValue(root["id"]); recordID != 0 {
		return recordID
	}

	data := root["data"]
	if data == nil {
		return 0
	}
	if recordID := parseRecordIDValue(data); recordID != 0 {
		return recordID
	}
	if dataMap, ok := data.(map[string]interface{}); ok {
		if recordID := parseRecordIDValue(dataMap["recordId"]); recordID != 0 {
			return recordID
		}
		if recordID := parseRecordIDValue(dataMap["recordID"]); recordID != 0 {
			return recordID
		}
		if recordID := parseRecordIDValue(dataMap["id"]); recordID != 0 {
			return recordID
		}
	}
	return 0
}

func parseRecordIDValue(value interface{}) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		recordID, _ := strconv.ParseInt(v, 10, 64)
		return recordID
	default:
		return 0
	}
}

func summarizeResponseText(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if len(text) > 200 {
		return text[:200] + "..."
	}
	return text
}

func validateProxyBusinessOK(result *proxyResponse) error {
	if result == nil {
		return fmt.Errorf("空响应")
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", result.StatusCode)
	}

	root, ok := result.ResponseJSON.(map[string]interface{})
	if !ok {
		return nil
	}

	codeValue, hasCode := root["code"]
	if !hasCode {
		return nil
	}
	codeText := strings.TrimSpace(fmt.Sprintf("%v", codeValue))
	if codeText == "200" || codeText == "0" || strings.EqualFold(codeText, "ok") {
		return nil
	}

	msg := strings.TrimSpace(stringValue(root["msg"]))
	if msg == "" {
		msg = strings.TrimSpace(stringValue(root["message"]))
	}
	if msg == "" {
		msg = "业务返回失败"
	}
	return fmt.Errorf("%s(code=%s)", msg, codeText)
}

func highPriorityLabel(openAt time.Time) string {
	if openAt.IsZero() {
		return "高频"
	}
	return openAt.Format("15:04")
}

func buildPeriodLookup(result *meetListResult) map[string]meetPeriod {
	lookup := map[string]meetPeriod{}
	for _, site := range result.Sites {
		for _, period := range site.Periods {
			lookup[selectionLookupKey(site.SiteID, period.StartHM)] = period
		}
	}
	return lookup
}

type meetListResult struct {
	StatusCode   int
	RequestURL   string
	ResponseJSON map[string]interface{}
	ResponseText string
	Sites        []meetSite
}

type meetSite struct {
	SiteID   int
	SiteName string
	Periods  []meetPeriod
}

type meetPeriod struct {
	SiteID            int
	SiteName          string
	MeetDate          string
	Status            string
	StartHM           string
	EndHM             string
	HourCharge        interface{}
	AdditionalCharges interface{}
}

type realGateway struct{}

func (realGateway) UserAgent() string {
	return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36 MicroMessenger/7.0.20.1781(0x6700143B) NetType/WIFI MiniProgramEnv/Windows WindowsWechat/WMPF WindowsWechat(0x63090a13) UnifiedPCWindowsWechat(0xf2541a1f) XWEB/19921"
}

func (g realGateway) Auth(username, password, serviceURL string) (string, error) {
	client, err := newKJYYClient(serviceURL)
	if err != nil {
		return "", err
	}
	return client.login(username, password)
}

func (g realGateway) GetList(token, meetDate string) (*meetListResult, error) {
	params := map[string]string{
		"siteAssort":       defaultSiteAssort,
		"siteType":         defaultSiteType,
		"typeId":           strconv.Itoa(defaultTypeID),
		"params[meetDate]": meetDate,
	}
	values := urlValuesFromMap(params)
	resp, body, err := doKJYYRequest("GET", kjyyListURL, token, values, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if isAuthFailure(resp.StatusCode, body) {
		return nil, errAuthExpired
	}

	data, err := parseJSONBody(body)
	if err != nil {
		return &meetListResult{
			StatusCode:   resp.StatusCode,
			RequestURL:   resp.Request.URL.String(),
			ResponseText: string(body),
			ResponseJSON: map[string]interface{}{},
			Sites:        []meetSite{},
		}, nil
	}

	sites := make([]meetSite, 0)
	rows, _ := data["data"].([]interface{})
	for _, row := range rows {
		rowMap, ok := row.(map[string]interface{})
		if !ok {
			continue
		}
		siteMap, _ := rowMap["site"].(map[string]interface{})
		site := meetSite{
			SiteID:   intFromInterface(siteMap["siteId"]),
			SiteName: stringValue(siteMap["siteName"]),
			Periods:  make([]meetPeriod, 0),
		}
		items, _ := rowMap["item"].([]interface{})
		for _, itemValue := range items {
			itemMap, ok := itemValue.(map[string]interface{})
			if !ok {
				continue
			}
			site.Periods = append(site.Periods, meetPeriod{
				SiteID:            site.SiteID,
				SiteName:          site.SiteName,
				MeetDate:          meetDate,
				Status:            stringValue(itemMap["status"]),
				StartHM:           normalizeHM(stringValue(itemMap["startTime"])),
				EndHM:             normalizeHM(stringValue(itemMap["endTime"])),
				HourCharge:        firstNonNil(itemMap["hourCharge"], itemMap["orderMoney"]),
				AdditionalCharges: firstNonNil(itemMap["itemTeaching"], itemMap["additionalCharges"], float64(0)),
			})
		}
		sites = append(sites, site)
	}

	return &meetListResult{
		StatusCode:   resp.StatusCode,
		RequestURL:   resp.Request.URL.String(),
		ResponseJSON: data,
		ResponseText: string(body),
		Sites:        sites,
	}, nil
}

func (g realGateway) Initiate(token string, payload map[string]interface{}) (*proxyResponse, error) {
	resp, body, err := doKJYYRequest("POST", kjyyInitiateURL, token, nil, payload)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if isAuthFailure(resp.StatusCode, body) {
		return nil, errAuthExpired
	}
	return buildProxyResponse(resp, body), nil
}

func (g realGateway) Pay(token string, recordID int64) (*proxyResponse, error) {
	values := urlValuesFromMap(map[string]string{"recordId": strconv.FormatInt(recordID, 10)})
	resp, body, err := doKJYYRequest("GET", kjyyPayURL, token, values, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if isAuthFailure(resp.StatusCode, body) {
		return nil, errAuthExpired
	}
	return buildProxyResponse(resp, body), nil
}

func urlValuesFromMap(values map[string]string) url.Values {
	result := url.Values{}
	for key, value := range values {
		result.Set(key, value)
	}
	return result
}

func normalizeHM(value string) string {
	if strings.Contains(value, " ") {
		parts := strings.Split(value, " ")
		return parts[len(parts)-1]
	}
	return value
}

func intFromInterface(value interface{}) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		i, _ := strconv.Atoi(v)
		return i
	default:
		return 0
	}
}

func isAuthFailure(statusCode int, body []byte) bool {
	if statusCode == 401 || statusCode == 403 {
		return true
	}
	text := strings.ToLower(string(body))
	return strings.Contains(text, "token") && (strings.Contains(text, "expired") || strings.Contains(text, "invalid")) ||
		strings.Contains(text, "登录失效") ||
		strings.Contains(text, "认证失败")
}

type persistedConfig struct {
	Username        string             `json:"username"`
	Password        string             `json:"password"`
	ServiceURL      string             `json:"serviceUrl"`
	Token           string             `json:"token"`
	WeekStart       string             `json:"weekStart"`
	SelectedSiteID  int                `json:"selectedSiteId"`
	Mode            string             `json:"mode"`
	ManualDate      string             `json:"manualDate"`
	SoldOutStrategy string             `json:"soldOutStrategy"`
	Patterns        []persistedPattern `json:"patterns"`
}

type persistedPattern struct {
	Key               string      `json:"key"`
	SiteID            int         `json:"siteId"`
	SiteName          string      `json:"siteName"`
	WeekdayIndex      int         `json:"weekdayIndex"`
	WeekdayLabel      string      `json:"weekdayLabel"`
	StartHM           string      `json:"startHm"`
	EndHM             string      `json:"endHm"`
	OrderMoney        interface{} `json:"orderMoney"`
	AdditionalCharges interface{} `json:"additionalCharges"`
	Status            string      `json:"status"`
	StatusKey         string      `json:"statusKey"`
	Message           string      `json:"message"`
}

func discoverConfigPath() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "DGUTTennisAutoBooker", "config.json")
	}
	return filepath.Join(".local_data", "config.json")
}

func (a *bookingApp) loadConfig() {
	if !a.persist || a.configPath == "" {
		return
	}
	data, err := os.ReadFile(a.configPath)
	if err != nil {
		return
	}

	var cfg persistedConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.state.username = cfg.Username
	a.state.password = cfg.Password
	a.state.serviceURL = cfg.ServiceURL
	a.state.auth.Username = cfg.Username
	a.state.auth.Token = cfg.Token
	a.state.auth.Bearer = ""
	a.state.auth.LoggedIn = false
	a.state.auth.HasSavedCredentials = cfg.Username != "" && cfg.Password != ""
	a.state.week.WeekStart = cfg.WeekStart
	if cfg.SelectedSiteID != 0 {
		a.state.week.SelectedSiteID = cfg.SelectedSiteID
	}
	if cfg.Mode != "" {
		a.state.settings.Mode = cfg.Mode
	}
	if cfg.ManualDate != "" {
		a.state.settings.ManualDate = cfg.ManualDate
	}
	if cfg.SoldOutStrategy != "" {
		a.state.settings.SoldOutStrategy = cfg.SoldOutStrategy
	}
	a.state.settings.RuleHint = a.ruleHintLocked()
	a.state.patterns = map[string]*selectionPattern{}
	for _, item := range cfg.Patterns {
		a.state.patterns[item.Key] = &selectionPattern{
			Key:               item.Key,
			SiteID:            item.SiteID,
			SiteName:          item.SiteName,
			WeekdayIndex:      item.WeekdayIndex,
			WeekdayLabel:      item.WeekdayLabel,
			StartHM:           item.StartHM,
			EndHM:             item.EndHM,
			OrderMoney:        item.OrderMoney,
			AdditionalCharges: item.AdditionalCharges,
			Status:            item.Status,
			StatusKey:         item.StatusKey,
			Message:           item.Message,
		}
		a.refreshPatternStatusLocked(a.state.patterns[item.Key])
	}
	if cfg.Token != "" || a.state.auth.HasSavedCredentials {
		a.state.status = "已加载本地配置"
	}
}

func (a *bookingApp) restoreSavedSession() {
	a.mu.Lock()
	hasSavedCredentials := a.state.username != "" && a.state.password != ""
	hasToken := a.state.auth.Token != ""
	if a.state.serviceURL == "" {
		a.state.serviceURL = defaultKJYYServiceURL
	}
	if !hasSavedCredentials {
		a.mu.Unlock()
		return
	}
	if hasToken {
		a.state.auth.HasSavedCredentials = true
		a.state.auth.Username = a.state.username
		if a.state.auth.Bearer == "" {
			a.state.auth.Bearer = "Bearer " + a.state.auth.Token
		}
		a.state.auth.LoggedIn = true
		a.state.status = "已恢复登录"
		a.appendLogLocked("已从本地配置恢复登录信息")
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	if _, _, err := a.refreshToken(); err != nil {
		a.mu.Lock()
		a.state.auth.HasSavedCredentials = true
		a.state.auth.Username = a.state.username
		a.state.status = "已保存登录信息"
		a.appendLogLocked("检测到已保存的登录信息，但自动恢复失败: " + err.Error())
		a.mu.Unlock()
	}
}

func (a *bookingApp) saveConfigLocked() {
	if !a.persist || a.configPath == "" {
		return
	}
	patterns := make([]persistedPattern, 0, len(a.state.patterns))
	for _, pattern := range a.state.patterns {
		patterns = append(patterns, persistedPattern{
			Key:               pattern.Key,
			SiteID:            pattern.SiteID,
			SiteName:          pattern.SiteName,
			WeekdayIndex:      pattern.WeekdayIndex,
			WeekdayLabel:      pattern.WeekdayLabel,
			StartHM:           pattern.StartHM,
			EndHM:             pattern.EndHM,
			OrderMoney:        pattern.OrderMoney,
			AdditionalCharges: pattern.AdditionalCharges,
			Status:            pattern.Status,
			StatusKey:         pattern.StatusKey,
			Message:           pattern.Message,
		})
	}
	cfg := persistedConfig{
		Username:        a.state.username,
		Password:        a.state.password,
		Token:           a.state.auth.Token,
		WeekStart:       a.state.week.WeekStart,
		SelectedSiteID:  a.state.week.SelectedSiteID,
		Mode:            a.state.settings.Mode,
		ManualDate:      a.state.settings.ManualDate,
		SoldOutStrategy: a.state.settings.SoldOutStrategy,
		Patterns:        patterns,
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(a.configPath), 0o755)
	_ = os.WriteFile(a.configPath, data, 0o644)
}
