package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// jsonResult marshals v to a JSON tool result.
func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// File ini mengimplementasikan "kumpulan tool orkestrasi lintas task" P2
// (docs/orkestrasi benchmark §2 P2). Ini adalah tool host, karena butuh akses
// ke Manager (Store milik task mana pun), Engine (pause), dan proses pembuatan
// task, sehingga tinggal di layer server.
// Tool jenis baca mengarahkan "tool per-task yang sudah ada" untuk berjalan di
// atas store task target (membuat ToolSet sementara lalu memanggil tool yang
// sesuai), sehingga logika yang sama persis dapat dipakai ulang; tool jenis
// kontrol (spawn/pause) langsung memanggil Manager/Engine.
// Tool-tool ini di-seed ke tabel tools seperti tool traffic, dan diikat per
// agent (hanya terlihat jika diikat ke agent orkestrasi).

// hostTools is the runtime host-tool provider fed to ToolAugment: traffic tools
// (gated by capture) + cross-task orchestration tools + user-defined custom tools.
// The second return is the names of custom tools flagged `deferred` (schema
// withheld, routed via SearchExtraTools/ExecuteExtraTool). Per-agent binding still
// decides who actually sees any of them.
//
//nolint:unused // used as the hostTools provider in wireAgentAugment
func (s *Server) hostTools() ([]actool.CoreTool, map[string][]string) {
	tools := append(s.m.HostTools(), s.orchestrationTools()...)
	tools = append(tools, s.findingRetestTools()...)
	tools = append(tools, s.platformTools()...) // tool operasi platform (membuat/mengubah skill/tool/MCP, dipakai oleh Auto)
	custom, err := s.customTools()
	if err != nil {
		log.Printf("[custom-tool] gagal memuat: %v", err)
		return tools, nil
	}
	tools = append(tools, custom...)
	// deferred custom tools → name -> its bound agent keys. ToolAugment turns a
	// name into a deferred entry only for agents it's actually bound to (so we don't
	// advertise a tool the per-agent binding will drop from the callable set).
	deferred := map[string][]string{}
	rows, _ := s.m.pg.ListCustomTools()
	for _, t := range rows {
		if t.Deferred && t.Enabled {
			deferred[t.Key] = t.Agents
		}
	}
	return tools, deferred
}

// orchestrationTools returns the cross-task tool set. Bound per-agent via the
// tools table (default: no binding — opt-in for orchestration agents).
func (s *Server) orchestrationTools() []actool.CoreTool {
	return []actool.CoreTool{
		s.toolListTasks(),
		s.toolListLLMProfiles(),
		s.toolSpawnTask(),
		s.toolPauseTask(),
		s.toolGetTaskGraph(),
		s.toolListTaskFindings(),
		s.toolAddHint(),
		s.toolGetWorkerTrace(),
		s.toolListWorkerTraces(),
		s.toolSearchWorkerTraces(),
		s.toolGetTaskNodeDetail(),
		s.toolUpdateFindingReport(),
		s.toolGetFindingTraffic(),
		s.toolBindFindingTraffic(),
	}
}

// --- schema helpers ---

func strParam(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// parseProfileID reads an LLM profile id from a tool arg that may arrive as a JSON
// number (5) or a numeric string ("5"); returns 0 when absent/unparseable.
func parseProfileID(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(str), 10, 64)
		return v
	}
	return 0
}

func objSchema(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		m["required"] = req
	}
	return m
}

func roTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:   func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func wrTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// delegateToTask resolves the `task_id` in the input, builds a ToolSet bound to
// that task's store, strips task_id, and calls the chosen per-task tool — so the
// cross-task read reuses the exact in-task logic against another task.
func (s *Server) delegateToTask(ctx context.Context, in json.RawMessage, pick func(*agent.ToolSet) actool.CoreTool) (actool.Result, error) {
	var head struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(in, &head)
	if strings.TrimSpace(head.TaskID) == "" {
		return actool.Errorf("task_id wajib diisi"), nil
	}
	t, ok := s.m.Task(head.TaskID)
	if !ok {
		return actool.Errorf("task tidak ditemukan: " + head.TaskID), nil
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(in, &m)
	delete(m, "task_id")
	inner, _ := json.Marshal(m)
	tsx := agent.NewToolSet(t.Store, "orchestrator")
	if s.m.Assets() != nil {
		tsx.SetAssetStore(s.m.Assets(), s.m.Assets().Companies())
	}
	tsx.SetNotify(t.Notify)         // wake-up umum (operasi tulis tanpa callback khusus lewat ini; tool baca adalah no-op)
	tsx.SetNotifyHint(t.NotifyHint) // add_hint -> mencatat satu baris "manusia menambahkan N catatan strategi: ..." lalu trigger dan membangunkan planner
	return pick(tsx).Call(ctx, inner, nil)
}

// --- tools ---

func (s *Server) toolListTasks() actool.CoreTool {
	return roTool("list_tasks",
		"列出所有任务(id/描述/目标/状态/运行时长/父任务/LLM 配置)，编排 agent 用它掌握全局、看哪些任务卡太久、各自用哪个 LLM。运行时长：运行中=创建→现在，终态=创建→最后活动(秒)。llm_profile：任务 planner/worker 用的配置名，(激活配置)=跟随全局激活。",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			lastAct, _ := s.m.PG().LastActivityAll()
			// id -> name to resolve each task's pinned LLM profile.
			profName := map[int64]string{}
			if profs, err := s.m.pg.ListProfiles(); err == nil {
				for _, p := range profs {
					profName[p.ID] = p.Name
				}
			}
			out := make([]map[string]any, 0)
			for _, t := range s.m.List() {
				status := s.deriveTaskStatus(t)
				end := lastAct[t.ExpID]
				if live := s.engine.LastActivity(t.ID); live > end {
					end = live
				}
				dur := int64(0)
				if status == "running" {
					dur = time.Now().Unix() - t.CreatedAt
				} else if end > t.CreatedAt {
					dur = end - t.CreatedAt
				}
				row := map[string]any{"id": t.ID, "description": t.Description, "goal": t.Goal, "status": status, "run_seconds": dur}
				if t.ParentRef != "" {
					row["parent_ref"] = t.ParentRef
				}
				llmState := t.llmStateSnapshot()
				if llmState.ProfileID == nil {
					row["llm_profile"] = "(konfigurasi aktif)"
				} else if n, ok := profName[*llmState.ProfileID]; ok {
					row["llm_profile"] = n
				} else {
					row["llm_profile"] = fmt.Sprintf("#%d(sudah dihapus)", *llmState.ProfileID)
				}
				out = append(out, row)
			}
			return jsonResult(out)
		})
}

