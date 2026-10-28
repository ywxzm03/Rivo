package tui

// transcriptView 承载 transcript 的渲染缓存、展开状态与点击命中区间
type transcriptView struct {
	showThinking bool
	showAllTools bool
	// hideTools 是 Ctrl+Q 的遮罩：整段工具组与思考块只剩一行提示
	hideTools bool
	cache     map[string]cachedMessage
	// expanded 记录单击展开的组、调用与思考块
	expandedGroups   map[string]bool
	expandedCalls    map[string]bool
	expandedThoughts map[string]bool
	// hits 是最近一次渲染的点击命中区间，用于把单击映射回调用
	hits []toolHit
	// messageHits 由 renderMessage 写入，refreshTranscript 换算成屏幕行
	messageHits []toolHit
	// width / count 记录上次写入视口时的宽度与消息条数，
	// 两者都没变且无消息重绘时跳过整帧重建
	width int
	count int
}

// queueMirror 是后台队列在 TUI 侧的镜像，靠 revision 丢弃过期快照
type queueMirror struct {
	steeringRevision uint64
	followUpRevision uint64
	followUpRestored bool
}
