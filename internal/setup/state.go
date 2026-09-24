// Package setup 维护「站点是否已完成初始化」这一全局状态。
//
// 前台站点与后台服务都需要判断这个状态来决定是否放行请求，而状态只会在
// 初始化表单提交成功时由 false 变成 true，因此用一个原子布尔值在进程内共享，
// 避免每个请求都去查一次数据库。
package setup

import "sync/atomic"

// 初始化页面的地址。前台与后台都要用到，因此放在这里统一维护。
const (
	// InitPath 是站点初始化页面的规范地址。
	InitPath = "/init"
	// InitHTMLPath 是同一个页面的 .html 别名，方便直接在地址栏输入。
	InitHTMLPath = "/init.html"
)

// State 记录站点是否已完成初始化，可被多个 HTTP 处理器并发读取。
type State struct {
	done atomic.Bool
}

// NewState 依据数据库中的既有状态构造一个 State。
func NewState(initialized bool) *State {
	s := &State{}
	s.done.Store(initialized)
	return s
}

// Done 返回站点是否已完成初始化。
func (s *State) Done() bool {
	if s == nil {
		// 未注入 State 时按「已初始化」处理，保持与旧行为一致，避免把站点锁死。
		return true
	}
	return s.done.Load()
}

// MarkDone 在初始化成功后调用，此后 /init 不再对外开放。
func (s *State) MarkDone() {
	if s != nil {
		s.done.Store(true)
	}
}
