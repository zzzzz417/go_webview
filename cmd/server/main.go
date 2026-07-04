package main

import (
	"errors"
	"fmt"
	"log"
	"main/internal/api"
	"main/internal/config"
	"main/internal/routers"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	webview "github.com/webview/webview_go"
)

func init() {
	err := config.Setup()
	if err != nil {
		fmt.Println(err.Error())
		return
	}
}

func main() {
	closeLog := setupLogging()
	defer closeLog()
	defer recoverToLog()

	gin.SetMode(config.Setting.Server.RunMode)
	router := routers.InitRouter()
	cfg := config.Setting.Server
	addr := fmt.Sprintf(":%s", cfg.Port)
	srvUrl := fmt.Sprintf("http://127.0.0.1%s", addr)

	httpSrv := &http.Server{
		Addr:           addr,
		Handler:        router,
		ReadTimeout:    cfg.ReadTimeout,
		WriteTimeout:   cfg.WriteTimeout,
		MaxHeaderBytes: 1 << 20,
	}

	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("服务启动失败:", err)
		}
	}()

	api.StartAppBootstrap()
	w := webview.New(true)
	if w == nil {
		log.Fatal("webview init failed: ensure WebView2 runtime is installed")
	}
	defer w.Destroy()
	w.SetTitle("App")
	w.SetSize(1600, 1000, webview.HintNone)
	w.Navigate(srvUrl)
	w.Run()

	//----------------------阻塞---------------------

	err := httpSrv.Close()
	if err != nil {
		return
	}
}

func setupLogging() func() {
	exePath, err := os.Executable()
	if err != nil {
		return func() {}
	}
	logPath := filepath.Join(filepath.Dir(exePath), "go_webview.log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return func() {}
	}
	log.SetOutput(file)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	log.Println("application starting")
	return func() {
		log.Println("application exiting")
		_ = file.Close()
	}
}

func recoverToLog() {
	if r := recover(); r != nil {
		log.Printf("panic: %v\n%s", r, debug.Stack())
	}
}
