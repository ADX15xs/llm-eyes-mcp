// Command llm-eyes-probe measures how well the vision model localizes regions
// on a synthetic screenshot with exact ground truth, across thinking-budget
// conditions. It is the Go replacement for the retired
// scripts/probe_grounding_accuracy.mjs — the synthetic case is now drawn with
// image/draw, so no browser is involved.
//
// Experimental harness: talks directly to an OpenAI-compatible upstream (by
// default the NVIDIA NIM endpoint that hosts the same model as the gateway,
// bypassing the MCP server). Needs an upstream API key; not wired into the
// Makefile and never part of `make test`.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/xiaos/llm-eyes-mcp/internal/envfile"
	"github.com/xiaos/llm-eyes-mcp/internal/probe"
)

const (
	defaultUpstream = "https://integrate.api.nvidia.com/v1/chat/completions"
	// 与网关 vision-glm 部署同源的模型。
	defaultModel = "z-ai/glm-5.3-flash"
)

type condition struct {
	name     string
	thinking string
}

var (
	condOff   = condition{"thinking off (mtt=0)", "0"}
	condLimit = condition{"thinking 512", "512"}
	condFull  = condition{"thinking full", ""}
	condAll   = []condition{condOff, condLimit, condFull}
)

var conditions = map[string][]condition{
	"all":   condAll,
	"off":   {condOff},
	"limit": {condLimit},
	"full":  {condFull},
}

