package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

func executableRoot() string {
	if value := os.Getenv("EASYWORK_ROOT"); value != "" {
		value, _ = filepath.Abs(value)
		return value
	}
	exe, err := os.Executable()
	if err == nil {
		return filepath.Dir(exe)
	}
	wd, _ := os.Getwd()
	return wd
}
func openBrowser(url string) {
	if runtime.GOOS == "windows" {
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
}
func main() {
	lan := flag.Bool("lan", false, "允许局域网访问（无登录或访问令牌）")
	noBrowser := flag.Bool("no-browser", false, "不自动打开浏览器")
	flag.Parse()
	root := executableRoot()
	appLog := newDailyLogWriter(filepath.Join(root, "log"), "easywork", os.Stdout)
	defer appLog.Close()
	log.SetOutput(appLog)
	store, err := NewStore(filepath.Join(root, "history"))
	if err != nil {
		log.Fatalf("初始化运行数据失败: %v", err)
	}
	models, err := scanModels(root)
	if err != nil {
		log.Printf("扫描模型失败: %v", err)
	}
	vlPath := filepath.Join(root, "gguf", "vl", "PaddleOCR-VL-1.6-GGUF.gguf")
	if vl, vlErr := readGGUF(vlPath); vlErr == nil {
		models = append(models, vl)
	}
	hardware := newHardwareMonitor()
	manager := NewModelManager(root, store, hardware, models)
	app := &App{root: root, store: store, models: manager, tasks: NewTaskManager(), hardware: hardware, modelList: models}
	address := "127.0.0.1:18765"
	if *lan {
		address = "0.0.0.0:18765"
	}
	server := &http.Server{Addr: address, Handler: app.routes(), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		manager.Close()
		log.Fatalf("监听失败: %v", err)
	}
	display := "http://127.0.0.1:18765"
	if !*noBrowser {
		go func() { time.Sleep(300 * time.Millisecond); openBrowser(display) }()
	}
	log.Printf("服务已启动：%s（LAN=%v）", display, *lan)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP 服务异常: %v", err)
		}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
	log.Printf("正在退出并清理模型进程…")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	manager.Close()
}
