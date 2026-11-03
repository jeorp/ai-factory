package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

const (
	DocumentationInputBytes = 96 * 1024
	DocumentationMaxCalls   = 24
	DocumentationMaxReads   = 20
	DocumentationMaxFiles   = 15
	DocumentationRunTime    = 10 * time.Minute
)

func cloneDocumentation(d *domain.Documentation) *domain.Documentation {
	copy := *d
	copy.Evidence = append([]domain.DocumentationEvidence{}, d.Evidence...)
	copy.Sources = append([]domain.Source{}, d.Sources...)
	copy.Limitations = append([]string{}, d.Limitations...)
	return &copy
}

func (r *Runner) runDocumentation(ctx context.Context, in Input, emit func(Checkpoint) error) error {
	if in.Session.Documentation == nil {
		return errors.New("documentation_request_required")
	}
	doc := cloneDocumentation(in.Session.Documentation)
	if doc.Status == "ready" {
		return errors.New("nothing_to_resume")
	}
	ctx, cancel := context.WithTimeout(ctx, DocumentationRunTime)
	defer cancel()
	target := domain.Target{Repository: doc.Repository, Commit: doc.Commit, Kind: "repository"}
	if _, _, _, err := github.Tool(target, github.Read{Kind: "repository"}); err != nil {
		return err
	}
	start := time.Now()
	cp := Checkpoint{Summary: in.Session.Summary, Metrics: in.Session.Metrics, Sources: append([]domain.Source{}, in.Session.Sources...), Limitations: append([]string{}, in.Session.Limitations...)}
	cp.Metrics.LastInputTokens, cp.Metrics.LastOutputTokens, cp.Metrics.HasLastUsage = 0, 0, false
	instructions := []domain.Message{}
	for _, m := range in.Session.Messages {
		if m.Role == "instruction" {
			instructions = append(instructions, m)
		}
	}
	checkpoint := func(progress string) error {
		cp.Progress = progress
		cp.Documentation = cloneDocumentation(doc)
		cp.Metrics.DurationMS = time.Since(start).Milliseconds()
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		if int64(mem.HeapAlloc) > cp.Metrics.HeapPeakBytes {
			cp.Metrics.HeapPeakBytes = int64(mem.HeapAlloc)
		}
		return emit(cp)
	}
	addLimit := func(text string) {
		for _, old := range doc.Limitations {
			if old == text {
				return
			}
		}
		if len(doc.Limitations) < 30 {
			doc.Limitations = append(doc.Limitations, text)
		}
	}
	read := func(a github.Read) error {
		if err := checkpoint("関連コードを確認しています: " + a.Path); err != nil {
			return err
		}
		content, _, n, err := r.GitHub.Read(ctx, in.GitHubToken, target, a)
		cp.Metrics.ToolCalls++
		cp.Metrics.FetchedBytes += n
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if echoes(content, in) {
			return errors.New("credential_echo_blocked")
		}
		e := domain.DocumentationEvidence{Kind: a.Kind, Path: a.Path, Failed: err != nil}
		if err != nil {
			addLimit("取得できなかった範囲: " + a.Path)
			e.Content = "取得失敗。実装は未確認です。"
		} else {
			e.Content = domain.Clip(content, 12*1024)
			if len(e.Content) < len(content) {
				addLimit("ファイルの一部を省略: " + a.Path)
			}
			// Build references locally; a remote response cannot choose a different repository or commit.
			_, _, source, _ := github.Tool(target, a)
			if !containsSource(doc.Sources, source.URL) {
				doc.Sources = append(doc.Sources, source)
			}
			if !containsSource(cp.Sources, source.URL) {
				cp.Sources = append(cp.Sources, source)
			}
		}
		doc.Evidence = append(doc.Evidence, e)
		return checkpoint("取得したコードのつながりを整理しています")
	}
	// Discover real paths before asking the model to follow a feature across files.
	if len(doc.Evidence) == 0 {
		if err := read(github.Read{Kind: "repository", Detail: "summary"}); err != nil {
			return err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		for {
			select {
			case instruction, ok := <-in.Instructions:
				if !ok {
					in.Instructions = nil
					continue
				}
				instructions = append(instructions, instruction)
			default:
				goto drained
			}
		}
	drained:
		cp.Applied = nil
		for _, m := range instructions {
			cp.Applied = append(cp.Applied, m.ID)
		}
		files := 0
		for _, e := range doc.Evidence {
			if e.Kind == "file" {
				files++
			}
		}
		task := documentationTask{Scope: doc.Scope, Request: doc.Request, RemainingReads: max(0, DocumentationMaxReads-len(doc.Evidence)), RemainingFiles: max(0, DocumentationMaxFiles-files)}
		task.FinalAnswerOnly = task.RemainingReads == 0 || task.RemainingFiles == 0 || doc.Calls >= DocumentationMaxCalls-1
		if task.FinalAnswerOnly {
			addLimit("調査上限に達したため、取得済みの範囲で作成しました。")
		}
		input, omitted := buildDocumentationContext(doc, task, instructions)
		if omitted {
			addLimit("入力上限により、一部の取得本文を省略して整理しました。")
		}
		if len(input) > DocumentationInputBytes {
			return errors.New("ai_input_limit")
		}
		if len(input) > cp.Metrics.MaxInputBytes {
			cp.Metrics.MaxInputBytes = len(input)
		}
		if err := checkpoint("実装を理解するための資料をまとめています"); err != nil {
			return err
		}
		doc.Calls = min(doc.Calls+1, DocumentationMaxCalls)
		d, usage, err := r.next(ctx, in, input, "documentation")
		addUsage(&cp.Metrics, usage)
		if err != nil && err.Error() == "ai_token_budget_exceeded" {
			doc.Calls = max(0, doc.Calls-1)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if saveErr := checkpoint("資料作成を中断しました。保存済みの調査から再開できます"); saveErr != nil {
				return saveErr
			}
			return err
		}
		if err := checkpoint("調査の途中経過を保存しています"); err != nil {
			return err
		}
		raw, _ := json.Marshal(d)
		if echoes(string(raw), in) {
			return errors.New("credential_echo_blocked")
		}
		if d.StartImplementation != nil || d.StartReview != nil || d.Review != nil || d.Search != nil || d.Plan != nil || d.Implementation != nil || d.Verification != nil || d.Answer != "" || (d.Read != nil && d.Document != nil) {
			return errors.New("invalid_documentation_result")
		}
		if d.Read != nil {
			a := *d.Read
			if task.FinalAnswerOnly {
				return errors.New("invalid_documentation_result")
			}
			if (a.Kind != "repository" && a.Kind != "file") || (a.Kind == "repository" && a.Path != "") || a.Number != 0 || a.Validate() != nil || (a.Kind == "file" && task.RemainingFiles == 0) {
				addLimit("実行しなかった取得要求: " + domain.Clip(a.Path, 512))
				if err := checkpoint("取得対象を見直しています"); err != nil {
					return err
				}
				continue
			}
			duplicate := false
			for _, e := range doc.Evidence {
				if e.Kind == a.Kind && e.Path == a.Path {
					duplicate = true
					break
				}
			}
			if duplicate {
				cp.Metrics.CacheHits++
				addLimit("同じ取得要求の繰り返しを省略: " + a.Path)
				if err := checkpoint("取得済みの情報を使って整理しています"); err != nil {
					return err
				}
				continue
			}
			if strings.TrimSpace(d.Summary) != "" {
				doc.Notes = domain.Clip(d.Summary, 2048)
			}
			if err := read(a); err != nil {
				return err
			}
			continue
		}
		// An instruction received during generation must be reflected before publishing the result.
		select {
		case instruction, ok := <-in.Instructions:
			if ok {
				instructions = append(instructions, instruction)
				continue
			}
			in.Instructions = nil
		default:
		}
		if !validDocumentationResult(d.Document) {
			return errors.New("invalid_documentation_result")
		}
		known := map[string]bool{}
		for _, e := range doc.Evidence {
			if !e.Failed && e.Kind == "file" && e.Path != "" {
				known[e.Path] = true
			}
		}
		for i := range d.Document.Sections {
			paths := []string{}
			for _, path := range d.Document.Sections[i].Paths {
				if known[path] {
					paths = append(paths, path)
				} else {
					addLimit("参照コードを未取得: " + path)
				}
			}
			d.Document.Sections[i].Paths = paths
		}
		if len(known) == 0 {
			addLimit("ソースコード本文を取得できていないため、実装の挙動は未確認です。")
		}
		addLimit("実装の実行・テストによる動作確認は行っていません。")
		doc.Status, doc.Result, doc.CreatedAt = "ready", d.Document, time.Now().UTC()
		doc.Evidence = nil
		doc.Notes = ""
		cp.Answer = "実装理解の資料「" + d.Document.Title + "」を作成しました。資料パネルから参照コードと未確認事項を確認できます。"
		return checkpoint("資料を作成しました")
	}
}

func buildDocumentationContext(doc *domain.Documentation, task documentationTask, instructions []domain.Message) (string, bool) {
	evidence := append([]domain.DocumentationEvidence{}, doc.Evidence...)
	// Keep all path metadata; omit old excerpts explicitly when JSON escaping exceeds the input cap.
	omitted := []string{}
	payload := map[string]any{"documentationTask": task, "target": domain.Target{Repository: doc.Repository, Commit: doc.Commit}, "notes": doc.Notes, "instructions": domain.LatestInstructions(instructions, 4096), "evidence": evidence, "limitations": doc.Limitations, "omittedExcerpts": omitted}
	body, _ := json.Marshal(payload)
	for i := 0; len(body) > DocumentationInputBytes && i < len(evidence); i++ {
		if evidence[i].Content == "" {
			continue
		}
		evidence[i].Content = "（入力上限のため本文を省略。notesと未確認事項を参照）"
		omitted = append(omitted, evidence[i].Path)
		payload["omittedExcerpts"] = omitted
		body, _ = json.Marshal(payload)
	}
	return string(body), len(omitted) > 0
}

func validDocumentationResult(d *domain.DocumentationResult) bool {
	if d == nil || strings.TrimSpace(d.Title) == "" || len(d.Title) > 180 || strings.TrimSpace(d.Summary) == "" || len(d.Summary) > 1500 || len(d.Sections) < 1 || len(d.Sections) > 8 || len(d.Unverified) > 12 {
		return false
	}
	for _, s := range d.Sections {
		if strings.TrimSpace(s.Heading) == "" || len(s.Heading) > 120 || strings.TrimSpace(s.Body) == "" || len(s.Body) > 4000 || len(s.Paths) > 8 {
			return false
		}
		for _, path := range s.Paths {
			if path == "" || (github.Read{Kind: "file", Path: path}).Validate() != nil {
				return false
			}
		}
	}
	for _, u := range d.Unverified {
		if strings.TrimSpace(u) == "" || len(u) > 512 {
			return false
		}
	}
	raw, _ := json.Marshal(d)
	return len(raw) <= 24000
}

func demoDocumentation(input string) Decision {
	var body struct {
		Task     documentationTask              `json:"documentationTask"`
		Evidence []domain.DocumentationEvidence `json:"evidence"`
	}
	_ = json.Unmarshal([]byte(input), &body)
	if len(body.Evidence) < 2 && !body.Task.FinalAnswerOnly {
		return Decision{Read: &github.Read{Kind: "file", Path: "README.md", Detail: "summary"}, Summary: "固定デモのREADMEを確認します。"}
	}
	name := body.Task.Request
	if body.Task.Scope == "repository" {
		name = "リポジトリ全体"
	}
	return Decision{Document: &domain.DocumentationResult{
		Title: domain.Clip(name, 100) + "の実装ガイド（固定デモ）", Summary: "資料の構成を体験する固定デモです。実際のリポジトリの実装は確認していません。",
		Sections: []domain.DocumentationSection{
			{Heading: "目的と利用場面", Body: fmt.Sprintf("「%s」を理解するため、入口から処理とデータの流れを辿ります。以下は実装調査後の資料のサンプルです。", domain.Clip(name, 300)), Paths: []string{"README.md"}},
			{Heading: "入口と関係するコード", Body: "画面、API、外部サービスなどの役割をここに整理します。実際に取得したコードだけを参照リンクに表示します。", Paths: []string{"README.md"}},
			{Heading: "操作から結果までの処理の流れ", Body: "1. 利用者が画面で操作します。\n2. APIがリクエストを受け取ります。\n3. 必要なデータを取得・処理します。\n4. 結果を画面へ返します。\n実際の関数・呼び出し関係は未確認です。", Paths: []string{}},
			{Heading: "データと状態", Body: "入力、状態の変化、保存先をコードの根拠とともに説明します。固定デモでは実際の保存処理は確認していません。", Paths: []string{}},
			{Heading: "変更時の影響と注意点", Body: "変更が伝わる呼び出し元・API・保存形式と、確認すべきテストをまとめます。", Paths: []string{}},
			{Heading: "コードを読む順番", Body: "1. READMEで機能の目的を確認します。\n2. 画面やAPIの入口を読みます。\n3. 呼び出し先、保存処理、関連テストへ進みます。", Paths: []string{"README.md"}},
		}, Unverified: []string{"固定デモのため、対象機能の有無と実コードの動作・依存関係は未確認です。"},
	}}
}
