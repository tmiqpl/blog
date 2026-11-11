package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tmiqpl/blog/internal/store"
)

// seedSearchPosts 写入 8 篇正文都含 marker 的已发布文章，返回最新与最旧两篇的标题。
// 搜索结果按创建时间倒序，第一页应只见到最新篇，第二页只有最旧篇。
var searchTitleNums = []string{"一", "二", "三", "四", "五", "六", "七", "八"}

func seedSearchPosts(t *testing.T, st *store.Store, marker string) (newest, oldest string) {
	t.Helper()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 8; i++ {
		_, _, err := st.UpsertPost(store.PostInput{
			Title:     "搜索文章" + searchTitleNums[i-1] + "号",
			Slug:      "search-post-" + searchTitleNums[i-1],
			Content:   "包含 " + marker + " 的正文",
			Published: true,
			CreatedAt: base.AddDate(0, 0, i),
		})
		if err != nil {
			t.Fatalf("写入第 %d 篇文章失败: %v", i, err)
		}
	}
	return "搜索文章八号", "搜索文章一号"
}

func TestSearchHonorsPageParam(t *testing.T) {
	srv, st := newTestServer(t, true)
	newest, oldest := seedSearchPosts(t, st, "分页标记")

	q := url.Values{"q": {"分页标记"}}

	first := do(srv, http.MethodGet, "/search?"+q.Encode(), nil, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("搜索第一页应返回 200，实际 %d", first.Code)
	}
	if !strings.Contains(first.Body.String(), newest) {
		t.Errorf("第一页应包含最新的 %q", newest)
	}
	if strings.Contains(first.Body.String(), oldest) {
		t.Errorf("第一页（每页 %d 篇）不应包含最旧的 %q", PerPage, oldest)
	}

	second := do(srv, http.MethodGet, "/search?"+q.Encode()+"&page=2", nil, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("搜索第二页应返回 200，实际 %d", second.Code)
	}
	if !strings.Contains(second.Body.String(), oldest) {
		t.Error("第二页应包含最旧的文章")
	}
	if strings.Contains(second.Body.String(), newest) {
		t.Error("第二页不应再包含第一页的最新文章——page 参数被忽略了")
	}
}

func TestSearchPaginationLinkEncoded(t *testing.T) {
	srv, st := newTestServer(t, true)
	seedSearchPosts(t, st, "a&b")

	// 关键词含 &，分页链接里的关键词必须编码，否则 q 参数会被截断
	rec := do(srv, http.MethodGet, "/search?q=a%26b", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("搜索应返回 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, `href="/search?q=a%26b&amp;page=2"`) {
		t.Error(`分页「下一页」链接应为 /search?q=a%26b&page=2（关键词已编码）`)
	}
	if !strings.Contains(body, "下一页") {
		t.Error("搜索结果超过一页时应渲染分页导航")
	}
}

func TestSearchShowsTotalCount(t *testing.T) {
	srv, st := newTestServer(t, true)
	seedSearchPosts(t, st, "计数标记")

	rec := do(srv, http.MethodGet, "/search?q=%E8%AE%A1%E6%95%B0%E6%A0%87%E8%AE%B0", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("搜索应返回 200，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "共 8 篇") {
		t.Error("副标题应显示命中的文章总数「共 8 篇」，而不是当前页的条数")
	}
}
