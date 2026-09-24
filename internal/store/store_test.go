package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/tmiqpl/blog/internal/models"
)

// newTestStore 创建一个隔离的空数据库，不写入示例数据。
func newTestStore(t *testing.T) *Store {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func mustCreate(t *testing.T, st *Store, in PostInput) int64 {
	t.Helper()

	id, _, err := st.UpsertPost(in)
	if err != nil {
		t.Fatalf("创建文章失败: %v", err)
	}
	return id
}

func TestSeedSamplePosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.db")

	st, err := Open(path)
	if err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}
	defer st.Close()

	// Open 只建表，不写入任何内容——此时站点处于「未初始化」状态
	if n, err := st.CountPosts(); err != nil || n != 0 {
		t.Errorf("Open 不应写入示例数据，实际 %d 篇 (err=%v)", n, err)
	}

	if err := st.SeedSamplePosts(); err != nil {
		t.Fatalf("写入示例文章失败: %v", err)
	}

	n, err := st.CountPosts()
	if err != nil {
		t.Fatal(err)
	}
	if n != len(seedPosts) {
		t.Errorf("应写入 %d 篇示例文章，实际 %d", len(seedPosts), n)
	}

	tags, err := st.ListTags()
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) == 0 {
		t.Error("示例文章应带有关联标签")
	}
}

func TestSeedRunsOnlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "once.db")

	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SeedSamplePosts(); err != nil {
		t.Fatal(err)
	}
	before, _ := first.CountPosts()
	_ = first.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.SeedSamplePosts(); err != nil {
		t.Fatal(err)
	}

	after, _ := second.CountPosts()
	if before != after {
		t.Errorf("重复调用不应重复写入示例数据，%d → %d", before, after)
	}
}

func TestInitializedFlag(t *testing.T) {
	st := newTestStore(t)

	ok, err := st.IsInitialized()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("新建数据库不应处于已初始化状态")
	}

	if err := st.MarkInitialized(); err != nil {
		t.Fatal(err)
	}

	ok, err = st.IsInitialized()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("标记后应处于已初始化状态")
	}

	// 其他设置项的存在不应影响判定
	if err := st.SetSetting(SettingSiteTitle, "随便什么"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.IsInitialized(); !ok {
		t.Error("写入其他设置项后仍应保持已初始化")
	}
}

// 旧版本的库没有 site_initialized 标记，但已经有管理员账号，
// 这种情况应视为早已初始化，不能突然要求重新初始化。
func TestIsInitializedFallsBackToExistingAdmin(t *testing.T) {
	st := newTestStore(t)

	if err := st.SetSetting(SettingAdminUsername, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(SettingAdminPassword, "pbkdf2-sha256$1000$c2FsdA$aGFzaA"); err != nil {
		t.Fatal(err)
	}

	ok, err := st.IsInitialized()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("已存在管理员账号时应视为已初始化")
	}
}

// 只有站点信息、没有管理员账号，说明初始化没走完，仍应视为未初始化。
func TestIsInitializedWhenOnlySiteInfoExists(t *testing.T) {
	st := newTestStore(t)

	if err := st.SetSetting(SettingSiteTitle, "半个站点"); err != nil {
		t.Fatal(err)
	}

	ok, err := st.IsInitialized()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("只有站点信息、没有管理员账号时不应视为已初始化")
	}
}

func TestUpsertPostCreateAndUpdate(t *testing.T) {
	st := newTestStore(t)

	id := mustCreate(t, st, PostInput{
		Title:     "第一篇",
		Slug:      "first",
		Content:   "# 第一篇\n\n正文内容。",
		Published: true,
		Tags:      []string{"Go", "后端"},
	})

	post, err := st.GetPostBySlug("first")
	if err != nil {
		t.Fatalf("读取文章失败: %v", err)
	}
	if post.ID != id {
		t.Errorf("ID = %d，期望 %d", post.ID, id)
	}
	if post.Title != "第一篇" {
		t.Errorf("标题 = %q", post.Title)
	}
	if len(post.Tags) != 2 {
		t.Errorf("标签数 = %d，期望 2", len(post.Tags))
	}
	// 未提供摘要时应自动生成
	if post.Summary == "" {
		t.Error("摘要应自动从正文提取")
	}

	// 同 slug 再次写入应更新而非新建
	newID := mustCreate(t, st, PostInput{
		Title:     "第一篇（改）",
		Slug:      "first",
		Content:   "改后的正文",
		Published: true,
		Tags:      []string{"Go"},
	})
	if newID != id {
		t.Errorf("同 slug 应更新同一条记录，ID %d → %d", id, newID)
	}

	updated, err := st.GetPostBySlug("first")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "第一篇（改）" {
		t.Errorf("标题未更新: %q", updated.Title)
	}
	if len(updated.Tags) != 1 {
		t.Errorf("标签应被重建为 1 个，实际 %d", len(updated.Tags))
	}

	total, _ := st.CountPosts()
	if total != 1 {
		t.Errorf("更新不应产生新记录，总数 = %d", total)
	}
}

