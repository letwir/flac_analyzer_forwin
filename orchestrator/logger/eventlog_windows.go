// Package logger provides structured CUI color streaming, log levels, and Windows EventLog bindings.
// Mor: OS Event Log & Fatal Screen Presentation (IO Monad)
package logger

import (
	"log"
	"time"

	"golang.org/x/sys/windows/svc/eventlog"
)

// SetupEventLog attempts to register and open the Windows Application Event Log source.
// SideEffectFn: SetupEventLog
func SetupEventLog() *eventlog.Log {
	const sourceName = "FlacAnalyzerOrchestrator"
	// イベントソースのインストールを試みます（未登録時のみ反映）
	_ = eventlog.InstallAsEventCreate(sourceName, eventlog.Error|eventlog.Warning|eventlog.Info)

	elog, err := eventlog.Open(sourceName)
	if err != nil {
		log.Printf("Warning: Failed to open Windows event log (maybe run as non-admin?): %v\n", err)
		return nil
	}
	return elog
}

// FatalErrorLog displays a bilingual, beautifully formatted fatal diagnostic block and halts execution.
// SideEffectFn: FatalErrorLog
func FatalErrorLog(titleJP, descJP, hintJP, titleEN, descEN, hintEN string, err error) {
	log.Printf("==========================================================================")
	log.Printf(" ❌ 【エラー発生 / ERROR OCCURRED】 %s", titleJP)
	log.Printf(" --------------------------------------------------------------------------")
	log.Printf(" [JP] %s", descJP)
	if hintJP != "" {
		log.Printf(" 💡 [ヒント] %s", hintJP)
	}
	log.Printf(" --------------------------------------------------------------------------")
	log.Printf(" [EN] %s", descEN)
	if hintEN != "" {
		log.Printf(" 💡 [Hint] %s", hintEN)
	}
	if err != nil {
		log.Printf(" 🔍 [Details/詳細] %v", err)
	}
	log.Printf("==========================================================================")
	log.Printf("※ コンソールが即座に閉じるのを防ぐため、5秒間待機いたしますわ...")
	time.Sleep(5 * time.Second)
	log.Fatalf("Orchestrator terminated due to fatal error.")
}
