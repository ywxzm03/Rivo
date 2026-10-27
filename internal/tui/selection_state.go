package tui

import "time"

// selection 承载鼠标框选与复制提示的状态
type selection struct {
	anchor screenPoint
	focus  screenPoint
	active bool
	// lastClick 与 lastClickAt 用于识别双击选词
	lastClick   screenPoint
	lastClickAt time.Time
	// mouse 记录最近一次鼠标位置，供拖拽与滚轮使用
	mouse    screenPoint
	mouseSet bool
	fragment string
	// noticeSeq 与 notice 驱动复制成功的临时提示
	noticeSeq uint64
	notice    bool
}
