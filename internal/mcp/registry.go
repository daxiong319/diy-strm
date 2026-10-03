package mcp

import "sync"

// 默认注册表：进程内只构建一次。
// 工具对象都是无状态值类型，因此可以安全共享。
var (
	defaultRegistryOnce sync.Once
	defaultRegistry     *Registry
)

// DefaultRegistry 返回默认工具注册表。
//
// 工具清单覆盖 litepan 的四个能力域：
//   - 影视发现与订阅（internal/discover/discovery）
//   - 离线下载队列（internal/offlinedownload）
//   - MoviePilot（internal/moviepilot）
//   - 网盘文件操作（internal/api，通过钩子注入）
func DefaultRegistry() *Registry {
	defaultRegistryOnce.Do(func() {
		registry := NewRegistry()
		registry.Register(
			// 影视发现与订阅
			mediaSubscriptionListTool{},
			mediaSubscriptionGetTool{},
			mediaSubscriptionRunTool{},
			mediaChannelListTool{},
			mediaChannelRunTool{},
			mediaEmbyMissingStatusTool{},
			mediaEmbyMissingResultsTool{},
			mediaEmbyMissingScanTool{},

			// 离线下载队列
			offlineListTool{},

			// MoviePilot
			mediaPilotSubscribesTool{},
			mediaPilotDownloadsTool{},
			mediaPilotSearchTool{},
			mediaPilotTestTool{},

			// 网盘文件操作
			netdiskListTool{},
			netdiskRenameTool{},
			netdiskMoveTool{},
			netdiskMkdirTool{},
			netdiskDeleteTool{},
		)
		defaultRegistry = registry
	})
	return defaultRegistry
}

// configSnapshotFunc 是当前生效的快照读取函数，可由宿主替换。
// 默认返回空快照（等价于「未启用」），保证配置未接入时不会误放行写工具。
var (
	configSnapshotMu   sync.RWMutex
	configSnapshotFunc = func() *McpConfigSnapshot { return &McpConfigSnapshot{} }
)

// SetConfigSnapshotFunc 注册配置快照读取函数（由 internal/api 在初始化时注入）。
//
// 用函数而不是配置值：每次请求都能读到最新配置，不需要在配置变更时重建服务端。
func SetConfigSnapshotFunc(fn func() *McpConfigSnapshot) {
	if fn == nil {
		return
	}
	configSnapshotMu.Lock()
	defer configSnapshotMu.Unlock()
	configSnapshotFunc = fn
}

// ConfigSnapshot 读取当前配置快照。
func ConfigSnapshot() *McpConfigSnapshot {
	configSnapshotMu.RLock()
	fn := configSnapshotFunc
	configSnapshotMu.RUnlock()
	if fn == nil {
		return &McpConfigSnapshot{}
	}
	return fn()
}

// DefaultServer 用默认注册表与当前配置构造 MCP 服务端。
func DefaultServer() *Server {
	return NewServer(DefaultRegistry(), ConfigSnapshot)
}
