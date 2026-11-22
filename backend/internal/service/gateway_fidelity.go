package service

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// anthropicBetaTokenContextManagementPrefix 官方 context_management 能力对应的 beta 前缀。
// 带该前缀（例如 context-management-2025-06-27）才允许 body 里出现 context_management。
const anthropicBetaTokenContextManagementPrefix = "context-management-"

// anthropicBetaTokenCompactionPrefix 压缩能力对应的 beta 前缀。
// C3 场景客户端只发 compact-2026-01-12，此时 context_management 同样必须放行。
const anthropicBetaTokenCompactionPrefix = "compact-"

// anthropicBetaTokenThinkingBindingPrefix thinking 绑定控制能力对应的 beta 前缀。
// 只有显式携带该 beta 时，客户端才声明自己能处理签名校验失败，网关才允许做
// 「剥离历史 thinking 后重试」这类会改写请求体的自动修复。
const anthropicBetaTokenThinkingBindingPrefix = "thinking-binding-controls"

// gatewayFidelityEntranceMessage 与官方一致的缺 beta 报错文案。
const gatewayFidelityEntranceMessage = "context_management requires the context-management-2025-06-27 beta header"

// anthropicBetaHeaderHasAnyPrefix 判断 anthropic-beta 头里是否有任一 token 命中给定前缀。
func anthropicBetaHeaderHasAnyPrefix(header string, prefixes ...string) bool {
	if strings.TrimSpace(header) == "" {
		return false
	}
	for _, raw := range strings.Split(header, ",") {
		token := strings.ToLower(strings.TrimSpace(raw))
		if token == "" {
			continue
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(token, prefix) {
				return true
			}
		}
	}
	return false
}

// applyGatewayFidelityRequestRewrites 在转发前对 Anthropic 请求体做两处保真度修正，
// 使其行为与官方线路一致：
//  1. 严格入口：body 带 context_management 但 anthropic-beta 既无 context-management-*
//     也无 compact-* 时返回官方 400（否则官方会直接拒，网关却静默接受，被判为不可接入）。
//  2. 工具请求 max_tokens 抬底：带非空 tools 且 max_tokens 低于下限时抬到下限，
//     避免思考预算吃满后思考块占满输出、工具参数回填为空。
//
// 返回 handled=true 表示已经写回客户端响应（调用方应立即返回）。
func (s *GatewayService) applyGatewayFidelityRequestRewrites(ctx context.Context, c *gin.Context, account *Account, parsed *ParsedRequest) (handled bool) {
	if s == nil || c == nil || parsed == nil || account == nil {
		return false
	}
	if account.Platform != PlatformAnthropic {
		return false
	}
	flags := s.settingService.ResolveGatewayFidelity(ctx, account.ID, parsed.GroupID)
	if !flags.entranceStrict && !flags.toolMaxTokensFloor {
		return false
	}

	body := parsed.Body.Bytes()
	changed := false

	if flags.entranceStrict {
		cm := gjson.GetBytes(body, "context_management")
		if cm.Exists() && cm.IsObject() {
			betaHeader := ""
			if c.Request != nil {
				betaHeader = c.Request.Header.Get("anthropic-beta")
			}
			if !anthropicBetaHeaderHasAnyPrefix(betaHeader,
				anthropicBetaTokenContextManagementPrefix,
				anthropicBetaTokenCompactionPrefix,
			) {
				c.JSON(http.StatusBadRequest, gin.H{
					"type": "error",
					"error": gin.H{
						"type":    "invalid_request_error",
						"message": gatewayFidelityEntranceMessage,
					},
				})
				c.Abort()
				return true
			}
		}
	}

	if flags.toolMaxTokensFloor && flags.toolMaxTokensValue > 0 {
		if updated, ok := gatewayFidelityApplyToolMaxTokensFloor(body, flags.toolMaxTokensValue); ok {
			body = updated
			changed = true
		}
	}

	if changed {
		if err := parsed.ReplaceBody(body); err != nil {
			return false
		}
	}
	return false
}

// gatewayFidelityRequestWantsThinkingBinding 判断本次请求是否显式声明了
// thinking-binding-controls 能力。未声明时网关不应把上游的签名 400 洗成 200。
func gatewayFidelityRequestWantsThinkingBinding(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	return anthropicBetaHeaderHasAnyPrefix(
		c.Request.Header.Get("anthropic-beta"),
		anthropicBetaTokenThinkingBindingPrefix,
	)
}

// gatewayFidelityBodyHasContextManagement 供测试与调用方判断请求体是否带上下文管理指令。
func gatewayFidelityBodyHasContextManagement(body []byte) bool {
	if len(bytes.TrimSpace(body)) == 0 {
		return false
	}
	cm := gjson.GetBytes(body, "context_management")
	return cm.Exists() && cm.IsObject()
}

// gatewayFidelityAllowSignatureRepair 判断本次请求是否允许对上游 thinking 签名 400 做
// 「剥离历史 thinking 后重试」的自动修复。
//
// 官方上游确实会校验 thinking 签名的绑定关系：客户端带 thinking-binding-controls
// beta 时才能自行处理签名失效；不带该 beta 的请求，上游返回 400 才是正确行为。
// 网关若无条件重试，会把「应当被拒」的请求洗成 200（探测脚本 C5 因此判为不通过）。
func (s *GatewayService) gatewayFidelityAllowSignatureRepair(ctx context.Context, c *gin.Context, account *Account, parsed *ParsedRequest) bool {
	if s == nil || s.settingService == nil {
		return true
	}
	var groupID *int64
	if parsed != nil {
		groupID = parsed.GroupID
	}
	flags := s.settingService.ResolveGatewayFidelity(ctx, accountIDOf(account), groupID)
	if !flags.thinkingBindingGate {
		return true
	}
	return gatewayFidelityRequestWantsThinkingBinding(c)
}

func accountIDOf(account *Account) int64 {
	if account == nil {
		return 0
	}
	return account.ID
}

// gatewayFidelityApplyToolMaxTokensFloor 在 body 带非空 tools 且 max_tokens 低于下限时
// 把 max_tokens 抬到下限，返回新的 body 与是否发生变化。
// max_tokens 只是输出预算上限，抬高不会额外计费；这样才能保证思考块吃满预算时
// 仍然留有足够空间写工具调用参数。
func gatewayFidelityApplyToolMaxTokensFloor(body []byte, floor int64) ([]byte, bool) {
	if floor <= 0 || len(body) == 0 {
		return body, false
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() || len(tools.Array()) == 0 {
		return body, false
	}
	maxTokens := gjson.GetBytes(body, "max_tokens")
	if !maxTokens.Exists() || maxTokens.Type != gjson.Number {
		return body, false
	}
	if maxTokens.Int() >= floor {
		return body, false
	}
	updated, err := sjson.SetBytes(body, "max_tokens", floor)
	if err != nil {
		return body, false
	}
	return updated, true
}
