// talkcards 沟通牌 Go 后端（单二进制，前端静态资源已嵌入）。
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/labstack/echo/v4"

	"talkcards/backend/internal/hub"
	"talkcards/backend/internal/logging"
	"talkcards/backend/internal/store"
	"talkcards/backend/internal/wssrv"
	"talkcards/backend/web"
)

var version = "dev"

// envOr 环境变量缺省值兜底：CLI 显式参数 > 环境变量 > 内置默认
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	var (
		port             = flag.StringP("port", "p", envOr("PORT", "8000"), "服务监听端口（默认取 PORT 环境变量，缺省 8000）")
		host             = flag.String("host", "", "服务监听地址（默认所有接口）")
		reconnectTimeout = flag.Int("reconnect-timeout", 600, "游戏中断线后的重连等待时限（秒），0 表示无限等待")
		dbPath           = flag.String("db", "./talkcards.db", "战绩 SQLite 数据库路径")
		logLevel         = flag.String("log-level", "info", "日志级别：debug|info|warn|error")
		logFormat        = flag.String("log-format", "text", "日志格式：text|json")
		staticDir        = flag.String("static-dir", "", "前端静态资源目录（磁盘加载，替代嵌入资源；缺省用嵌入的 web/static）")
		botDelayMinMs    = flag.Int("bot-delay-min-ms", 50, "机器人出牌/叫分随机延迟下限（毫秒）")
		botDelayMaxMs    = flag.Int("bot-delay-max-ms", 150, "机器人出牌/叫分随机延迟上限（毫秒），测试可调小加速")
		showVersion      = flag.BoolP("version", "v", false, "打印版本并退出")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("talkcards %s\n", version)
		return
	}

	logger, err := logging.New(logging.Options{Level: *logLevel, Format: *logFormat})
	if err != nil {
		fmt.Fprintf(os.Stderr, "日志初始化失败: %v\n", err)
		os.Exit(2)
	}
	slog.SetDefault(logger)

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	// 战绩持久化（恒开；登录仅以用户名为唯一身份，无需落库）
	st, err := store.Open(*dbPath)
	if err != nil {
		logger.Error("打开数据库失败", "path", *dbPath, "err", err)
		os.Exit(1)
	}
	defer st.Close()

	wsServer := wssrv.New(logger)
	gameServer := hub.New(st, time.Duration(*reconnectTimeout)*time.Second, logger)
	gameServer.SetBotDelay(time.Duration(*botDelayMinMs)*time.Millisecond, time.Duration(*botDelayMaxMs)*time.Millisecond)
	gameServer.Register(wsServer)
	defer gameServer.Flush() // 等待异步落库排空（LIFO：先于 st.Close 执行）
	e.GET("/ws", echo.WrapHandler(wsServer))

	var staticFS fs.FS
	if *staticDir != "" {
		// 磁盘静态资源目录（开发用，免重新构建二进制即可改前端）
		staticFS = os.DirFS(*staticDir)
		if _, err := fs.Stat(staticFS, "index.html"); err != nil {
			logger.Error("静态资源目录缺少 index.html", "dir", *staticDir)
			os.Exit(1)
		}
	} else if f, err := fs.Sub(web.Assets, "static"); err != nil {
		logger.Error("嵌入静态资源解包失败", "err", err)
		os.Exit(1)
	} else {
		staticFS = f
	}
	e.StaticFS("/", staticFS)

	// 收到中断信号后优雅关闭：停收新连接、等待在途请求，随后 main 正常返回走 defer
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.Shutdown(shutdownCtx); err != nil {
			logger.Error("关闭服务失败", "err", err)
		}
	}()

	addr := net.JoinHostPort(*host, *port)
	logger.Info("服务启动", "version", version, "addr", addr, "db", *dbPath)

	// 打印可点击访问地址：本机回环 + 所有非回环 IPv4（局域网 IP，方便他人直连）
	printAccessURLs(logger, *host, *port)
	if err := e.Start(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("服务异常退出", "err", err)
		os.Exit(1)
	}
}

// printAccessURLs 启动时打印可直接点击访问的地址：
// host 非空时只打印该地址；否则打印回环地址 + 所有非回环 IPv4（局域网 IP）。
func printAccessURLs(logger *slog.Logger, host, port string) {
	if host != "" {
		logger.Info(fmt.Sprintf("访问地址: http://%s", net.JoinHostPort(host, port)))
		return
	}
	logger.Info(fmt.Sprintf("访问地址: http://127.0.0.1:%s", port))
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok {
				if ip := ipNet.IP.To4(); ip != nil {
					logger.Info(fmt.Sprintf("访问地址: http://%s", net.JoinHostPort(ip.String(), port)))
				}
			}
		}
	}
}
