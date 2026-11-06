// clodesen2api 是应用启动入口，负责组装各层依赖并管理进程生命周期。
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"clodesen2api/internal/admin"
	"clodesen2api/internal/admission"
	"clodesen2api/internal/apikey"
	"clodesen2api/internal/backups"
	"clodesen2api/internal/config"
	"clodesen2api/internal/controlstore"
	"clodesen2api/internal/jobs"
	"clodesen2api/internal/modelcooldown"
	"clodesen2api/internal/modelpolicy"
	"clodesen2api/internal/processguard"
	"clodesen2api/internal/server"
	"clodesen2api/internal/updater"
	"clodesen2api/internal/upstream"
	"clodesen2api/internal/usage"
)

var version = "dev"
var goarm = "7"

func main() {
	if run() {
		if err := updater.Restart(); err != nil {
			log.Fatalf("[fatal] 重启失败: %v", err)
		}
	}
}

func run() bool {
	cfg := config.Load()
	dbPath := filepath.Join(filepath.Dir(cfg.Admin.TokenFile), "control.sqlite3")
	backupDir := filepath.Join(filepath.Dir(cfg.Admin.TokenFile), "backups")
	guard, err := processguard.Open(dbPath + ".instance-lock")
	if err != nil {
		log.Fatalf("[fatal] 数据目录被恢复操作锁定: %v", err)
	}
	defer guard.Close()
	validateRestore := func(ctx context.Context, dir string) error { return admin.ValidateControlBackup(ctx, dir, *cfg) }
	if backups.Pending(backupDir) {
		if err := guard.Exclusive(); err != nil {
			log.Fatalf("[fatal] 恢复时仍有其他实例运行")
		}
		if restored, err := backups.ApplyPending(context.Background(), backupDir, dbPath, admin.BackupFiles(cfg), validateRestore); err != nil {
			log.Fatalf("[fatal] 恢复失败，原件位于备份暂存目录: %v", err)
		} else if restored {
			return true
		}
		if err := guard.Shared(); err != nil {
			log.Fatalf("[fatal] 恢复锁降级失败: %v", err)
		}
	}
	control, err := controlstore.Open(dbPath)
	if err != nil {
		log.Fatalf("[fatal] 加载控制数据库失败: %v", err)
	}
	defer control.Close()
	settings, err := config.OpenSettings(context.Background(), control, cfg)
	if err != nil {
		log.Fatalf("[fatal] 加载管理配置失败: %v", err)
	}
	startup := settings.Startup()
	cfg = &startup
	capacity, err := admission.Open(control)
	if err != nil {
		log.Fatalf("[fatal] 加载请求准入失败: %v", err)
	}
	policies, err := modelpolicy.Open(control)
	if err != nil {
		log.Fatalf("[fatal] 加载模型策略失败: %v", err)
	}
	cooldowns, err := modelcooldown.Open(control)
	if err != nil {
		log.Fatalf("[fatal] 加载模型冷却失败: %v", err)
	}
	keysFile := cfg.API.KeysFile
	if keysFile == "" {
		keysFile = "data/api-keys.json"
	}
	keyStore, err := apikey.OpenControl(control, keysFile)
	if err != nil {
		log.Fatalf("[fatal] 加载 API key 存储失败: %v", err)
	}
	apiAuth, err := apikey.NewAuthenticator(cfg.API.Key, keyStore, cfg.API.AllowNoAuth, cfg.API.TrustedProxies)
	if err != nil {
		log.Fatalf("[fatal] API key 配置无效: %v", err)
	}

	token, err := admin.LoadToken(cfg.Admin.Token, cfg.Admin.TokenFile)
	if err != nil {
		log.Fatalf("[fatal] 加载管理令牌失败: %v", err)
	}
	initialAccounts, err := upstream.ConfiguredAccounts(cfg)
	if err != nil {
		log.Fatalf("[fatal] 解析账号配置失败: %v", err)
	}
	accountStore, err := admin.OpenControlAccounts(control, cfg.Admin.AccountsFile, cfg.Auth.Strategy, initialAccounts)
	if err != nil {
		log.Fatalf("[fatal] 加载管理账号文件失败: %v", err)
	}
	if err := settings.ImportLegacyStrategy(context.Background(), accountStore.Strategy()); err != nil {
		log.Fatalf("[fatal] 迁移账号策略失败: %v", err)
	}
	startup = settings.Startup()
	cfg = &startup
	up, err := upstream.NewWithAccounts(cfg, accountStore.Accounts())
	if err != nil {
		log.Fatalf("[fatal] 初始化上游账号池失败: %v", err)
	}
	up.SetAccountStateRecorder(accountStore.UpdateRuntimeState)
	up.SetModelCooldowns(cooldowns)
	usageStats, usageErr := usage.NewControl(control, filepath.Join(filepath.Dir(cfg.Admin.TokenFile), "usage.json"))
	if usageErr != nil {
		log.Fatalf("[fatal] 加载持久化调用统计失败: %v", usageErr)
	}
	defer func() {
		if err := usageStats.Close(); err != nil {
			log.Printf("[usage] 关闭审计存储失败: %v", err)
		}
	}()
	adminHandler := admin.NewServiceWithConfig(up, accountStore, token, cfg.Admin.TokenFile, cfg, usageStats)
	adminHandler.SetKeyStore(keyStore)
	adminHandler.SetControlStore(control)
	if err := adminHandler.MigrateControlData(context.Background()); err != nil {
		log.Fatalf("[fatal] 迁移管理数据失败: %v", err)
	}
	adminHandler.SetSettings(settings)
	adminHandler.SetModelPolicies(policies)
	adminHandler.SetModelCooldowns(cooldowns)
	adminHandler.SetAdmission(capacity)
	backupStore, err := backups.New(control, filepath.Join(filepath.Dir(cfg.Admin.TokenFile), "backups"), version, admin.BackupFiles(cfg))
	if err != nil {
		log.Fatalf("[fatal] 初始化备份服务失败: %v", err)
	}
	backupStore.SetValidator(validateRestore)
	adminHandler.SetBackups(backupStore)

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
	jobStore, err := jobs.Open(control)
	if err != nil {
		log.Fatalf("[fatal] 加载后台任务失败: %v", err)
	}
	if err := jobStore.ImportScheduler(rootCtx, filepath.Join(filepath.Dir(cfg.Admin.TokenFile), "redemption-scheduler.json")); err != nil {
		log.Fatalf("[fatal] 迁移自动兑换记录失败: %v", err)
	}
	jobRunner := adminHandler.StartJobs(rootCtx, jobStore)
	defer jobRunner.Close()
	defer func() {
		if err := keyStore.FlushUsage(); err != nil {
			log.Printf("[keys] 保存最近使用时间失败: %v", err)
		}
	}()
	var background sync.WaitGroup
	background.Add(3)
	go func() {
		defer background.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := keyStore.FlushUsage(); err != nil {
					log.Printf("[keys] 保存最近使用时间失败: %v", err)
				}
			case <-rootCtx.Done():
				return
			}
		}
	}()
	go func() {
		defer background.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(rootCtx, 35*time.Second)
				up.CalibrateCreditCooldowns(ctx)
				cancel()
			case <-rootCtx.Done():
				return
			}
		}
	}()

	go func() { defer background.Done(); adminHandler.RunSchedules(rootCtx) }()

	var handlers sync.WaitGroup
	var handlerMu sync.Mutex
	handlersClosed := false
	handler := adminHandler.WithAccountSync(server.NewHandlerWithAdmission(up, usageStats, apiAuth, policies, capacity, adminHandler))
	srv := &http.Server{
		Addr: cfg.Server.Addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlerMu.Lock()
			if handlersClosed {
				handlerMu.Unlock()
				http.Error(w, "service stopping", 503)
				return
			}
			handlers.Add(1)
			handlerMu.Unlock()
			defer handlers.Done()
			handler.ServeHTTP(w, r)
		}),
		BaseContext:       func(net.Listener) context.Context { return rootCtx },
		ReadHeaderTimeout: 15 * time.Second,
	}
	restartSignal := make(chan struct{}, 1)
	var restarting atomic.Bool
	adminHandler.SetUpdater(version, goarm, func() error {
		restarting.Store(true)
		select {
		case restartSignal <- struct{}{}:
		default:
		}
		return nil
	})
	restoreSignal := make(chan struct{}, 1)
	var restoring atomic.Bool
	adminHandler.SetRestoreRequest(func() bool {
		if err := guard.Exclusive(); err != nil {
			return false
		}
		restoring.Store(true)
		restoreSignal <- struct{}{}
		return true
	})

	// 优雅退出
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(sigCh)
		select {
		case <-sigCh:
		case <-restoreSignal:
		case <-restartSignal:
		}
		log.Printf("[shutdown] 收到退出信号，等待请求结束...")
		rootCancel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
	}()

	log.Printf("[boot] clodesen2api listening on %s -> %s (OpenAI-compatible)", cfg.Server.Addr, up.OpenAIBaseURL())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("[fatal] server error: %v", err)
	}
	<-shutdownDone
	handlerMu.Lock()
	handlersClosed = true
	handlerMu.Unlock()
	handlers.Wait()
	jobRunner.Close()
	background.Wait()
	adminHandler.Close()
	if err := usageStats.Close(); err != nil {
		log.Printf("[shutdown] 审计关闭失败")
	}
	if restoring.Load() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := backupStore.Create(ctx, true)
		cancel()
		if err != nil {
			_ = backupStore.Disarm()
			log.Printf("[restore] 排空后的备份失败，取消恢复")
		}
	}
	log.Printf("[shutdown] bye")
	return restoring.Load() || restarting.Load()
}
