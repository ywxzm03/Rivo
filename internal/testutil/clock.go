package testutil

import (
	"sync"
	"time"
)

// Clock 是可推进的时钟，替代测试里真实等待
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock 从固定时间点开始
func NewClock(start time.Time) *Clock { return &Clock{now: start} }

// Now 返回当前时间
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance 推进时钟并返回新时间
func (c *Clock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}
