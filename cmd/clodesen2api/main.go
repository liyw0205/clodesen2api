// clodesen2api 是应用启动入口，负责组装各层依赖并管理进程生命周期。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"clodesen2api/internal/admin"
	"clodesen2api/internal/checkin"
	"clodesen2api/internal/config"
	"clodesen2api/internal/redemption"
	"clodesen2api/internal/server"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

func main() {
	cfg := config.Load()

	token, err := admin.LoadToken(cfg.Admin.Token, cfg.Admin.TokenFile)
	if err != nil {
		log.Fatalf("[fatal] 加载管理令牌失败: %v", err)
	}
	initialAccounts, err := upstream.ConfiguredAccounts(cfg)
	if err != nil {
		log.Fatalf("[fatal] 解析账号配置失败: %v", err)
	}
	accountStore, err := admin.OpenAccountStore(cfg.Admin.AccountsFile, cfg.Auth.Strategy, initialAccounts)
	if err != nil {
		log.Fatalf("[fatal] 加载管理账号文件失败: %v", err)
	}
	cfg.Auth.Strategy = accountStore.Strategy()
	up, err := upstream.NewWithAccounts(cfg, accountStore.Accounts())
	if err != nil {
		log.Fatalf("[fatal] 初始化上游账号池失败: %v", err)
	}
	usageStats := usage.New()
	adminHandler := admin.NewHandlerWithConfig(up, accountStore, token, cfg.Admin.TokenFile, cfg, usageStats)

	// 启动时预登录一次，避免首个请求卡在登录超时上
	log.Printf("[boot] 正在登录上游账号池 (%d 个账号) %s ...", up.AccountCount(), cfg.Upstream.BaseURL)
	if up.AccountCount() == 0 {
		log.Printf("[boot] 尚未配置上游账号，服务将先启动管理面板")
	} else if err := up.Login(context.Background()); err != nil {
		log.Printf("[boot] 预登录失败(将延后重试): %v", err)
	} else {
		log.Printf("[boot] 预登录成功")
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// 启动每日签到调度（仅在配置了账号密码且 checkin.enable=true 时）
	if cfg.Checkin.Enable && up.AccountCount() > 0 {
		sch, err := checkin.NewScheduler(up, cfg.Checkin.At, cfg.Checkin.Timezone)
		if err != nil {
			log.Printf("[boot] 签到调度初始化失败: %v", err)
		} else {
			log.Printf("[boot] 签到调度已启动，时区=%s 每日 %s", cfg.Checkin.Timezone, cfg.Checkin.At)
			go sch.Run(rootCtx)
			defer sch.Stop()
		}
	} else {
		log.Printf("[boot] 签到已跳过 (enable=%v)", cfg.Checkin.Enable)
	}

	if cfg.Redemption.Enable && cfg.Redemption.Code != "" && up.AccountCount() > 0 {
		sch, err := redemption.NewScheduler(up, cfg.Redemption.At, cfg.Redemption.Code)
		if err != nil {
			log.Printf("[boot] 自动兑换调度初始化失败: %v", err)
		} else {
			log.Printf("[boot] 自动兑换已启动，时区=Asia/Shanghai 每日 %s", cfg.Redemption.At)
			go sch.Run(rootCtx)
			defer sch.Stop()
		}
	} else {
		log.Printf("[boot] 自动兑换已跳过 (enable=%v, codeConfigured=%v)", cfg.Redemption.Enable, cfg.Redemption.Code != "")
	}

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           server.NewHandlerWithUsage(up, usageStats, adminHandler),
		ReadHeaderTimeout: 15 * time.Second,
	}

	// 优雅退出
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Printf("[shutdown] 收到退出信号，等待请求结束...")
		rootCancel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	log.Printf("[boot] clodesen2api listening on %s -> %s (OpenAI-compatible)", cfg.Server.Addr, up.OpenAIBaseURL())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("[fatal] server error: %v", err)
	}
	log.Printf("[shutdown] bye")
}
