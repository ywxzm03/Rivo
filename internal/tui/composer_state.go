package tui

// composer 承载输入框侧的交互状态：补全、历史、草稿与粘贴/图片注册表
type composer struct {
	// selector 非空时输入被选择器接管
	selector *selector
	// completion 是当前补全候选下标，completionHidden 隐藏候选列表
	completion       int
	completionHidden bool
	histories        composerHistories // 各 scope 的本地条目及共享持久快照
	// pastes 把折叠标记映射回粘贴原文，images 同理映射图片
	pastes       map[int]string
	pasteCounter int
	images       map[int]ImagePart
	imageCounter int
}
