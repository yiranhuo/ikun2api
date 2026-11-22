//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ============================================================================
// 网关保真度修复（gateway fidelity）单元测试
// ============================================================================
//
// 这组开关收敛三处「直连官方线路能过、经过本网关后被改写」的差异：
//  1. 严格入口：body 带 context_management 却没有 context-management-* /
//     compact-* beta 时应与官方一样返回 400；
//  2. 工具请求 max_tokens 抬底：tools 非空且 max_tokens 低于下限时抬到下限；
//  3. thinking 签名 400 的自动剥离重试，只有显式携带
//     thinking-binding-controls beta 时才允许。
//
// 探测脚本对应项：C1 / B2 / C5。

func resetGatewayFidelityCacheForTest() {
	gatewayFidelityCache.Store((*cachedGatewayFidelitySettings)(nil))
}

func newGatewayFidelitySettingService(t *testing.T, payload string) *SettingService {
	t.Helper()
	resetGatewayFidelityCacheForTest()
	t.Cleanup(resetGatewayFidelityCacheForTest)
	return &SettingService{
		settingRepo: &settingRepoStub{values: map[string]string{
			SettingKeyGatewayFidelitySettings: payload,
		}},
	}
}

func TestAnthropicBetaHeaderHasAnyPrefix(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"empty", "", false},
		{"single context-management", "context-management-2025-06-27", true},
		{"single compact", "compact-2026-01-12", true},
		{"compact plus others", "oauth-2025-04-20, compact-2026-01-12", true},
		{"upper case", "Context-Management-2025-06-27", true},
		{"thinking binding", "thinking-binding-controls-2026-08-01", true},
		{"unrelated only", "fine-grained-tool-streaming-2025-05-14", false},
		{"prefix but different family", "contextwindow-2025-01-01", false},
		{"leading whitespace", "  compact-2026-01-12  ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := anthropicBetaHeaderHasAnyPrefix(tc.header,
				anthropicBetaTokenContextManagementPrefix,
				anthropicBetaTokenCompactionPrefix,
				anthropicBetaTokenThinkingBindingPrefix,
			)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestGatewayFidelityApplyToolMaxTokensFloor(t *testing.T) {
	valid := func(t *testing.T, raw string) []byte {
		t.Helper()
		require.True(t, json.Valid([]byte(raw)), raw)
		return []byte(raw)
	}

	t.Run("raises when tools present and max_tokens below floor", func(t *testing.T) {
		in := valid(t, `{"model":"claude-opus-5-5","max_tokens":2048,"tools":[{"name":"t","input_schema":{"type":"object"}}],"messages":[]}`)
		out, changed := gatewayFidelityApplyToolMaxTokensFloor(in, 4096)
		require.True(t, changed)
		require.Equal(t, int64(4096), gjson.GetBytes(out, "max_tokens").Int())
		require.Equal(t, "claude-opus-5-5", gjson.GetBytes(out, "model").String())
		require.Len(t, gjson.GetBytes(out, "tools").Array(), 1)
	})

	t.Run("keeps max_tokens when already at or above floor", func(t *testing.T) {
		in := valid(t, `{"max_tokens":4096,"tools":[{"name":"t"}],"messages":[]}`)
		out, changed := gatewayFidelityApplyToolMaxTokensFloor(in, 4096)
		require.False(t, changed)
		require.Equal(t, in, out)

		in2 := valid(t, `{"max_tokens":8192,"tools":[{"name":"t"}],"messages":[]}`)
		_, changed2 := gatewayFidelityApplyToolMaxTokensFloor(in2, 4096)
		require.False(t, changed2)
	})

	t.Run("ignores requests without tools", func(t *testing.T) {
		in := valid(t, `{"max_tokens":64,"messages":[]}`)
		_, changed := gatewayFidelityApplyToolMaxTokensFloor(in, 4096)
		require.False(t, changed)
	})

	t.Run("ignores empty tools array", func(t *testing.T) {
		in := valid(t, `{"max_tokens":64,"tools":[],"messages":[]}`)
		_, changed := gatewayFidelityApplyToolMaxTokensFloor(in, 4096)
		require.False(t, changed)
	})

	t.Run("ignores missing max_tokens", func(t *testing.T) {
		in := valid(t, `{"tools":[{"name":"t"}],"messages":[]}`)
		_, changed := gatewayFidelityApplyToolMaxTokensFloor(in, 4096)
		require.False(t, changed)
	})

	t.Run("ignores non positive floor", func(t *testing.T) {
		in := valid(t, `{"max_tokens":64,"tools":[{"name":"t"}],"messages":[]}`)
		_, changed := gatewayFidelityApplyToolMaxTokensFloor(in, 0)
		require.False(t, changed)
	})
}

