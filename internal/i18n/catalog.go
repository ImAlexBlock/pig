// Package i18n holds the user-facing message catalog for chat replies.
// Prompt text stays English: only what a person reads is translated.
package i18n

// Keys are the catalog entry names. They are constants so a typo fails to
// compile instead of rendering an empty reply.
const (
	KeyHelp            = "help"
	KeyUnknownCommand  = "unknown_command"
	KeyNewSession      = "new_session"
	KeyNoSessions      = "no_sessions"
	KeySessionsHint    = "sessions_hint"
	KeyResumeUsage     = "resume_usage"
	KeySessionNotFound = "session_not_found"
	KeyResumed         = "resumed"
	KeyStoppingDropped = "stopping_dropped"
	KeyStopping        = "stopping"
	KeyQueueCleared    = "queue_cleared"
	KeyNothingRunning  = "nothing_running"
	KeyCompactNothing  = "compact_nothing"
	KeyCompacted       = "compacted"
	KeyModelShow       = "model_show"
	KeyModelUnknown    = "model_unknown"
	KeyModelSet        = "model_set"
	KeyNoJobs          = "no_jobs"
	KeyStatusUnknown   = "status_unknown"
	KeyStatusSession   = "status_session"
	KeyStatusModel     = "status_model"
	KeyStatusUsage     = "status_usage"
	KeyStatusJobs      = "status_jobs"
	KeyError           = "error"
	KeyPairingFailed   = "pairing_failed"
	KeyPaired          = "paired"
	KeyPairingRequired = "pairing_required"
	KeyAppended        = "appended"
	KeyStoppedAfter    = "stopped_after"
	KeyNoOutput        = "no_output"
	KeyReplyLimit      = "reply_limit"
	KeyJobDone         = "job_done"
	KeyJobOther        = "job_other"
	KeyProgressTools   = "progress_tools"
	KeyProgressCtx     = "progress_ctx"
	KeyLangSet         = "lang_set"
	KeyLangShow        = "lang_show"
	KeyLangUsage       = "lang_usage"
	KeyLangUnknown     = "lang_unknown"
)

var en = map[string]string{
	KeyHelp: `Commands
/new — start a new session
/sessions [n] — recent sessions, * is this chat
/resume <id> — resume a session
/stop — cancel the running turn
/status — session, model, usage, jobs
/compact — compact the context now
/model [ref] — show or set the model
/jobs — recent background jobs
/lang [en|zh|zh-Hant] — show or set the reply language
/help — this list`,
	KeyUnknownCommand:  "unknown command; /help for the list",
	KeyNewSession:      "new session %s (%s)",
	KeyNoSessions:      "no sessions",
	KeySessionsHint:    "/resume <id> to switch, /new to start fresh",
	KeyResumeUsage:     "usage: /resume <session-id>",
	KeySessionNotFound: "session not found: %s",
	KeyResumed:         "resumed %s",
	KeyStoppingDropped: "stopping (dropped %d queued)",
	KeyStopping:        "stopping",
	KeyQueueCleared:    "queue cleared (%d dropped)",
	KeyNothingRunning:  "nothing running",
	KeyCompactNothing:  "nothing to compact",
	KeyCompacted:       "compacted with %s: %d → %d tokens",
	KeyModelShow:       "model: %s",
	KeyModelUnknown:    "unknown model: %s",
	KeyModelSet:        "model set to %s",
	KeyNoJobs:          "no jobs",
	KeyStatusUnknown:   "session: unknown",
	KeyStatusSession:   "session: %s",
	KeyStatusModel:     "model: %s (window %d)",
	KeyStatusUsage:     "usage: in %d out %d cost $%.4f",
	KeyStatusJobs:      "jobs running: %d",
	KeyError:           "error: %s",
	KeyPairingFailed:   "pairing failed: %s",
	KeyPaired:          "paired. send a message to start.",
	KeyPairingRequired: "pairing required: /pair <code>",
	KeyAppended:        "appended (#%d) · handled after the current step",
	KeyStoppedAfter:    "⏳ stopped after %s",
	KeyNoOutput:        "(no output)",
	KeyReplyLimit:      "\n[… reply limit reached; full text: session://%s]",
	KeyJobDone:         "job %s done: %s",
	KeyJobOther:        "job %s %s: %s",
	KeyProgressTools:   "%d tools",
	KeyProgressCtx:     "ctx %s",
	KeyLangSet:         "lang: %s",
	KeyLangShow:        "lang: %s",
	KeyLangUsage:       "usage: /lang en|zh|zh-Hant",
	KeyLangUnknown:     "unsupported language: %s (en, zh, zh-Hant)",
}