// toolListLLMProfiles lists the available LLM profiles (name/model/active) so an
// orchestration agent can pick one for spawn_task's llm_profile. Never leaks keys.
func (s *Server) toolListLLMProfiles() actool.CoreTool {
	return roTool("list_llm_profiles",
		"列出可用的 LLM 配置(profile)：id、名称、模型、格式、是否为当前激活配置。用 id 给 spawn_task 的 llm_profile_id 参数指定子任务专属 LLM（如侦察用便宜模型、利用用强模型）。不含 API Key。",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			profs, err := s.m.pg.ListProfiles()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out := make([]map[string]any, 0, len(profs))
			for _, p := range profs {
				out = append(out, map[string]any{
					"id": p.ID, "name": p.Name, "model": p.Model, "format": p.Format, "is_active": p.IsDefault,
				})
			}
			return jsonResult(map[string]any{"profiles": out})
		})
}

func (s *Server) toolSpawnTask() actool.CoreTool {
	return wrTool("spawn_task",
		"新建一个子任务并启动探索引擎，返回 task_id。用于把一件事(如一道题/一个目标)派成独立任务。parent_ref 可选：填当前编排关联的父任务 id 做父子关联。",
		objSchema(map[string]any{
			"description":            strParam("任务描述(简短标题)"),
			"goal":                   strParam("任务目标(要达成什么)"),
			"parent_ref":             strParam("可选：父任务 id(做父子关联)"),
			"source_task_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": fmt.Sprintf("可选：只读继承的来源任务 id 列表(最多 %d 个)。子任务可只读引用这些任务已探明的资产/结论作为起点；与 parent_ref 的纯父子指针不同，这是内容继承。", db.MaxTaskSourceCount)},
			"llm_profile_id":         map[string]any{"type": "integer", "description": "可选：指定本子任务 planner/worker 用的 LLM 配置 id(见 list_llm_profiles)；留空则继承父任务、再回退全局激活配置"},
			"timeout_seconds":        map[string]any{"type": "integer", "description": "可选：任务级超时(秒)。到点后触发优雅收尾并进入 timeout 终态；留空或 0 = 不限时"},
			"plan_heartbeat_seconds": map[string]any{"type": "integer", "description": "可选：planner 心跳触发间隔(秒)。距上轮规划结束/任务开始满该值且期间无触发 → 触发一轮规划(兜底死锁 + 唤醒去监督飞行中的 worker)。留空或 0 = 默认 600(10min)；"},
			"seed_first_intent":      map[string]any{"type": "boolean", "description": "可选：对于简单任务可开启，创建时直接下发一条种子意图(内容=描述+目标)让 worker 免等首轮 planner 直接开跑测试；默认 false(走标准先规划再执行)。"},
		}, "description", "goal"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Description          string          `json:"description"`
				Goal                 string          `json:"goal"`
				ParentRef            string          `json:"parent_ref"`
				SourceTaskIDs        []string        `json:"source_task_ids"`
				LLMProfileID         json.RawMessage `json:"llm_profile_id"`
				TimeoutSeconds       int             `json:"timeout_seconds"`
				PlanHeartbeatSeconds int             `json:"plan_heartbeat_seconds"`
				SeedFirstIntent      bool            `json:"seed_first_intent"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Description) == "" {
				a.Description = "Task Tanpa Nama"
			}
			if strings.TrimSpace(a.Goal) == "" {
				return actool.Errorf("goal wajib diisi"), nil
			}
			if a.TimeoutSeconds < 0 {
				a.TimeoutSeconds = 0
			}
			// Task sumber yang hanya diwarisi secara read-only: batas jumlah + setiap id harus valid/tidak duplikat/ada, aturan validasi sama dengan pembuatan task via HTTP.
			if len(a.SourceTaskIDs) > db.MaxTaskSourceCount {
				return actool.Errorf(fmt.Sprintf("maksimal %d task terkait dapat dipilih", db.MaxTaskSourceCount)), nil
			}
			sourceIDs := make([]int64, 0, len(a.SourceTaskIDs))
			seenSources := map[int64]bool{}
			for _, raw := range a.SourceTaskIDs {
				id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
				if err != nil || id <= 0 || seenSources[id] {
					return actool.Errorf("id task terkait tidak valid atau duplikat"), nil
				}
				if _, ok := s.m.Task(strconv.FormatInt(id, 10)); !ok {
					return actool.Errorf(fmt.Sprintf("task terkait #%d tidak ditemukan", id)), nil
				}
				seenSources[id] = true
				sourceIDs = append(sourceIDs, id)
			}
			// LLM profile resolution: explicit id > inherit parent's pin > active(nil).
			var pin *int64
			if id := parseProfileID(a.LLMProfileID); id > 0 {
				if _, ok := s.loadProfileConfig(id); !ok {
					return actool.Errorf(fmt.Sprintf("konfigurasi LLM #%d tidak ditemukan atau belum memiliki API Key", id)), nil
				}
				pin = &id
			} else if a.ParentRef != "" {
				if pt, ok := s.m.Task(a.ParentRef); ok {
					pin = pt.LLMProfileID
				}
			}
			var llmIDs []int64
			if pin != nil {
				llmIDs = []int64{*pin}
			}
			t, err := s.m.CreateTaskWithOptions(a.Description, a.Goal, db.TaskCreateOptions{
				SourceTaskIDs:        sourceIDs,
				LLMProfileIDs:        llmIDs,
				TimeoutSeconds:       a.TimeoutSeconds,
				PlanHeartbeatSeconds: a.PlanHeartbeatSeconds,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if a.ParentRef != "" {
				t.ParentRef = a.ParentRef
				if id, e := strconv.ParseInt(t.ID, 10, 64); e == nil {
					_ = s.m.PG().SetParentRef(id, a.ParentRef)
				}
			}
			// Proses pasca-pembuatan yang dipakai bersama, menggunakan segmen launchTask
			// yang sama dengan pembuatan task via HTTP (server.go createTask):
			// seed + dekomposisi goal yang terlihat di background (ronde ke-0/langkah LLM/goal satu per satu) + engine.Run.
			// seed_first_intent defaultnya false (mode standar: rencanakan dulu baru eksekusi); task sederhana bisa mengaktifkannya untuk langsung menjalankan satu work pengujian.
			s.launchTask(t, a.Description+" "+a.Goal, a.SeedFirstIntent)
			return actool.Text(fmt.Sprintf("task created: %s", t.ID)), nil
		})
}

func (s *Server) toolPauseTask() actool.CoreTool {
	return wrTool("pause_task", "暂停指定任务(停止其 planner/worker 循环)。",
		objSchema(map[string]any{"task_id": strParam("要暂停的任务 id")}, "task_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				TaskID string `json:"task_id"`
			}
			_ = json.Unmarshal(in, &a)
			t, ok := s.m.Task(a.TaskID)
			if !ok {
				return actool.Errorf("task tidak ditemukan: " + a.TaskID), nil
			}
			if _, err := s.applyTaskControlWithCause(t, "pause", agent.AbortPausedByOrchestrator); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("task paused: " + a.TaskID), nil
		})
}

func (s *Server) toolGetTaskGraph() actool.CoreTool {
	return roTool("get_task_graph", "读指定任务的探索图总览(同 graph_overview：资产计数/frontier/发现/覆盖等)，用 task_id 指定任务。",
		objSchema(map[string]any{"task_id": strParam("任务 id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GraphOverviewTool)
		})
}

func (s *Server) toolListTaskFindings() actool.CoreTool {
	return roTool("list_task_findings", "读指定任务的确认漏洞(含 flag/PoC；每条带 id/task_id/intent_id/vulnclass/severity/摘要/状态)，用 task_id 指定任务。",
		objSchema(map[string]any{"task_id": strParam("任务 id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListFindingsTool)
		})
}

func (s *Server) toolAddHint() actool.CoreTool {
	return wrTool("add_task_hint", "给指定任务注入战略提示(该任务的 planner 下轮生成意图时会读到)。\n"+
		"★优先批量：多条提示放进 hints 数组一次提交（返回 ids 数组，与 hints 等长同序，失败项 id=0）；单条则省略 hints 直接给顶层 text。",
		objSchema(map[string]any{
			"task_id":      strParam("任务 id"),
			"hints":        map[string]any{"type": "array", "description": "【优先用这个】提示数组，每个元素字段同顶层（text/asset_ids/traffic_refs）。", "items": objSchema(map[string]any{"text": strParam("提示内容"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": agent.HintTrafficSchema()})},
			"text":         strParam("[单条] 提示内容"),
			"traffic_refs": agent.HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "锚定的资产 id（可选，0/1/多个；该任务内的资产 id）"},
		}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).AddHintTool)
		})
}

func (s *Server) toolGetWorkerTrace() actool.CoreTool {
	return roTool("get_task_worker_trace",
		"看指定任务里某个 work(意图)的执行过程：get_task_worker_trace(task_id, intent_id) 看步骤摘要；再带 step_ids=[...] 取那几步完整内容(一次最多 5 个,多传只返回前 5 个)。",
		objSchema(map[string]any{
			"task_id":   strParam("任务 id"),
			"intent_id": map[string]any{"type": "integer", "description": "意图 id(该任务里的 work)"},
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "可选：要取完整内容的步骤 id(一次最多 5 个,多传只返回前 5 个,其余在 omitted_step_ids 里列出)"},
		}, "task_id", "intent_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GetWorkerTraceTool)
		})
}

func (s *Server) toolListWorkerTraces() actool.CoreTool {
	return roTool("list_task_worker_traces", "列出指定任务里跑过哪些 work(意图) + 各自步数，用于发现哪些 work 值得翻看(再用 get_task_worker_trace)。",
		objSchema(map[string]any{"task_id": strParam("任务 id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListWorkerTracesTool)
		})
}

func (s *Server) toolSearchWorkerTraces() actool.CoreTool {
	return roTool("search_task_worker_traces", "在指定任务里按关键字搜索所有 work 的执行过程(返回命中步骤摘要 + intent_id)。",
		objSchema(map[string]any{"task_id": strParam("任务 id"), "q": strParam("搜索关键字")}, "task_id", "q"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).SearchWorkerTracesTool)
		})
}

func (s *Server) toolGetTaskNodeDetail() actool.CoreTool {
	return roTool("get_task_node_detail",
		"读指定任务里某个探索图节点的完整内容(发现/事实/意图/目标：摘要 + 详情/证据/PoC)。id 为探索节点 id(如 report_finding 返回、或 list_task_findings 里的 id)。写漏洞报告前用它取该漏洞的完整证据。",
		objSchema(map[string]any{
			"task_id": strParam("任务 id"),
			"id":      map[string]any{"type": "integer", "description": "探索图节点 id(非资产 id)"},
		}, "task_id", "id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).NodeDetailTool)
		})
}

// toolUpdateFindingReport writes/overwrites a finding's detailed Markdown report.
// finding_id is the id report_finding returned ("finding recorded: <id>", the
// finding node id). The write (SetFindingReportByNodeID) is keyed by node_id and
// task-agnostic, so this host tool needs no task_id / exploration store.
func (s *Server) toolUpdateFindingReport() actool.CoreTool {
	return wrTool("update_finding_report",
		"为已登记的漏洞写入/更新【详细报告】(Markdown 全文,整段覆盖旧内容)。finding_id 传 report_finding 返回的那个 id(\"finding recorded: <id>\" 里的数字)。报告建议包含:漏洞概述、影响与危害、复现步骤、证据/PoC、修复建议。",
		objSchema(map[string]any{
			"finding_id":       map[string]any{"type": "integer", "description": "目标漏洞 id(report_finding 返回的 id)"},
			"report":           strParam("详细报告全文,Markdown 格式"),
			"evidence_version": map[string]any{"type": "integer", "description": "get_finding_traffic 返回的证据 version；用于防止报告覆盖新的证据变更"},
		}, "finding_id", "report"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				EvidenceVersion *int64          `json:"evidence_version"`
				FindingID       json.RawMessage `json:"finding_id"`
				Report          string          `json:"report"`
			}
			_ = json.Unmarshal(in, &a)
			nodeID := parseProfileID(a.FindingID) // memakai ulang parsing "angka atau string angka"
			if nodeID <= 0 {
				return actool.Errorf("finding_id tidak valid"), nil
			}
			n, err := s.m.pg.SetFindingReportVersionByNodeID(ctx, nodeID, a.Report, a.EvidenceVersion)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == 0 {
				return actool.Errorf(fmt.Sprintf("tidak ditemukan catatan kerentanan untuk finding_id=%d (daftarkan dulu lewat report_finding)", nodeID)), nil
			}
			return actool.Text(fmt.Sprintf("finding %d report updated (%d chars)", nodeID, len(a.Report))), nil
		})
}

// deriveTaskStatus mirrors listTasks' status derivation for the list_tasks tool.
func (s *Server) deriveTaskStatus(t *Task) string {
	lifecycle := t.lifecycleSnapshot()
	switch {
	case isTerminalStatus(lifecycle.Status):
		return lifecycle.Status
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		return "paused"
	case s.engine.ReadyFor(t) && s.engine.Started(t.ID):
		return "running"
	}
	return "created"
}

// orchestrationToolSeeds seeds the cross-task tools into the tools table so they
// are bindable per-agent (default: bound to nobody — opt-in for orchestration
// agents). First-insert only, like the traffic seeds.
func (s *Server) seedOrchestrationTools() {
	// task-op + platform tools default-bind to the built-in Auto agent (secara alami
	// dipakai untuk mengoperasikan platform). SeedTool hanya berlaku pada insert pertama;
	// baris yang sudah di-seed di database lama diikat ulang oleh seedAutoDefaultBindings.
	autoAgents, _ := json.Marshal([]string{"auto"})
	for _, t := range s.orchestrationTools() {
		schema, _ := json.Marshal(t.InputSchema())
		bindings := autoAgents
		if t.Name() == "bind_finding_traffic" {
			bindings = json.RawMessage(`["reporter"]`)
		}
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, bindings)
	}
	for _, t := range s.platformTools() {
		schema, _ := json.Marshal(t.InputSchema())
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, autoAgents)
	}
	s.refreshBuiltinToolSchemas()
	s.seedAutoDefaultBindings()
	s.seedPlannerDefaultBindings()
	s.seedPlannerListAssetsBinding()
	s.seedCompanyScopeRebind()
	s.seedWorkerReadToolsUnbind() // list_facts/list_companies/list_worker_traces diputus dari binding default worker (satu kali)
	s.seedWorkerReadbackRebind()  // memperbaiki migrasi lama yang salah hapus: mengikat ulang search_all_worker_traces/get_worker_trace/node_detail ke worker (satu kali)
	s.seedAutoReportFindingBinding()
	s.unbindGoalMetDefault()
	s.reseedGoalsPrompt()             // prompt goals ditambah langkah "ekstrak batasan operasi" -> database lama menambahkan satu versi default baru (satu kali)
	s.reseedMainAgentPrompt()         // prompt mainagent ditambah "setelah goal tercapai, add_intent bertanya balik apakah perlu dibuat goal" (satu kali)
	s.reseedPlannerPrompt()           // prompt planner: menulis ulang alasan sah "0 intent" + menambah pemeriksaan kriteria penerimaan yang terukur (satu kali)
	s.reseedWorkerPrompt()            // prompt worker: menambah ambang bukti untuk kesimpulan negatif (satu kali)
	s.seedReporterAgent()             // menyiapkan agent "Penulisan Laporan" + binding tool + trigger finding (satu kali)
	s.upgradeReporterTriggerMessage() // migrasi tambahan untuk database lama: membuat reporter mengirim balik evidence_version (satu kali)
	s.seedFindingTrafficTools()       // menambah parameter evidence opsional dan tool evidence read-only, tetap mempertahankan konfigurasi pengguna
	s.seedFindingWorkflowTools()
	// Catatan: binding tool default pentest tidak perlu migrasi, karena BuiltinToolSeeds
	// sudah menyeed list_assets/insert_assets/report_finding/list_findings/list_companies
	// bersama pentest sejak inisialisasi baru (proyek belum memiliki database lama, jadi
	// tidak perlu migrasi).
}

// refreshBuiltinToolSchemas propagates code schema/description changes on the
// orchestration + platform tools into already-seeded rows ONCE per version flag —
// SeedTool is first-insert-only, so a new param (e.g. llm_profile on spawn_task) never
// reaches an old DB otherwise. Preserves each tool's agent binding + enabled flag.
// Bump the flag whenever these tools' schemas/descriptions change in code.
func (s *Server) refreshBuiltinToolSchemas() {
	const flag = "tool_schema_refresh_v7_list_facts_paging"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	tools := append(s.orchestrationTools(), s.platformTools()...)
	for _, t := range tools {
		schema, _ := json.Marshal(t.InputSchema())
		if err := s.m.pg.RefreshToolDefaults(t.Name(), t.Description(), schema); err != nil {
			log.Printf("[tools] refresh %s schema failed: %v", t.Name(), err)
		}
	}
	// Sekaligus mengembalikan sebagian tool agent built-in ke default kode:
	//   - goal_met: deskripsi yang di-seed database lama membawa kesan menyesatkan
	//     "mengakhiri ronde perencanaan ini", membuat planner menganggapnya sebagai
	//     cara "mengakhiri ronde kosong" sehingga baru mulai berjalan pun langsung
	//     dianggap seluruh task sudah selesai.
	//   - insert_assets: menambah parameter related (menandai apakah asset relevan
	//     dengan task saat ini, menentukan apakah masuk ke coverage), karena SeedTool
	//     hanya berlaku pada insert pertama, schema yang sudah di-seed di database
	//     lama tidak akan menerima parameter baru ini.
	//   - list_facts: diubah menjadi berpaginasi, menambah parameter limit/before/q;
	//     schema kosong yang sudah di-seed database lama akan menampilkan "tidak ada
	//     parameter" di halaman manajemen tool, dan model juga tidak mendapat
	//     penjelasan parameter-parameter ini.
	refreshBuiltin := map[string]bool{"goal_met": true, "insert_assets": true, "list_facts": true}
	for _, sd := range agent.BuiltinToolSeeds() {
		if !refreshBuiltin[sd.Key] {
			continue
		}
		schema, _ := json.Marshal(sd.Schema)
		if err := s.m.pg.RefreshToolDefaults(sd.Key, sd.Desc, schema); err != nil {
			log.Printf("[tools] refresh %s desc failed: %v", sd.Key, err)
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
	log.Printf("[tools] schema tool orchestration/platform telah disegarkan ke default kode (satu kali)")
}

// unbindGoalMetDefault removes goal_met's default "planner" binding ONCE (guarded by
// a settings flag), so existing DBs match the new default of NO agent. goal_met bypasses
// per-goal prove_goal to declare the whole task done — powerful/risky and redundant with
// the prove_goal→auto-complete path — so it ships unbound; users can re-bind it per agent
// in the UI. A user's own binding to another agent is untouched (we only strip planner).
func (s *Server) unbindGoalMetDefault() {
	const flag = "goal_met_unbind_default_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.RemoveAgentFromTool("planner", "goal_met"); err != nil {
		log.Printf("[tools] gagal melepas binding goal_met dari planner: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// reseedGoalsPrompt menyegarkan prompt goal decomposer "goals" ke [default kode
// saat ini], karena teks default menambahkan langkah "ekstrak batasan operasi
// (set_constraints) sebelum memecah goal", sedangkan SeedPromptIfEmpty hanya
// berlaku pada insert pertama, sehingga version 1 yang sudah ada di database
// lama tidak akan menerima langkah ini. Di sini dipakai manajemen versi:
// [menambahkan versi baru] lalu beralih ke sana (ResetPromptToDefault), versi
// lama tetap tersimpan di riwayat, jika pengguna pernah mengustomisasi masih
// bisa diambil kembali dari riwayat versi. Dijaga oleh settings flag -> hanya
// dilakukan sekali; jika default berubah lagi nanti, flag ini dinaikkan lagi.
// Database yang benar-benar baru tidak perlu migrasi ini (SeedPromptIfEmpty
// sudah menyeed default terbaru).
func (s *Server) reseedGoalsPrompt() {
	const flag = "goals_prompt_constraint_step_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // hanya dicoba sekali, berhasil atau tidak
	a, err := s.m.pg.GetAgentByKey("goals")
	if err != nil || a == nil {
		return // database benar-benar baru belum punya baris agent, seedPrompts akan langsung menyeed default terbaru, migrasi ini tidak perlu
	}
	tmpl := agent.BuiltinPromptSeeds()["goals"]
	if tmpl == "" {
		return
	}
	// Database baru sudah di-seed default terbaru oleh seedPrompts -> versi saat ini sudah sama dengan default kode, tidak perlu menambah versi duplikat.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] gagal menyegarkan prompt goals ke default baru: %v", err)
		return
	}
	log.Printf("[prompts] prompt goals telah ditambahkan versi default baru (menambah langkah ekstrak batasan operasi, satu kali)")
}

// reseedMainAgentPrompt menyegarkan prompt mainagent ke [default kode saat
// ini], karena teks default menambahkan panduan "setelah semua goal tercapai,
// saat add_intent langsung mengirim intent, tanya balik ke manusia apakah
// perlu didaftarkan sebagai goal resmi", sedangkan SeedPromptIfEmpty hanya
// berlaku pada insert pertama, versi yang sudah ada di database lama tidak
// akan menerimanya. Dipakai manajemen versi: [menambahkan versi baru] lalu
// beralih ke sana (ResetPromptToDefault), versi lama tetap tersimpan di
// riwayat, jika pengguna pernah mengustomisasi masih bisa diambil kembali
// dari riwayat versi. Dijaga oleh settings flag -> hanya dilakukan sekali.
// Database yang benar-benar baru tidak perlu migrasi ini (SeedPromptIfEmpty
// sudah menyeed default terbaru). Strukturnya identik dengan
// reseedGoalsPrompt.
func (s *Server) reseedMainAgentPrompt() {
	const flag = "mainagent_prompt_goalless_intent_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // hanya dicoba sekali, berhasil atau tidak
	a, err := s.m.pg.GetAgentByKey("mainagent")
	if err != nil || a == nil {
		return // database benar-benar baru belum punya baris agent, seedPrompts akan langsung menyeed default terbaru, migrasi ini tidak perlu
	}
	tmpl := agent.BuiltinPromptSeeds()["mainagent"]
	if tmpl == "" {
		return
	}
	// Database baru sudah di-seed default terbaru oleh seedPrompts -> versi saat ini sudah sama dengan default kode, tidak perlu menambah versi duplikat.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] gagal menyegarkan prompt mainagent ke default baru: %v", err)
		return
	}
	log.Printf("[prompts] prompt mainagent telah ditambahkan versi default baru (menambah tanya balik buat goal setelah goal tercapai, satu kali)")
}

// reseedPlannerPrompt menyegarkan prompt planner ke [default kode saat ini],
// karena teks default telah dirapikan ulang secara struktural, dan
// "menahan diri" diturunkan jadi hanya dedup, ditambah "kedalaman lebih
// diutamakan daripada coverage", "garis keras: jika goal belum tercapai dan
// tidak ada intent yang berjalan maka harus menghasilkan sesuatu", serta
// batas atas untuk peninjauan ulang kesimpulan negatif. Setiap kali default
// berubah secara substansial, flag di bawah ini dinaikkan (saat ini v2) agar
// database lama disegarkan ulang. SeedPromptIfEmpty hanya berlaku pada insert
// pertama, versi yang sudah ada di database lama tidak akan menerimanya,
// karena itu dipakai manajemen versi: [menambahkan versi baru] lalu beralih
// ke sana (ResetPromptToDefault), versi lama tetap tersimpan di riwayat, jika
// pengguna pernah mengustomisasi masih bisa diambil kembali dari riwayat
// versi. Dijaga oleh settings flag -> hanya dilakukan sekali. Database yang
// benar-benar baru tidak perlu migrasi ini (SeedPromptIfEmpty sudah menyeed
// default terbaru). Strukturnya identik dengan reseedGoalsPrompt.
func (s *Server) reseedPlannerPrompt() {
	const flag = "planner_prompt_compact_realistic_v2"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // hanya dicoba sekali, berhasil atau tidak
	a, err := s.m.pg.GetAgentByKey("planner")
	if err != nil || a == nil {
		return // database benar-benar baru belum punya baris agent, seedPrompts akan langsung menyeed default terbaru, migrasi ini tidak perlu
	}
	tmpl := agent.BuiltinPromptSeeds()["planner"]
	if tmpl == "" {
		return
	}
	// Database baru sudah di-seed default terbaru oleh seedPrompts -> versi saat ini sudah sama dengan default kode, tidak perlu menambah versi duplikat.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] gagal menyegarkan prompt planner ke default baru: %v", err)
		return
	}
	log.Printf("[prompts] prompt planner telah ditambahkan versi default baru (perapian struktural + penurunan menahan diri jadi dedup + kedalaman diutamakan + batas atas peninjauan negatif, satu kali)")
}

// reseedWorkerPrompt menyegarkan prompt worker ke [default kode saat ini],
// karena teks default pada bagian record_fact menghapus kalimat "kesimpulan
// negatif ditulis sebagai observasi + cara baca yang bersifat tentatif", dan
// memisahkan confidence (observed/inferred) dari "apakah seluruh cara untuk
// intent ini sudah dicoba habis" (hal ini mudah menyesatkan perencana),
// sekaligus mempersempit array facts menjadi hanya sedikit pengecualian yang
// "benar-benar independen satu sama lain, tidak bisa digabung". Flag
// dinaikkan ke v3 agar database lama disegarkan ulang. SeedPromptIfEmpty
// hanya berlaku pada insert pertama, versi yang sudah ada di database lama
// tidak akan menerimanya, karena itu dipakai manajemen versi: [menambahkan
// versi baru] lalu beralih ke sana, versi lama tetap tersimpan di riwayat dan
// bisa diambil kembali. Dijaga oleh settings flag -> hanya dilakukan sekali.
// Database yang benar-benar baru tidak perlu migrasi ini. Strukturnya
// identik dengan reseedGoalsPrompt.
func (s *Server) reseedWorkerPrompt() {
	const flag = "worker_prompt_compact_v4"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // hanya dicoba sekali, berhasil atau tidak
	a, err := s.m.pg.GetAgentByKey("worker")
	if err != nil || a == nil {
		return // database benar-benar baru belum punya baris agent, seedPrompts akan langsung menyeed default terbaru, migrasi ini tidak perlu
	}
	tmpl := agent.BuiltinPromptSeeds()["worker"]
	if tmpl == "" {
		return
	}
	// Database baru sudah di-seed default terbaru oleh seedPrompts -> versi saat ini sudah sama dengan default kode, tidak perlu menambah versi duplikat.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] gagal menyegarkan prompt worker ke default baru: %v", err)
		return
	}
	log.Printf("[prompts] prompt worker telah ditambahkan versi default baru (bagian pencarian konteks dipersempit jadi list_assets/list_findings, menghapus list_facts/node_detail/asset_neighbors, satu kali)")
}

// reporterToolCallMessage harus tanpa syarat mewajibkan satu kali membaca
// get_finding_traffic sebelum menulis laporan. Tool ini bersifat read-only
// dan "tidak bergantung pada capture toggle", baik auto-binding dinyalakan
// atau tidak, evidence yang diikat manual tetap bisa dibaca. Jika di sini
// ditulis "baca hanya jika auto-binding aktif", maka pada konfigurasi default
// (nonaktif) reporter tidak akan mengirim evidence_version,
// SetFindingReportVersionByNodeID akan menulis -1 sesuai semantik legacy, dan
// detail kerentanan serta ekspor Markdown akan terus-menerus menampilkan
// "evidence telah berubah, laporan perlu diperbarui", padahal tidak ada
// entry point apa pun di UI yang bisa membersihkan status itu.
//
// PERHATIAN: reporterToolCallMessage dan reporterToolCallMessageV1 di bawah
// adalah pesan trigger yang dikirim langsung sebagai instruksi ke LLM agent
// reporter (bukan teks log/error untuk pembaca manusia), dan
// reporterToolCallMessageV1 juga dibandingkan persis (==) saat migrasi untuk
// menentukan apakah trigger lama masih memakai teks default. Sesuai batasan
// keamanan tugas ini, keduanya SENGAJA TIDAK diterjemahkan dan dibiarkan
// dalam bahasa aslinya.
const reporterToolCallMessage = "上面刚有一个漏洞被 report_finding 登记。请读取返回 JSON 的 finding_id（独立漏洞记录 ID）与 finding_node_id（探索节点 ID），" +
	"先用 get_finding_traffic(finding_id) 读取当前证据清单及其 version（空清单是正常情况，照常写报告）；" +
	"若运行指引启用自动绑定，在读取前先核实并关联本次漏洞的流量。节点详情使用 finding_node_id。" +
	"最后调用 update_finding_report(finding_id=finding_node_id, report, evidence_version=实际读取版本) 保存，" +
	"evidence_version 必须传，否则报告会被永久标记为待更新。不要混用两种编号。"

// Pesan trigger versi lama (0.3.8 dan sebelumnya). Hanya catatan yang masih identik persis dengan ini yang akan ditimpa migrasi, yang sudah diubah pengguna tetap dibiarkan apa adanya.
const reporterToolCallMessageV1 = "上面刚有一个漏洞被 report_finding 登记。请从触发上下文里取出 finding_id" +
	"（工具返回 \"finding recorded: <id>\" 里的数字）与任务 id，按你的职责撰写该漏洞的详细报告，" +
	"最后调用 update_finding_report(finding_id, report) 保存。"

// upgradeReporterTriggerMessage menyegarkan pesan trigger reporter di
// database lama yang masih memakai teks default ke versi baru.
// seedReporterAgent dijaga oleh flag reporter_agent_seed_v1 dan hanya menulis
// trigger saat agent baru dibuat, sehingga database yang diupgrade tidak
// mendapat teks baru ini, schema tool sudah dilengkapi evidence_version oleh
// seedFindingTrafficTools, tetapi tidak ada apa pun yang memberi tahu
// reporter untuk memakainya. Satu kali, dan hanya menimpa teks yang belum
// diubah.
func (s *Server) upgradeReporterTriggerMessage() {
	const flag = "reporter_trigger_evidence_version_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // hanya dicoba sekali
	triggers, err := s.m.pg.ListTriggersFor("reporter")
	if err != nil {
		log.Printf("[reporter] gagal membaca trigger: %v", err)
		return
	}
	for _, t := range triggers {
		if !t.OnToolCall || t.ToolCallMessage != reporterToolCallMessageV1 {
			continue // sudah diubah pengguna atau bukan trigger finding, tidak disentuh.
		}
		t.ToolCallMessage = reporterToolCallMessage
		if err := s.m.pg.UpdateTrigger(t); err != nil {
			log.Printf("[reporter] gagal meningkatkan pesan trigger: %v", err)
			return
		}
		log.Printf("[reporter] pesan trigger telah diperbarui untuk membaca dan mengirim balik evidence_version")
	}
}

// seedReporterAgent menyiapkan satu agent kustom "Penulisan Laporan"
// (builtin=false, dapat diedit/dihapus lewat UI): mengikat tool
// update_finding_report + tool query task, dan memasang satu trigger
// "terpicu saat report_finding dipanggil", setiap kali satu kerentanan
// terdaftar maka agent ini dibangunkan untuk menulis laporan detail. Satu
// kali (dijaga settings flag): jika pengguna menghapusnya, tidak akan
// dibuat ulang. Dependensi: tool orchestration sudah dimasukkan ke SeedTool
// di atas fungsi ini, sehingga binding ini bisa berhasil.
func (s *Server) seedReporterAgent() {
	const flag = "reporter_agent_seed_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // hanya dicoba sekali, berhasil atau tidak

	if exist, _ := s.m.pg.GetAgentByKey("reporter"); exist != nil {
		return // key sudah dipakai (pernah dibuat manual oleh pengguna), tidak ditimpa
	}
	a, err := s.m.pg.CreateAgent("reporter", "Penulisan Laporan",
		"Penulisan laporan detail kerentanan: terpicu otomatis saat kerentanan ditemukan, mengambil evidence dan proses eksekusi lalu menulis laporan Markdown dan menyimpannya kembali.")
	if err != nil {
		log.Printf("[reporter] gagal membuat agent: %v", err)
		return
	}
	if err := s.m.pg.SeedPromptIfEmpty(a.ID, agent.ReporterDefaultPrompt); err != nil {
		log.Printf("[reporter] gagal menyeed prompt: %v", err)
	}
	// Strategi eksekusi trigger: parallel + none, satu kerentanan satu laporan, beberapa finding dieksekusi paralel masing-masing menulis laporannya sendiri.
	// merge harus none: jika tidak (default all) sekumpulan finding akan digabung jadi satu run, sehingga paralel jadi tidak berarti.
	// maxParallel=5: maksimal 5 sesi laporan berjalan bersamaan, untuk menghindari terlalu banyak panggilan LLM sekaligus.
	if err := s.m.pg.SetAgentTriggerBehavior("reporter", "parallel", "none", 5); err != nil {
		log.Printf("[reporter] gagal mengatur strategi eksekusi trigger: %v", err)
	}
	// Mengikat tool yang dibutuhkan: menulis laporan + membaca evidence/proses eksekusi/kondisi.
	if err := s.m.pg.AddAgentToToolBinding("reporter", []string{
		"update_finding_report", "get_task_node_detail", "list_task_findings",
		"get_task_worker_trace", "list_task_worker_traces", "search_task_worker_traces",
		"get_task_graph",
	}); err != nil {
		log.Printf("[reporter] gagal mengikat tool: %v", err)
	}
	// Trigger: terpicu saat report_finding dipanggil (tool mengembalikan "finding recorded: <id>" membawa finding_id, id task juga ada di pesan trigger).
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey:        "reporter",
		Enabled:         true,
		OnToolCall:      true,
		ToolNames:       []string{"report_finding"},
		ToolCallMessage: reporterToolCallMessage,
	}); err != nil {
		log.Printf("[reporter] gagal membuat trigger: %v", err)
	}
	log.Printf("[reporter] agent \"Penulisan Laporan\" + trigger finding telah disiapkan")
}

// seedAutoReportFindingBinding adds "auto" to report_finding's binding ONCE so
// conversation-context agents can call it without requiring an intent_id.
func (s *Server) seedAutoReportFindingBinding() {
	const flag = "auto_report_finding_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("auto", []string{"report_finding"}); err != nil {
		log.Printf("[auto] gagal mengikat binding default report_finding: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedPlannerDefaultBindings adds "planner" to report_finding's binding ONCE
// (guarded by a settings flag), so existing DBs — whose report_finding row was
// seeded as worker-only — also let the planner record findings. Fresh DBs already
// get it via PlannerTools(); this only backfills without overriding a user unbind.
func (s *Server) seedPlannerDefaultBindings() {
	const flag = "planner_report_finding_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"report_finding"}); err != nil {
		log.Printf("[planner] gagal mengikat binding default report_finding: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedPlannerListAssetsBinding adds "planner" to list_assets's binding ONCE
// (guarded by a settings flag), so existing DBs — whose list_assets row was seeded
// as auto/pentest-only — also let the planner query the asset store by DSL. Fresh
// DBs already get it via PlannerTools(); this only backfills without overriding a
// user unbind.
func (s *Server) seedPlannerListAssetsBinding() {
	const flag = "planner_list_assets_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"list_assets"}); err != nil {
		log.Printf("[planner] gagal mengikat binding default list_assets: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedCompanyScopeRebind changes add_company_scope's default binding ONCE on
// existing DBs (guarded by a settings flag): the tool moves off worker and onto
// planner — defining a company's asset scope is a planning/main/auto concern, not
// something a worker does mid-exploration. Fresh DBs already get planner via
// PlannerTools() and lack worker via WorkerTools(); this only backfills old rows.
// One-shot + flag-guarded so a user who later re-binds worker isn't overridden.
func (s *Server) seedCompanyScopeRebind() {
	const flag = "company_scope_rebind_v1" // peralihan binding default worker->planner
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"add_company_scope"}); err != nil {
		log.Printf("[planner] gagal mengikat binding default add_company_scope: %v", err)
		return
	}
	if err := s.m.pg.RemoveAgentFromTool("worker", "add_company_scope"); err != nil {
		log.Printf("[worker] gagal melepas binding add_company_scope: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedWorkerReadToolsUnbind strips the read-context tools off worker's default
// binding ONCE on existing DBs (guarded by a settings flag): a worker executes one
// intent and writes back — reading facts/companies and listing all workers' traces is
// a planning/main concern, not the executor's. Fresh DBs already lack these via
// WorkerTools(); this only backfills old rows without overriding a user who
// deliberately re-binds worker. Each RemoveAgentFromTool is per-tool +
// membership-guarded, so planner/mainagent bindings of the same tool are untouched.
//
// NOTE: search_all_worker_traces / get_worker_trace / node_detail are intentionally NOT
// unbound — worker owns them for cross-work look-back + node drill-down (see WorkerTools).
// They used to be in this list back when worker lacked them; seedWorkerReadbackRebind
// repairs DBs whose old run stripped them.
func (s *Server) seedWorkerReadToolsUnbind() {
	const flag = "worker_readtools_unbind_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	for _, k := range []string{
		"list_facts", "list_companies", "list_worker_traces",
	} {
		if err := s.m.pg.RemoveAgentFromTool("worker", k); err != nil {
			log.Printf("[worker] gagal melepas binding %s dari worker: %v", k, err)
			return // jika gagal, flag tidak disimpan, dicoba lagi saat startup berikutnya
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedWorkerReadbackRebind re-binds the cross-work look-back / drill-down tools onto
// worker ONCE (guarded by a settings flag): an earlier seedWorkerReadToolsUnbind wrongly
// stripped search_all_worker_traces / get_worker_trace / node_detail from worker after
// they had been added to WorkerTools(), so any DB that ran that migration lost them.
// Fresh DBs already have them via WorkerTools() and this is a harmless no-op there.
// One-shot + flag-guarded so a user who later deliberately unbinds them isn't overridden.
func (s *Server) seedWorkerReadbackRebind() {
	const flag = "worker_readback_rebind_v2" // v2: menambahkan node_detail
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("worker", []string{
		"search_all_worker_traces", "get_worker_trace", "node_detail",
	}); err != nil {
		log.Printf("[worker] gagal mengikat ulang tool lihat-balik/detail: %v", err)
		return // jika gagal, flag tidak disimpan, dicoba lagi saat startup berikutnya
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedAutoDefaultBindings adds "auto" to the task-op + platform tools' bindings
// ONCE (guarded by a settings flag), so existing DBs whose tool rows were seeded
// before Auto existed still give Auto its default toolset — without re-adding it
// after a user deliberately unbinds.
func (s *Server) seedAutoDefaultBindings() {
	const flag = "auto_default_bindings_v3" // v3: mengganti nama tool asset lama, menambahkan insert_assets/add_company_scope
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	keys := make([]string, 0, len(platformToolKeys)+12)
	for _, t := range s.orchestrationTools() {
		keys = append(keys, t.Name())
	}
	keys = append(keys, platformToolKeys...)
	// Tool asset: Auto sering perlu melihat/mendaftarkan asset dan mengelola scope perusahaan saat mengoperasikan platform.
	keys = append(keys, "insert_assets", "add_company_scope", "list_assets")
	if err := s.m.pg.AddAgentToToolBinding("auto", keys); err != nil {
		log.Printf("[auto] gagal mengikat binding default: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}