func TestUpsertPostValidation(t *testing.T) {
	st := newTestStore(t)

	if _, _, err := st.UpsertPost(PostInput{Title: "   "}); err == nil {
		t.Error("空标题应返回错误")
	}
}

func TestUpsertPostGeneratesSlugFromTitle(t *testing.T) {
	st := newTestStore(t)

	id := mustCreate(t, st, PostInput{Title: "Hello World", Content: "x", Published: true})

	post, err := st.GetPostByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if post.Slug != "hello-world" {
		t.Errorf("slug = %q，期望 hello-world", post.Slug)
	}
}

func TestDraftIsHiddenFromPublicQueries(t *testing.T) {
	st := newTestStore(t)

	mustCreate(t, st, PostInput{Title: "已发布", Slug: "published", Content: "x", Published: true})
	mustCreate(t, st, PostInput{Title: "草稿", Slug: "draft", Content: "x", Published: false})

	if _, err := st.GetPostBySlug("draft"); err != ErrNotFound {
		t.Errorf("草稿不应能通过 GetPostBySlug 取到，实际 err = %v", err)
	}

	posts, err := st.ListPosts(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].Slug != "published" {
		t.Errorf("列表应只含已发布文章，实际 %d 篇", len(posts))
	}

	n, _ := st.CountPosts()
	if n != 1 {
		t.Errorf("公开计数应只统计已发布文章，实际 %d", n)
	}

	// 后台应能看到全部
	all, total, err := st.ListPostsAdmin(PostFilter{}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || total != 2 {
		t.Errorf("后台应看到 2 篇文章，实际 %d（total=%d）", len(all), total)
	}
}

func TestListPostsPaginationAndOrder(t *testing.T) {
	st := newTestStore(t)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		mustCreate(t, st, PostInput{
			Title:     string(rune('A'+i)) + " 文章",
			Slug:      "post-" + string(rune('a'+i)),
			Content:   "x",
			Published: true,
			CreatedAt: base.AddDate(0, 0, i),
		})
	}

	first, err := st.ListPosts(3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 {
		t.Fatalf("第一页应有 3 篇，实际 %d", len(first))
	}
	// 按时间倒序，最新的是第 7 篇（下标 6 → 'G'）
	if first[0].Slug != "post-g" {
		t.Errorf("应按发布时间倒序，首篇为 %q", first[0].Slug)
	}

	last, err := st.ListPosts(3, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 1 {
		t.Errorf("最后一页应有 1 篇，实际 %d", len(last))
	}
}

func TestListPostsByTag(t *testing.T) {
	st := newTestStore(t)

	mustCreate(t, st, PostInput{Title: "A", Slug: "a", Content: "x", Published: true, Tags: []string{"Go", "后端"}})
	mustCreate(t, st, PostInput{Title: "B", Slug: "b", Content: "x", Published: true, Tags: []string{"Go"}})
	mustCreate(t, st, PostInput{Title: "C", Slug: "c", Content: "x", Published: true, Tags: []string{"前端"}})

	posts, err := st.ListPostsByTag("go", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 {
		t.Errorf("Go 标签下应有 2 篇，实际 %d", len(posts))
	}

	n, err := st.CountPostsByTag("go")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("Go 标签计数 = %d，期望 2", n)
	}

	tag, err := st.GetTagBySlug("go")
	if err != nil {
		t.Fatal(err)
	}
	if tag.Count != 2 {
		t.Errorf("标签 Count = %d，期望 2", tag.Count)
	}

	if _, err := st.GetTagBySlug("不存在"); err != ErrNotFound {
		t.Errorf("不存在的标签应返回 ErrNotFound，实际 %v", err)
	}
}

func TestListTagsExcludesUnused(t *testing.T) {
	st := newTestStore(t)

	mustCreate(t, st, PostInput{Title: "A", Slug: "a", Content: "x", Published: true, Tags: []string{"在用"}})
	mustCreate(t, st, PostInput{Title: "B", Slug: "b", Content: "x", Published: false, Tags: []string{"仅草稿"}})

	tags, err := st.ListTags()
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if tag.Name == "仅草稿" {
			t.Error("只被草稿引用的标签不应出现在前台标签列表中")
		}
	}
	if len(tags) != 1 || tags[0].Name != "在用" {
		t.Errorf("前台标签列表应只有 1 个，实际 %v", tags)
	}

	adminTags, err := st.ListTagsAdmin()
	if err != nil {
		t.Fatal(err)
	}
	if len(adminTags) != 2 {
		t.Errorf("后台标签列表应有 2 个，实际 %d", len(adminTags))
	}
}

func TestSearchPosts(t *testing.T) {
	st := newTestStore(t)

	mustCreate(t, st, PostInput{Title: "Go 并发模式", Slug: "go-concurrency", Content: "关于 goroutine", Published: true})
	mustCreate(t, st, PostInput{Title: "SQLite 实践", Slug: "sqlite", Content: "关于 WAL 模式", Published: true})
	mustCreate(t, st, PostInput{Title: "隐藏的 Go 文章", Slug: "hidden", Content: "草稿", Published: false})

	cases := map[string]int{
		"Go":        1, // 标题匹配已发布文章，草稿不计
		"goroutine": 1,
		"WAL":       1,
		"不存在关键词":    0,
	}

	for keyword, want := range cases {
		posts, err := st.SearchPosts(keyword, 20)
		if err != nil {
			t.Fatalf("搜索 %q 失败: %v", keyword, err)
		}
		if len(posts) != want {
			t.Errorf("搜索 %q 得到 %d 条，期望 %d", keyword, len(posts), want)
		}
	}
}

func TestRelatedPosts(t *testing.T) {
	st := newTestStore(t)

	a := mustCreate(t, st, PostInput{Title: "A", Slug: "a", Content: "x", Published: true, Tags: []string{"Go", "后端"}})
	mustCreate(t, st, PostInput{Title: "B", Slug: "b", Content: "x", Published: true, Tags: []string{"Go", "后端"}})
	mustCreate(t, st, PostInput{Title: "C", Slug: "c", Content: "x", Published: true, Tags: []string{"Go"}})
	mustCreate(t, st, PostInput{Title: "D", Slug: "d", Content: "x", Published: true, Tags: []string{"前端"}})

	related, err := st.RelatedPosts(a, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(related) != 2 {
		t.Fatalf("应有 2 篇相关文章，实际 %d", len(related))
	}
	// 共享标签更多的排前面
	if related[0].Slug != "b" {
		t.Errorf("共享 2 个标签的文章应排第一，实际 %q", related[0].Slug)
	}
	for _, p := range related {
		if p.Slug == "a" {
			t.Error("相关文章不应包含自己")
		}
	}
}

func TestSetPublishedAndDelete(t *testing.T) {
	st := newTestStore(t)
	id := mustCreate(t, st, PostInput{Title: "A", Slug: "a", Content: "x", Published: true})

	if err := st.SetPublished(id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetPostBySlug("a"); err != ErrNotFound {
		t.Error("转为草稿后不应在前台可见")
	}

	if err := st.SetPublished(id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetPostBySlug("a"); err != nil {
		t.Errorf("重新发布后应可见，实际 %v", err)
	}

	if err := st.DeletePostByID(id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetPostByID(id); err != ErrNotFound {
		t.Error("删除后应查不到")
	}
	if err := st.DeletePostByID(id); err != ErrNotFound {
		t.Errorf("重复删除应返回 ErrNotFound，实际 %v", err)
	}
}

func TestDeletePostCascadesTags(t *testing.T) {
	st := newTestStore(t)
	id := mustCreate(t, st, PostInput{Title: "A", Slug: "a", Content: "x", Published: true, Tags: []string{"临时"}})

	if err := st.DeletePostByID(id); err != nil {
		t.Fatal(err)
	}

	tags, err := st.ListTagsAdmin()
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if tag.Name == "临时" && tag.Count != 0 {
			t.Error("文章删除后标签关联应被级联清除")
		}
	}
}

func TestSlugTaken(t *testing.T) {
	st := newTestStore(t)
	id := mustCreate(t, st, PostInput{Title: "A", Slug: "a", Content: "x", Published: true})

	taken, err := st.SlugTaken("a", 0)
	if err != nil || !taken {
		t.Errorf("slug 应被判定为已占用（taken=%v, err=%v）", taken, err)
	}

	// 排除自身时不算占用
	taken, err = st.SlugTaken("a", id)
	if err != nil || taken {
		t.Errorf("排除自身时不应判定为占用（taken=%v, err=%v）", taken, err)
	}

	taken, _ = st.SlugTaken("brand-new", 0)
	if taken {
		t.Error("未被使用的 slug 不应判定为占用")
	}
}

func TestRenameAndDeleteTag(t *testing.T) {
	st := newTestStore(t)
	mustCreate(t, st, PostInput{Title: "A", Slug: "a", Content: "x", Published: true, Tags: []string{"旧名"}})

	tags, _ := st.ListTagsAdmin()
	if len(tags) != 1 {
		t.Fatalf("应有 1 个标签，实际 %d", len(tags))
	}
	id := tags[0].ID

	if err := st.RenameTag(id, "新名", "new-name"); err != nil {
		t.Fatalf("重命名失败: %v", err)
	}
	renamed, err := st.GetTagBySlug("new-name")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "新名" {
		t.Errorf("标签名 = %q", renamed.Name)
	}

	// 名称冲突应报错
	mustCreate(t, st, PostInput{Title: "B", Slug: "b", Content: "x", Published: true, Tags: []string{"另一个"}})
	all, _ := st.ListTagsAdmin()
	for _, tag := range all {
		if tag.Name == "另一个" {
			if err := st.RenameTag(tag.ID, "新名", "conflict"); err == nil {
				t.Error("重命名为已存在的名称应报错")
			}
		}
	}

	// 仅删除标签，文章保留
	removed, err := st.DeleteTag(id, false)
	if err != nil {
		t.Fatalf("删除标签失败: %v", err)
	}
	if removed != 0 {
		t.Errorf("仅删除标签时不应删除文章，实际删除 %d 篇", removed)
	}
	if n, _ := st.CountPosts(); n != 2 {
		t.Errorf("文章应保留，实际 %d 篇", n)
	}
}

func TestDeleteTagWithPosts(t *testing.T) {
	st := newTestStore(t)
	mustCreate(t, st, PostInput{Title: "A", Slug: "a", Content: "x", Published: true, Tags: []string{"要删的"}})
	mustCreate(t, st, PostInput{Title: "B", Slug: "b", Content: "x", Published: true, Tags: []string{"保留的"}})

	tags, _ := st.ListTagsAdmin()
	var target int64
	for _, tag := range tags {
		if tag.Name == "要删的" {
			target = tag.ID
		}
	}

	removed, err := st.DeleteTag(target, true)
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if removed != 1 {
		t.Errorf("应删除 1 篇文章，实际 %d", removed)
	}

	posts, _ := st.ListPosts(10, 0)
	if len(posts) != 1 || posts[0].Slug != "b" {
		t.Errorf("应只保留 b 文章，实际 %d 篇", len(posts))
	}
}

func TestCleanupOrphanTags(t *testing.T) {
	st := newTestStore(t)
	id := mustCreate(t, st, PostInput{Title: "A", Slug: "a", Content: "x", Published: true, Tags: []string{"孤儿"}})

	// 先让标签失去引用，模拟历史遗留数据
	if _, err := st.db.Exec(`DELETE FROM post_tags WHERE post_id = ?`, id); err != nil {
		t.Fatal(err)
	}

	n, err := st.CleanupOrphanTags()
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if n != 1 {
		t.Errorf("应清理 1 个空标签，实际 %d", n)
	}

	again, err := st.CleanupOrphanTags()
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("重复清理应无事可做，实际清理 %d 个", again)
	}
}

func TestStats(t *testing.T) {
	st := newTestStore(t)

	mustCreate(t, st, PostInput{Title: "A", Slug: "a", Content: "12345", Published: true, Tags: []string{"x"}})
	mustCreate(t, st, PostInput{Title: "B", Slug: "b", Content: "123", Published: false, Tags: []string{"y"}})

	stats, err := st.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalPosts != 2 || stats.PublishedPosts != 1 || stats.DraftPosts != 1 {
		t.Errorf("文章统计错误: %+v", stats)
	}
	if stats.TotalTags != 2 {
		t.Errorf("标签数 = %d，期望 2", stats.TotalTags)
	}
	if stats.TotalWords != 8 {
		t.Errorf("正文字符数 = %d，期望 8", stats.TotalWords)
	}
	if stats.LatestPost == nil {
		t.Error("应返回最近更新的文章")
	}
}

func TestStatsOnEmptyDatabase(t *testing.T) {
	st := newTestStore(t)

	stats, err := st.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalPosts != 0 || stats.TotalWords != 0 || stats.LatestPost != nil {
		t.Errorf("空库统计应全为零值，实际 %+v", stats)
	}
}

func TestPostFilter(t *testing.T) {
	st := newTestStore(t)

	mustCreate(t, st, PostInput{Title: "Go 教程", Slug: "go-guide", Content: "内容", Published: true})
	mustCreate(t, st, PostInput{Title: "Go 教程草稿", Slug: "go-draft", Content: "内容", Published: false})
	mustCreate(t, st, PostInput{Title: "SQL 教程", Slug: "sql-guide", Content: "内容", Published: true})

	cases := []struct {
		filter PostFilter
		want   int
	}{
		{PostFilter{}, 3},
		{PostFilter{Status: "published"}, 2},
		{PostFilter{Status: "draft"}, 1},
		{PostFilter{Keyword: "Go"}, 2},
		{PostFilter{Keyword: "教程"}, 3},
		{PostFilter{Keyword: "sql"}, 1}, // LIKE 在 SQLite 中对 ASCII 不区分大小写
		{PostFilter{Status: "published", Keyword: "教程"}, 2},
		{PostFilter{Status: "draft", Keyword: "教程"}, 1},
		{PostFilter{Keyword: "不存在"}, 0},
	}

	for _, c := range cases {
		_, total, err := st.ListPostsAdmin(c.filter, 10, 0)
		if err != nil {
			t.Fatalf("筛选 %+v 失败: %v", c.filter, err)
		}
		if total != c.want {
			t.Errorf("筛选 %+v 得到 %d 条，期望 %d", c.filter, total, c.want)
		}
	}
}

func TestSettings(t *testing.T) {
	st := newTestStore(t)

	value, err := st.GetSetting("missing")
	if err != nil {
		t.Fatal(err)
	}
	if value != "" {
		t.Errorf("不存在的设置项应返回空字符串，实际 %q", value)
	}

	if err := st.SetSetting("site_title", "我的博客"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("site_title", "新标题"); err != nil {
		t.Fatal(err)
	}

	value, _ = st.GetSetting("site_title")
	if value != "新标题" {
		t.Errorf("设置应被覆盖，实际 %q", value)
	}

	if err := st.SetSettings(map[string]string{"a": "1", "b": "2"}); err != nil {
		t.Fatal(err)
	}
	all, err := st.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all["a"] != "1" || all["b"] != "2" {
		t.Errorf("批量写入结果错误: %v", all)
	}
}

func TestSeedSettingsDoesNotOverwrite(t *testing.T) {
	st := newTestStore(t)

	if err := st.SeedSettings(map[string]string{"k": "默认值"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("k", "用户值"); err != nil {
		t.Fatal(err)
	}
	if err := st.SeedSettings(map[string]string{"k": "默认值", "new": "新增"}); err != nil {
		t.Fatal(err)
	}

	all, _ := st.Settings()
	if all["k"] != "用户值" {
		t.Errorf("已存在的设置不应被默认值覆盖，实际 %q", all["k"])
	}
	if all["new"] != "新增" {
		t.Errorf("缺失的设置应被补齐，实际 %q", all["new"])
	}
}

func TestAllPostsIncludesDrafts(t *testing.T) {
	st := newTestStore(t)
	mustCreate(t, st, PostInput{Title: "A", Slug: "a", Content: "x", Published: true})
	mustCreate(t, st, PostInput{Title: "B", Slug: "b", Content: "x", Published: false})

	posts, err := st.AllPosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 {
		t.Errorf("导出应包含草稿，实际 %d 篇", len(posts))
	}
}

func TestReadingMinutes(t *testing.T) {
	cases := []struct {
		content string
		want    int
	}{
		{"", 1},
		{"短", 1},
		{string(make([]rune, 400)), 1},
		{string(make([]rune, 401)), 2},
		{string(make([]rune, 1200)), 3},
	}

	for _, c := range cases {
		p := &models.Post{Content: c.content}
		if got := p.ReadingMinutes(); got != c.want {
			t.Errorf("内容长度 %d → 阅读时长 %d，期望 %d", len([]rune(c.content)), got, c.want)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Golang":      "golang",
		"Hello World": "hello-world",
		"后端":          "后端", // 中文直接保留，保证 URL 可读
		"A_b-c":       "a-b-c",
		"  Go  ":      "go",
	}

	for input, want := range cases {
		if got := Slugify(input); got != want {
			t.Errorf("Slugify(%q) = %q，期望 %q", input, got, want)
		}
	}
}
