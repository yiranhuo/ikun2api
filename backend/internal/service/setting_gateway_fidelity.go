package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// cachedGatewayFidelitySettings 缓存网关保真度修复配置（进程内缓存，30s TTL）。
// 这份配置在每次 Anthropic 转发前都要读，必须走零锁快路径。
type cachedGatewayFidelitySettings struct {
	settings  GatewayFidelitySettings
	expiresAt int64 // unix nano
}

var gatewayFidelityCache atomic.Value // *cachedGatewayFidelitySettings
var gatewayFidelitySF singleflight.Group

const gatewayFidelityCacheTTL = 30 * time.Second
const gatewayFidelityErrorTTL = 5 * time.Second
const gatewayFidelityDBTimeout = 5 * time.Second

// GetGatewayFidelitySettings 获取网关保真度修复配置
func (s *SettingService) GetGatewayFidelitySettings(ctx context.Context) (*GatewayFidelitySettings, error) {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyGatewayFidelitySettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultGatewayFidelitySettings(), nil
		}
		return nil, fmt.Errorf("get gateway fidelity settings: %w", err)
	}
	if value == "" {
		return DefaultGatewayFidelitySettings(), nil
	}

	var settings GatewayFidelitySettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return DefaultGatewayFidelitySettings(), nil
	}
	// 老版本落库的 JSON 可能缺少后加的子开关。缺字段回填默认值，但保留管理员
	// 显式写入的 false（与 rectifier_settings 的处理方式保持一致）。
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &fields); err == nil {
		defaults := DefaultGatewayFidelitySettings()
		if _, ok := fields["enabled"]; !ok {
			settings.Enabled = defaults.Enabled
		}
		if _, ok := fields["entrance_strict"]; !ok {
			settings.EntranceStrict = defaults.EntranceStrict
		}
		if _, ok := fields["tool_max_tokens_floor"]; !ok {
			settings.ToolMaxTokensFloor = defaults.ToolMaxTokensFloor
		}
		if _, ok := fields["tool_max_tokens_floor_value"]; !ok || settings.ToolMaxTokensFloorValue <= 0 {
			settings.ToolMaxTokensFloorValue = defaults.ToolMaxTokensFloorValue
		}
		if _, ok := fields["thinking_binding_gated"]; !ok {
			settings.ThinkingBindingGated = defaults.ThinkingBindingGated
		}
	}

	return &settings, nil
}

// SetGatewayFidelitySettings 保存网关保真度修复配置
func (s *SettingService) SetGatewayFidelitySettings(ctx context.Context, settings *GatewayFidelitySettings) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}
	if settings.ToolMaxTokensFloorValue <= 0 {
		settings.ToolMaxTokensFloorValue = DefaultGatewayFidelitySettings().ToolMaxTokensFloorValue
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal gateway fidelity settings: %w", err)
	}

	if err := s.settingRepo.Set(ctx, SettingKeyGatewayFidelitySettings, string(data)); err != nil {
		return err
	}
	// 面板保存后立刻生效，不能等 30s TTL 自然过期。
	gatewayFidelityCache.Store((*cachedGatewayFidelitySettings)(nil))
	return nil
}

// getGatewayFidelitySettingsCached 读取带进程内缓存的配置。
// DB 错误时回落到默认值并短暂缓存，避免故障期间每个请求都打库。
func (s *SettingService) getGatewayFidelitySettingsCached(ctx context.Context) *GatewayFidelitySettings {
	if cached, ok := gatewayFidelityCache.Load().(*cachedGatewayFidelitySettings); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			settings := cached.settings
			return &settings
		}
	}

	val, _, _ := gatewayFidelitySF.Do("gateway_fidelity", func() (any, error) {
		if cached, ok := gatewayFidelityCache.Load().(*cachedGatewayFidelitySettings); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				settings := cached.settings
				return &settings, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gatewayFidelityDBTimeout)
		defer cancel()
		settings, err := s.GetGatewayFidelitySettings(dbCtx)
		ttl := gatewayFidelityCacheTTL
		if err != nil || settings == nil {
			settings = DefaultGatewayFidelitySettings()
			ttl = gatewayFidelityErrorTTL
		}
		gatewayFidelityCache.Store(&cachedGatewayFidelitySettings{
			settings:  *settings,
			expiresAt: time.Now().Add(ttl).UnixNano(),
		})
		return settings, nil
	})
	if settings, ok := val.(*GatewayFidelitySettings); ok && settings != nil {
		return settings
	}
	return DefaultGatewayFidelitySettings()
}

// gatewayFidelityFlags 是解析后的、可直接在转发路径上使用的开关快照。
type gatewayFidelityFlags struct {
	entranceStrict      bool
	toolMaxTokensFloor  bool
	toolMaxTokensValue  int64
	thinkingBindingGate bool
}

// ResolveGatewayFidelity 解析网关保真度修复开关，并按分组/账号级别做排除判断。
// fail-open：配置读取失败时按默认值（全部开启）处理。
func (s *SettingService) ResolveGatewayFidelity(ctx context.Context, accountID int64, groupID *int64) gatewayFidelityFlags {
	if s == nil {
		return gatewayFidelityFlags{}
	}
	settings := s.getGatewayFidelitySettingsCached(ctx)
	if settings == nil || !settings.Enabled {
		return gatewayFidelityFlags{}
	}
	if containsPositiveInt64(settings.ExcludedAccounts, accountID) {
		return gatewayFidelityFlags{}
	}
	if groupID != nil && containsPositiveInt64(settings.ExcludedGroups, *groupID) {
		return gatewayFidelityFlags{}
	}
	return gatewayFidelityFlags{
		entranceStrict:      settings.EntranceStrict,
		toolMaxTokensFloor:  settings.ToolMaxTokensFloor,
		toolMaxTokensValue:  settings.ToolMaxTokensFloorValue,
		thinkingBindingGate: settings.ThinkingBindingGated,
	}
}

// IsGatewayThinkingBindingGated 判断是否要求请求显式携带 thinking-binding-controls
// beta 才允许对签名 400 做自动剥离重试。
func (s *SettingService) IsGatewayThinkingBindingGated(ctx context.Context) bool {
	if s == nil {
		return false
	}
	settings := s.getGatewayFidelitySettingsCached(ctx)
	if settings == nil {
		return false
	}
	return settings.Enabled && settings.ThinkingBindingGated
}