// row is one sample in the console table and the --out JSON.
type row struct {
	Name             string  `json:"name"`
	Rep              int     `json:"rep"`
	MS               int64   `json:"ms,omitempty"`
	OK               bool    `json:"ok"`
	Error            string  `json:"error,omitempty"`
	Status           int     `json:"status,omitempty"`
	Finish           string  `json:"finish,omitempty"`
	Returned         int     `json:"returned,omitempty"`
	IoUMean          float64 `json:"iouMean,omitempty"`
	IoUOk            int     `json:"iouOk,omitempty"`
	TextOk           int     `json:"textOk,omitempty"`
	PixelSpace       int     `json:"pixelSpace,omitempty"`
	ReasoningTokens  *int    `json:"reasoningTokens,omitempty"`
	CompletionTokens *int    `json:"completionTokens,omitempty"`
	Content          string  `json:"content,omitempty"`
	JSONOK           bool    `json:"-"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "llm-eyes-probe:", err)
		os.Exit(1)
	}
}

func run() error {
	reps := flag.Int("reps", 2, "calls per thinking condition")
	which := flag.String("thinking", "all", "conditions to run: all|off|limit|full")
	baseURL := flag.String("base-url", defaultUpstream, "OpenAI-compatible chat/completions URL")
	model := flag.String("model", defaultModel, "upstream model name")
	apiKey := flag.String("api-key", "", "upstream API key (default: --key-env variable, then .env files)")
	keyEnv := flag.String("key-env", "NVIDIA_API_KEY", "environment variable holding the API key")
	timeout := flag.Duration("timeout", 4*time.Minute, "per-call deadline")
	out := flag.String("out", "", "optional path for the full JSON results")
	flag.Parse()

	key := *apiKey
	if key == "" {
		key = os.Getenv(*keyEnv)
	}
	if key == "" {
		// 上游 key 在 uoma 仓库根的 .env 里（llm-eyes 自己的 .env 只有网关的 UOMA_API_KEY）
		key = envfile.Load(".env", "../uoma/.env")[*keyEnv]
	}
	if key == "" {
		return fmt.Errorf("no API key: pass --api-key, set $%s, or add it to .env / ../uoma/.env", *keyEnv)
	}
	conds, ok := conditions[*which]
	if !ok {
		return fmt.Errorf("unknown --thinking %q (want all|off|limit|full)", *which)
	}

	testCase, err := probe.Render()
	if err != nil {
		return err
	}
	fmt.Printf("synthetic case: %dx%d, %d targets, ground truth from draw parameters\n\n",
		probe.W, probe.H, len(probe.Targets))

	ctx := context.Background()
	var rows []row
	for _, cond := range conds {
		for rep := 1; rep <= *reps; rep++ {
			r := probe.Call(ctx, probe.CallOptions{
				BaseURL:  *baseURL,
				APIKey:   key,
				Model:    *model,
				PNG:      testCase.PNG,
				Thinking: cond.thinking,
				Timeout:  *timeout,
			})
			result := newRow(cond, rep, testCase.GT, r)
			rows = append(rows, result)
			printRow(result)
		}
	}
	printSummary(conds, rows)

	if *out != "" {
		dump := struct {
			GT      map[string][4]float64 `json:"gt"`
			Results []row                 `json:"results"`
		}{testCase.GT, rows}
		data, err := json.MarshalIndent(dump, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*out, data, 0o644); err != nil {
			return err
		}
		fmt.Printf("\nresults written to %s\n", *out)
	}
	return nil
}

func newRow(cond condition, rep int, gt map[string][4]float64, r *probe.CallResult) row {
	row := row{
		Name:             cond.name,
		Rep:              rep,
		MS:               r.Milliseconds,
		Status:           r.Status,
		Finish:           r.Finish,
		ReasoningTokens:  r.ReasoningTokens,
		CompletionTokens: r.CompletionTokens,
	}
	if r.Error != "" {
		row.Error = r.Error
		return row
	}
	row.OK = true
	row.JSONOK = r.Parsed != nil
	row.Content = r.Content
	s := probe.Score(gt, r.Parsed)
	row.Returned = s.Returned
	row.IoUMean = s.IoUMean
	row.IoUOk = s.IoUOk
	row.TextOk = s.TextOk
	row.PixelSpace = s.PixelSpace
	return row
}

func printRow(row row) {
	targets := len(probe.Targets)
	if !row.OK {
		fmt.Printf("%-22s rep%d %6dms  ERR(%d) %s\n", row.Name, row.Rep, row.MS, row.Status, row.Error)
		return
	}
	jsonState := "BAD"
	if row.JSONOK {
		jsonState = "ok"
	}
	fmt.Printf("%-22s rep%d %6dms  rt=%s out=%s json=%s n=%d/%d iou_mean=%.3f iou>=%.1f=%d/%d text=%d/%d",
		row.Name, row.Rep, row.MS, intStr(row.ReasoningTokens), intStr(row.CompletionTokens), jsonState,
		row.Returned, targets, row.IoUMean, probe.IoUPass, row.IoUOk, targets, row.TextOk, targets)
	if row.PixelSpace > 0 {
		fmt.Printf(" pixelSpace=%d/%d", row.PixelSpace, targets)
	}
	if row.IoUOk == targets {
		fmt.Print("  ✓")
	}
	fmt.Println()
}

func printSummary(conds []condition, rows []row) {
	fmt.Println("\n=== 汇总 ===")
	for _, cond := range conds {
		var n int
		var ms, iouMean, iouOK, textOK float64
		for _, r := range rows {
			if r.Name != cond.name || !r.OK {
				continue
			}
			n++
			ms += float64(r.MS)
			iouMean += r.IoUMean
			iouOK += float64(r.IoUOk)
			textOK += float64(r.TextOk)
		}
		if n == 0 {
			fmt.Printf("%-22s 无有效样本\n", cond.name)
			continue
		}
		f := float64(n)
		fmt.Printf("%-22s n=%d 延迟均值=%.0fms iou_mean=%.2f iou>=%.1f=%.2f/%d text=%.2f/%d\n",
			cond.name, n, ms/f, iouMean/f, probe.IoUPass, iouOK/f, len(probe.Targets), textOK/f, len(probe.Targets))
	}
}

func intStr(p *int) string {
	if p == nil {
		return "null"
	}
	return strconv.Itoa(*p)
}
