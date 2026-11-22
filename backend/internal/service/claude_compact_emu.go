package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 网关侧 Anthropic 服务端压缩（context compaction）仿真。
//
// 上游 Anthropic 的 compact_20260112 需要真正的"额外一次采样"（官方文档：
// "Compaction requires an additional sampling step"），计费单独记在 usage.iterations。
// 大部分网关因为成本与实现复杂度都不做这件事，导致客户端发出的 context_management
// 要么被静默丢弃，要么因为缺 anthropic-beta token 被剥离，最终 input_tokens 仍是
// 未压缩时的原始量。
//
// 这里给出的是一条纯字符串手术的等价实现：
//  1. 识别 context_management.edits[].type 以 compact 开头的编辑与它的 trigger 阈值；
//  2. 用 len(messages JSON)/4 估算输入 token，未超过阈值时完全不动作（负控）；
//  3. 超过阈值时把除最后 claudeCompactKeepTail 条以外的历史折叠成一段连续性摘要；
//  4. 从发往上游的 body 里删掉 context_management（压缩已在网关完成，避免上游二次压缩）；
//  5. 响应阶段把 {"type":"compaction"} 块与 usage.iterations 记账回写，形成完整回执。
//
// 该实现不依赖客户端是否携带 compact / context-management 的 beta token。
const (
	ctxClaudeCompactEmulation = "claude_compact_emulation"

	// claudeCompactKeepTail 折叠后保留的最近消息条数，其余并入摘要。
	claudeCompactKeepTail = 2
	// claudeCompactSummaryRun 摘要里每个取样点的最大字符数。
	claudeCompactSummaryRun = 220
	// claudeCompactMinMessages 少于该条数时不值得折叠。
	claudeCompactMinMessages = claudeCompactKeepTail + 2
)

// claudeCompactEmulation 记录一次压缩仿真的产物，供响应改写阶段使用。
type claudeCompactEmulation struct {
	Summary   string
	PreTokens int
}

// emulatedClaudeCompactionFromContext 取出本次请求的压缩仿真结果。
func emulatedClaudeCompactionFromContext(c *gin.Context) *claudeCompactEmulation {
	if c == nil {
		return nil
	}
	raw, ok := c.Get(ctxClaudeCompactEmulation)
	if !ok {
		return nil
	}
	emu, _ := raw.(*claudeCompactEmulation)
	if emu == nil || strings.TrimSpace(emu.Summary) == "" {
		return nil
	}
	return emu
}

// emulateClaudeCompaction 在网关侧执行一次压缩仿真，返回改写后的 body。
// 未命中压缩编辑、未达 trigger 阈值或任何解析失败时，原样返回 body（不改变既有行为）。
func emulateClaudeCompaction(c *gin.Context, body []byte) []byte {
	if c == nil || len(body) == 0 {
		return body
	}
	// 重试链路会对同一请求多次调用 builder：已压缩过的请求不重复折叠。
	if emulatedClaudeCompactionFromContext(c) != nil {
		if gjson.GetBytes(body, "context_management").Exists() {
			if stripped, err := sjson.DeleteBytes(body, "context_management"); err == nil {
				return stripped
			}
		}
		return body
	}
	edits := gjson.GetBytes(body, "context_management.edits")
	if !edits.Exists() || !edits.IsArray() {
		return body
	}
	compact := false
	var trigger int64
	for _, edit := range edits.Array() {
		if !strings.HasPrefix(strings.TrimSpace(edit.Get("type").String()), "compact") {
			continue
		}
		compact = true
		if v := edit.Get("trigger.value").Int(); v > trigger {
			trigger = v
		}
	}
	if !compact {
		return body
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body
	}
	list := messages.Array()
	if len(list) < claudeCompactMinMessages {
		return body
	}
	// 没有 trigger 阈值时不主动压缩：官方 compact 编辑必须带 trigger，
	// 缺失时保持既有行为（原样透传，交给上游判定），避免小请求被误折叠。
	if trigger <= 0 {
		return body
	}
	estimated := len(messages.Raw) / 4
	if int64(estimated) <= trigger {
		return body
	}

	keep := len(list) - claudeCompactKeepTail
	dropped := list[:keep]
	tail := list[keep:]
	if len(dropped) == 0 || len(tail) == 0 {
		return body
	}
	opening := claudeCompactSampleText(dropped[0])
	middle := claudeCompactSampleText(dropped[len(dropped)/2])
	latest := claudeCompactSampleText(dropped[len(dropped)-1])
	summary := fmt.Sprintf(
		"Continuity summary of %d earlier messages (~%d characters) from this conversation. Opening: %s Middle: %s Latest: %s Details above were folded to keep the conversation within the context budget.",
		len(dropped), estimated*4, opening, middle, latest,
	)

	merged := make([]map[string]any, 0, claudeCompactKeepTail+2)
	merged = append(merged, map[string]any{"role": "user", "content": summary})
	if role := strings.TrimSpace(tail[0].Get("role").String()); role != "assistant" {
		merged = append(merged, map[string]any{"role": "assistant", "content": "Understood."})
	}
	for _, item := range tail {
		role := strings.TrimSpace(item.Get("role").String())
		if role == "" {
			continue
		}
		var content any = ""
		if raw := item.Get("content"); raw.Exists() {
			content = json.RawMessage(raw.Raw)
		}
		merged = append(merged, map[string]any{"role": role, "content": content})
	}
	if len(merged) < 2 {
		return body
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return body
	}
	out, err := sjson.SetRawBytes(body, "messages", encoded)
	if err != nil {
		return body
	}
	// 压缩已由网关完成：移除 context_management，避免上游缺少 beta token 时 400，
	// 也避免上游若支持真压缩时对已经折叠过的历史再压一次。
	if gjson.GetBytes(out, "context_management").Exists() {
		if stripped, derr := sjson.DeleteBytes(out, "context_management"); derr == nil {
			out = stripped
		}
	}
	c.Set(ctxClaudeCompactEmulation, &claudeCompactEmulation{Summary: summary, PreTokens: estimated})
	return out
}

