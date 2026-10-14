package main

import (
	"log"
	"video-master/database"
	"video-master/services"
)

// 数据库后端回落保护的两个绑定（D-DBG-03、D-DBG-04）：启动出错页按 GetStartupBackendGuard 给出专门说明，
// 「这次改用本机 SQLite」二次确认后调 AcceptSQLiteAfterFallbackBlock。

// acceptSQLiteAfterFallbackBlock 是 database.AcceptSQLiteAfterFallbackBlock 的替换点，测试里换掉。
var acceptSQLiteAfterFallbackBlock = database.AcceptSQLiteAfterFallbackBlock

// GetStartupBackendGuard 返回本次启动的回落保护结果：被拦下时带上次的 PostgreSQL 位置、这次按优先级读过的
// 配置文件（标明是否存在）与推荐放配置的位置（数据目录下的 .env）。只读内存，不需要数据库连接。
func (a *App) GetStartupBackendGuard() database.StartupBackendGuard {
	guard := database.StartupGuard()
	log.Printf("API GetStartupBackendGuard blocked=%v", guard.Blocked)
	return guard
}

// AcceptSQLiteAfterFallbackBlock 只在本次启动被拦下时可用：把记录改成 sqlite，再走现有的 RelaunchApp 重启。
// 不写任何 .env；记录写不进去时不重启，把错误交回前端。
func (a *App) AcceptSQLiteAfterFallbackBlock() (*services.RelaunchResult, error) {
	if err := acceptSQLiteAfterFallbackBlock(); err != nil {
		log.Printf("API AcceptSQLiteAfterFallbackBlock err=%v", err)
		return nil, err
	}
	log.Printf("API AcceptSQLiteAfterFallbackBlock recorded sqlite, relaunching")
	return a.RelaunchApp()
}
