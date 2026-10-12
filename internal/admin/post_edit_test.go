package admin

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/tmiqpl/blog/internal/auth"
	"github.com/tmiqpl/blog/internal/store"
)

// countAll 返回文章总数（含草稿）。
func countAll(t *testing.T, st *store.Store) int {
	t.Helper()

	stats, err := st.Stats()
	if err != nil {
		t.Fatalf("统计文章失败: %v", err)
	}
	return stats.TotalPosts
}

// TestEditPostChangingSlugKeepsSingleRecord 回归 issue #3：
// 后台编辑文章时修改别名，不应新增重复文章，而应就地更新同一条记录。
func TestEditPostChangingSlugKeepsSingleRecord(t *testing.T) {
	env := newTestServer(t, CaptchaModeOff)
	srv, st := env.server, env.store
	session := loginSession(t, srv)
	cookies := map[string]string{auth.SessionCookieName: session}
	csrf := sessionCSRF(t, srv, session)

	id, _, err := st.UpsertPost(store.PostInput{
		Title: "原始标题", Slug: "old-slug", Content: "原始正文", Published: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := doRequest(srv, http.MethodPost, "/admin/posts/"+strconv.FormatInt(id, 10), url.Values{
		auth.CSRFFormField: {csrf},
		"title":            {"更新后的标题"},
		"slug":             {"new-slug"},
		"content":          {"更新后的正文"},
		"published":        {"on"},
	}, cookies)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("保存应返回 303，实际 %d（%s）", rec.Code, rec.Body.String())
	}

	if got := countAll(t, st); got != 1 {
		t.Errorf("编辑改别名后文章总数 = %d，期望 1（出现重复文章）", got)
	}

	post, err := st.GetPostByID(id)
	if err != nil {
		t.Fatalf("原记录应仍存在: %v", err)
	}
	if post.Slug != "new-slug" {
		t.Errorf("slug = %q，期望 new-slug", post.Slug)
	}
	if post.Title != "更新后的标题" {
		t.Errorf("标题 = %q", post.Title)
	}
}

// TestEditPostKeepsSlugUpdatesInPlace 确保不改别名的普通编辑仍能正常更新。
func TestEditPostKeepsSlugUpdatesInPlace(t *testing.T) {
	env := newTestServer(t, CaptchaModeOff)
	srv, st := env.server, env.store
	session := loginSession(t, srv)
	cookies := map[string]string{auth.SessionCookieName: session}
	csrf := sessionCSRF(t, srv, session)

	id, _, err := st.UpsertPost(store.PostInput{
		Title: "标题", Slug: "same-slug", Content: "正文", Published: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := doRequest(srv, http.MethodPost, "/admin/posts/"+strconv.FormatInt(id, 10), url.Values{
		auth.CSRFFormField: {csrf},
		"title":            {"标题改"},
		"slug":             {"same-slug"},
		"content":          {"正文改"},
		"published":        {"on"},
	}, cookies)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("保存应返回 303，实际 %d", rec.Code)
	}

	if got := countAll(t, st); got != 1 {
		t.Errorf("总数 = %d，期望 1", got)
	}
	post, _ := st.GetPostByID(id)
	if post.Title != "标题改" || post.Slug != "same-slug" {
		t.Errorf("更新结果不符: title=%q slug=%q", post.Title, post.Slug)
	}
}

// TestEditPostSlugConflictRejected 目标别名被其他文章占用时应报错且不产生重复。
func TestEditPostSlugConflictRejected(t *testing.T) {
	env := newTestServer(t, CaptchaModeOff)
	srv, st := env.server, env.store
	session := loginSession(t, srv)
	cookies := map[string]string{auth.SessionCookieName: session}
	csrf := sessionCSRF(t, srv, session)

	if _, _, err := st.UpsertPost(store.PostInput{Title: "A", Slug: "slug-a", Content: "a", Published: true}); err != nil {
		t.Fatal(err)
	}
	idB, _, err := st.UpsertPost(store.PostInput{Title: "B", Slug: "slug-b", Content: "b", Published: true})
	if err != nil {
		t.Fatal(err)
	}

	rec := doRequest(srv, http.MethodPost, "/admin/posts/"+strconv.FormatInt(idB, 10), url.Values{
		auth.CSRFFormField: {csrf},
		"title":            {"B"},
		"slug":             {"slug-a"},
		"content":          {"b"},
		"published":        {"on"},
	}, cookies)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("别名冲突应返回 422，实际 %d", rec.Code)
	}
	if got := countAll(t, st); got != 2 {
		t.Errorf("冲突时不应新增记录，总数 = %d，期望 2", got)
	}
}

// TestCreatePostStillCreates 新增文章（id 为 0）行为不变。
func TestCreatePostStillCreates(t *testing.T) {
	env := newTestServer(t, CaptchaModeOff)
	srv, st := env.server, env.store
	session := loginSession(t, srv)
	cookies := map[string]string{auth.SessionCookieName: session}
	csrf := sessionCSRF(t, srv, session)

	rec := doRequest(srv, http.MethodPost, "/admin/posts", url.Values{
		auth.CSRFFormField: {csrf},
		"title":            {"新文章"},
		"slug":             {"brand-new"},
		"content":          {"正文"},
		"published":        {"on"},
	}, cookies)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("新增应返回 303，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if got := countAll(t, st); got != 1 {
		t.Errorf("新增后总数 = %d，期望 1", got)
	}
}