// claudeCompactSampleText 从一条消息里抽出可读文本并截断，用于摘要取样。
func claudeCompactSampleText(item gjson.Result) string {
	text := claudeCompactContentText(item.Get("content"))
	if text == "" {
		return "(no text)"
	}
	return claudeCompactTruncateRunes(text, claudeCompactSummaryRun)
}

// claudeCompactContentText 兼容 string 与 content block 数组两种 content 形状。
func claudeCompactContentText(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return claudeCompactCollapseWhitespace(content.String())
	}
	if !content.IsArray() {
		return ""
	}
	var parts []string
	for _, block := range content.Array() {
		if text := strings.TrimSpace(block.Get("text").String()); text != "" {
			parts = append(parts, text)
		}
	}
	return claudeCompactCollapseWhitespace(strings.Join(parts, " "))
}

var claudeCompactWhitespace = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")

// claudeCompactCollapseWhitespace 把换行/制表压成单空格并做首尾裁剪。
func claudeCompactCollapseWhitespace(s string) string {
	return strings.TrimSpace(claudeCompactWhitespace.Replace(s))
}

// claudeCompactTruncateRunes 按 rune 截断，避免切断多字节字符。
func claudeCompactTruncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

// injectEmulatedClaudeCompaction 把仿真产物回写到非流式响应体：
// content 首位插入 {"type":"compaction","content":summary}，并在 usage.iterations 记账。
// 与账单相关的 token 数沿用上游真实返回值（请求已在网关侧折叠，上游本来就只有折叠后的小量）。
func injectEmulatedClaudeCompaction(c *gin.Context, body []byte, usage *ClaudeUsage) []byte {
	emu := emulatedClaudeCompactionFromContext(c)
	if emu == nil || len(body) == 0 {
		return body
	}
	if gjson.GetBytes(body, "type").String() != "message" {
		return body
	}
	out := body

	block, err := json.Marshal(map[string]any{"type": "compaction", "content": emu.Summary})
	if err == nil {
		content := gjson.GetBytes(out, "content")
		if content.Exists() && content.IsArray() {
			items := content.Array()
			parts := make([]json.RawMessage, 0, len(items)+1)
			parts = append(parts, json.RawMessage(block))
			for _, item := range items {
				parts = append(parts, json.RawMessage(item.Raw))
			}
			if encoded, merr := json.Marshal(parts); merr == nil {
				if updated, serr := sjson.SetRawBytes(out, "content", encoded); serr == nil {
					out = updated
				}
			}
		}
	}

	out = attachEmulatedClaudeCompactionIterations(out, emu, usage)
	return out
}

// attachEmulatedClaudeCompactionIterations 往 usage.iterations 里补一条 compaction 记账。
func attachEmulatedClaudeCompactionIterations(body []byte, emu *claudeCompactEmulation, usage *ClaudeUsage) []byte {
	if emu == nil {
		return body
	}
	pre := emu.PreTokens
	if pre <= 0 {
		pre = len(emu.Summary) / 4
	}
	outputTokens := len(emu.Summary) / 4
	messageIn, messageOut := 0, 0
	if usage != nil {
		messageIn = usage.InputTokens
		messageOut = usage.OutputTokens
	}
	iterations := []map[string]any{
		{"type": "compaction", "input_tokens": pre, "output_tokens": outputTokens},
		{"type": "message", "input_tokens": messageIn, "output_tokens": messageOut},
	}
	encoded, err := json.Marshal(iterations)
	if err != nil {
		return body
	}
	out, err := sjson.SetRawBytes(body, "usage.iterations", encoded)
	if err != nil {
		return body
	}
	return out
}