var zh = map[string]string{
	KeyHelp: `命令
/new — 新建会话
/sessions [n] — 最近会话，* 表示当前会话
/resume <id> — 切换会话
/stop — 取消正在运行的轮次
/status — 会话、模型、用量、任务
/compact — 立即压缩上下文
/model [ref] — 查看或切换模型
/jobs — 最近的后台任务
/lang [en|zh|zh-Hant] — 查看或切换回复语言
/help — 显示本列表`,
	KeyUnknownCommand:  "未知命令；发送 /help 查看列表",
	KeyNewSession:      "新会话 %s（%s）",
	KeyNoSessions:      "没有会话",
	KeySessionsHint:    "/resume <id> 切换，/new 新建",
	KeyResumeUsage:     "用法：/resume <会话-id>",
	KeySessionNotFound: "找不到会话：%s",
	KeyResumed:         "已切换到 %s",
	KeyStoppingDropped: "正在停止（丢弃 %d 条排队）",
	KeyStopping:        "正在停止",
	KeyQueueCleared:    "队列已清空（丢弃 %d 条）",
	KeyNothingRunning:  "当前没有运行的任务",
	KeyCompactNothing:  "没有可压缩的内容",
	KeyCompacted:       "已用 %s 压缩：%d → %d tokens",
	KeyModelShow:       "模型：%s",
	KeyModelUnknown:    "未知模型：%s",
	KeyModelSet:        "模型已切换为 %s",
	KeyNoJobs:          "没有任务",
	KeyStatusUnknown:   "会话：未知",
	KeyStatusSession:   "会话：%s",
	KeyStatusModel:     "模型：%s（窗口 %d）",
	KeyStatusUsage:     "用量：输入 %d 输出 %d 费用 $%.4f",
	KeyStatusJobs:      "运行中任务：%d",
	KeyError:           "错误：%s",
	KeyPairingFailed:   "配对失败：%s",
	KeyPaired:          "配对成功。发一条消息即可开始。",
	KeyPairingRequired: "需要配对：/pair <code>",
	KeyAppended:        "已追加（第 %d 条）· 当前步骤结束后处理",
	KeyStoppedAfter:    "⏳ 已停止，耗时 %s",
	KeyNoOutput:        "（无输出）",
	KeyReplyLimit:      "\n[… 已达回复上限；全文见 session://%s]",
	KeyJobDone:         "任务 %s 完成：%s",
	KeyJobOther:        "任务 %s %s：%s",
	KeyProgressTools:   "%d 步",
	KeyProgressCtx:     "上下文 %s",
	KeyLangSet:         "语言：%s",
	KeyLangShow:        "语言：%s",
	KeyLangUsage:       "用法：/lang en|zh|zh-Hant",
	KeyLangUnknown:     "不支持的语言：%s（en、zh、zh-Hant）",
}

var zhHant = map[string]string{
	KeyHelp: `指令
/new — 建立新工作階段
/sessions [n] — 最近的交談，* 表示目前交談
/resume <id> — 切換工作階段
/stop — 取消執行中的回合
/status — 工作階段、模型、用量、任務
/compact — 立即壓縮上下文
/model [ref] — 檢視或切換模型
/jobs — 最近的背景任務
/lang [en|zh|zh-Hant] — 檢視或切換回覆語言
/help — 顯示本清單`,
	KeyUnknownCommand:  "未知指令；請傳送 /help 查看清單",
	KeyNewSession:      "新工作階段 %s（%s）",
	KeyNoSessions:      "沒有工作階段",
	KeySessionsHint:    "/resume <id> 切換，/new 建立",
	KeyResumeUsage:     "用法：/resume <工作階段-id>",
	KeySessionNotFound: "找不到工作階段：%s",
	KeyResumed:         "已切換至 %s",
	KeyStoppingDropped: "正在停止（捨棄 %d 筆排隊）",
	KeyStopping:        "正在停止",
	KeyQueueCleared:    "佇列已清空（捨棄 %d 筆）",
	KeyNothingRunning:  "目前沒有執行中的任務",
	KeyCompactNothing:  "沒有可壓縮的內容",
	KeyCompacted:       "已用 %s 壓縮：%d → %d tokens",
	KeyModelShow:       "模型：%s",
	KeyModelUnknown:    "未知模型：%s",
	KeyModelSet:        "模型已切換為 %s",
	KeyNoJobs:          "沒有任務",
	KeyStatusUnknown:   "工作階段：未知",
	KeyStatusSession:   "工作階段：%s",
	KeyStatusModel:     "模型：%s（視窗 %d）",
	KeyStatusUsage:     "用量：輸入 %d 輸出 %d 費用 $%.4f",
	KeyStatusJobs:      "執行中任務：%d",
	KeyError:           "錯誤：%s",
	KeyPairingFailed:   "配對失敗：%s",
	KeyPaired:          "配對成功。傳送一則訊息即可開始。",
	KeyPairingRequired: "需要配對：/pair <code>",
	KeyAppended:        "已附加（第 %d 筆）· 目前步驟結束後處理",
	KeyStoppedAfter:    "⏳ 已停止，耗時 %s",
	KeyNoOutput:        "（無輸出）",
	KeyReplyLimit:      "\n[… 已達回覆上限；全文見 session://%s]",
	KeyJobDone:         "任務 %s 完成：%s",
	KeyJobOther:        "任務 %s %s：%s",
	KeyProgressTools:   "%d 步",
	KeyProgressCtx:     "上下文 %s",
	KeyLangSet:         "語言：%s",
	KeyLangShow:        "語言：%s",
	KeyLangUsage:       "用法：/lang en|zh|zh-Hant",
	KeyLangUnknown:     "不支援的語言：%s（en、zh、zh-Hant）",
}

var catalogs = map[Lang]map[string]string{
	EN:  en,
	ZH:  zh,
	ZHT: zhHant,
}