func TestGatewayFidelityApplyToolEffortCap(t *testing.T) {
	t.Run("adds low effort when output_config absent", func(t *testing.T) {
		out, changed := gatewayFidelityApplyToolEffortCap(
			[]byte(`{"max_tokens":4096,"tools":[{"name":"t"}]}`), 4096)
		require.True(t, changed)
		require.Equal(t, "low", gjson.GetBytes(out, "output_config.effort").String())
	})

	t.Run("keeps client effort untouched", func(t *testing.T) {
		in := []byte(`{"max_tokens":4096,"tools":[{"name":"t"}],"output_config":{"effort":"high"}}`)
		out, changed := gatewayFidelityApplyToolEffortCap(in, 4096)
		require.False(t, changed)
		require.Equal(t, "high", gjson.GetBytes(out, "output_config.effort").String())
	})

	t.Run("preserves other output_config fields", func(t *testing.T) {
		in := []byte(`{"max_tokens":2048,"tools":[{"name":"t"}],"output_config":{"format":{"type":"json_schema"}}}`)
		out, changed := gatewayFidelityApplyToolEffortCap(in, 4096)
		require.True(t, changed)
		require.Equal(t, "low", gjson.GetBytes(out, "output_config.effort").String())
		require.Equal(t, "json_schema", gjson.GetBytes(out, "output_config.format.type").String())
	})

	t.Run("skips requests without tools", func(t *testing.T) {
		_, changed := gatewayFidelityApplyToolEffortCap([]byte(`{"max_tokens":2048}`), 4096)
		require.False(t, changed)
	})

	t.Run("skips requests above floor", func(t *testing.T) {
		_, changed := gatewayFidelityApplyToolEffortCap(
			[]byte(`{"max_tokens":8192,"tools":[{"name":"t"}]}`), 4096)
		require.False(t, changed)
	})

	t.Run("ignores empty body", func(t *testing.T) {
		_, changed := gatewayFidelityApplyToolEffortCap(nil, 4096)
		require.False(t, changed)
	})
}

func TestGatewayFidelityBodyHasContextManagement(t *testing.T) {
	require.True(t, gatewayFidelityBodyHasContextManagement(
		[]byte(`{"context_management":{"edits":[]}}`)))
	require.False(t, gatewayFidelityBodyHasContextManagement(
		[]byte(`{"messages":[]}`)))
	require.False(t, gatewayFidelityBodyHasContextManagement(nil))
}

func TestResolveGatewayFidelityExclusions(t *testing.T) {
	svc := newGatewayFidelitySettingService(t, `{
		"enabled": true,
		"entrance_strict": true,
		"tool_max_tokens_floor": true,
		"tool_max_tokens_floor_value": 4096,
		"thinking_binding_gated": true,
		"excluded_groups": [7],
		"excluded_accounts": [1031]
	}`)
	ctx := context.Background()

	t.Run("all enabled for unlisted target", func(t *testing.T) {
		group := int64(35)
		flags := svc.ResolveGatewayFidelity(ctx, 1032, &group)
		require.True(t, flags.entranceStrict)
		require.True(t, flags.toolMaxTokensFloor)
		require.Equal(t, int64(4096), flags.toolMaxTokensValue)
		require.True(t, flags.thinkingBindingGate)
	})

	t.Run("account exclusion wins", func(t *testing.T) {
		group := int64(35)
		flags := svc.ResolveGatewayFidelity(ctx, 1031, &group)
		require.False(t, flags.entranceStrict)
		require.False(t, flags.toolMaxTokensFloor)
		require.False(t, flags.thinkingBindingGate)
	})

	t.Run("group exclusion wins", func(t *testing.T) {
		group := int64(7)
		flags := svc.ResolveGatewayFidelity(ctx, 9999, &group)
		require.False(t, flags.entranceStrict)
		require.False(t, flags.thinkingBindingGate)
	})

	t.Run("nil group id does not panic", func(t *testing.T) {
		flags := svc.ResolveGatewayFidelity(ctx, 9999, nil)
		require.True(t, flags.entranceStrict)
	})
}

func TestResolveGatewayFidelityMasterSwitchOff(t *testing.T) {
	svc := newGatewayFidelitySettingService(t, `{
		"enabled": false,
		"entrance_strict": true,
		"tool_max_tokens_floor": true,
		"tool_max_tokens_floor_value": 4096,
		"thinking_binding_gated": true
	}`)
	group := int64(35)
	flags := svc.ResolveGatewayFidelity(context.Background(), 1032, &group)
	require.False(t, flags.entranceStrict)
	require.False(t, flags.toolMaxTokensFloor)
	require.False(t, flags.thinkingBindingGate)
}

func TestGetGatewayFidelitySettingsBackfillsMissingFields(t *testing.T) {
	// 只有两个字段的老 JSON：其余必须回填默认值，显式写入的 false 必须保留。
	svc := newGatewayFidelitySettingService(t,
		`{"enabled":true,"entrance_strict":false}`)

	settings, err := svc.GetGatewayFidelitySettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.False(t, settings.EntranceStrict, "explicit false must be preserved")
	require.True(t, settings.ToolMaxTokensFloor)
	require.Equal(t, int64(4096), settings.ToolMaxTokensFloorValue)
	require.True(t, settings.ThinkingBindingGated)
}

func TestGetGatewayFidelitySettingsDefaultsWhenAbsent(t *testing.T) {
	resetGatewayFidelityCacheForTest()
	t.Cleanup(resetGatewayFidelityCacheForTest)
	svc := &SettingService{settingRepo: &settingRepoStub{values: map[string]string{}}}

	settings, err := svc.GetGatewayFidelitySettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, int64(4096), settings.ToolMaxTokensFloorValue)
}

func TestDefaultGatewayFidelitySettings(t *testing.T) {
	defaults := DefaultGatewayFidelitySettings()
	require.True(t, defaults.Enabled)
	require.True(t, defaults.EntranceStrict)
	require.True(t, defaults.ToolMaxTokensFloor)
	require.Equal(t, int64(4096), defaults.ToolMaxTokensFloorValue)
	require.True(t, defaults.ThinkingBindingGated)
	require.NotNil(t, defaults.ExcludedGroups)
	require.NotNil(t, defaults.ExcludedAccounts)
}
