package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tmiqpl/blog/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// connect 建立一个内存传输的 MCP 会话，用于直接调用工具。
func connect(t *testing.T, st *store.Store) *sdk.ClientSession {
	t.Helper()

	srv, err := NewServer(Options{Store: st})
	if err != nil {
		t.Fatalf("构造 MCP 服务失败: %v", err)
	}

	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.MCP().Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("连接服务端失败: %v", err)
	}

	client := sdk.NewClient(&sdk.Implementation{Name: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("连接客户端失败: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// callTool 调用一次工具并返回文本结果，同时报告是否为错误结果。
func callTool(t *testing.T, session *sdk.ClientSession, name string, args any) (string, bool) {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	var argMap map[string]any
	if err := json.Unmarshal(raw, &argMap); err != nil {
		t.Fatalf("反序列化参数失败: %v", err)
	}

	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      name,
		Arguments: argMap,
	})
	if err != nil {
		t.Fatalf("调用工具 %s 失败: %v", name, err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("工具 %s 未返回任何内容", name)
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("工具 %s 的返回不是文本", name)
	}
	return text.Text, res.IsError
}

func TestToolList(t *testing.T) {
	session := connect(t, newTestStore(t))

	var names []string
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("列出工具失败: %v", err)
		}
		names = append(names, tool.Name)
	}

	want := []string{"create_post", "update_post", "list_posts", "get_post"}
	if len(names) != len(want) {
		t.Fatalf("工具数量 = %d (%v)，期望 %d", len(names), names, len(want))
	}
	for _, w := range want {
		found := false
		for _, n := range names {
			if n == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("缺少工具 %s，实际有 %v", w, names)
		}
	}
}

func TestCreatePost(t *testing.T) {
	st := newTestStore(t)
	session := connect(t, st)

	text, isErr := callTool(t, session, "create_post", map[string]any{
		"title":   "MCP 新增的文章",
		"content": "# 标题\n\n正文内容。",
		"tags":    []string{"MCP", "Go"},
	})
	if isErr {
		t.Fatalf("新增失败: %s", text)
	}

	var out createPostOutput
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("解析结果失败: %v\n%s", err, text)
	}
	if out.ID <= 0 {
		t.Errorf("应返回有效 ID，实际 %d", out.ID)
	}
	if out.Slug == "" || !out.Published {
		t.Errorf("slug=%q published=%v", out.Slug, out.Published)
	}

	// 落库校验
	post, err := st.GetPostByID(out.ID)
	if err != nil {
		t.Fatal(err)
	}
	if post.Title != "MCP 新增的文章" {
		t.Errorf("标题 = %q", post.Title)
	}
	if len(post.Tags) != 2 {
		t.Errorf("标签数 = %d，期望 2", len(post.Tags))
	}
}

func TestCreatePostAsDraft(t *testing.T) {
	st := newTestStore(t)
	session := connect(t, st)

	text, isErr := callTool(t, session, "create_post", map[string]any{
		"title":     "草稿",
		"content":   "正文",
		"published": false,
	})
	if isErr {
		t.Fatalf("新增失败: %s", text)
	}

	var out createPostOutput
	_ = json.Unmarshal([]byte(text), &out)

	post, _ := st.GetPostByID(out.ID)
	if post.Published {
		t.Error("published=false 应存为草稿")
	}
}

func TestCreatePostValidation(t *testing.T) {
	session := connect(t, newTestStore(t))

	if _, isErr := callTool(t, session, "create_post", map[string]any{"title": "", "content": "x"}); !isErr {
		t.Error("空标题应返回错误")
	}
	if _, isErr := callTool(t, session, "create_post", map[string]any{"title": "x", "content": "  "}); !isErr {
		t.Error("空正文应返回错误")
	}
	if _, isErr := callTool(t, session, "create_post", map[string]any{
		"title": "x", "content": "y", "slug": "有 空格",
	}); !isErr {
		t.Error("非法 slug 应返回错误")
	}
}

func TestCreatePostSlugConflict(t *testing.T) {
	st := newTestStore(t)
	if _, _, err := st.UpsertPost(store.PostInput{Title: "已存在", Slug: "taken", Content: "x", Published: true}); err != nil {
		t.Fatal(err)
	}

	session := connect(t, st)
	_, isErr := callTool(t, session, "create_post", map[string]any{
		"title": "另一篇", "content": "y", "slug": "taken",
	})
	if !isErr {
		t.Error("slug 冲突应返回错误")
	}
}

func TestUpdatePostPartial(t *testing.T) {
	st := newTestStore(t)
	session := connect(t, st)

	text, _ := callTool(t, session, "create_post", map[string]any{
		"title": "原标题", "content": "原正文", "tags": []string{"Go"},
	})
	var created createPostOutput
	_ = json.Unmarshal([]byte(text), &created)

	// 只改标题，其余字段应保留
	text, isErr := callTool(t, session, "update_post", map[string]any{
		"id":    created.ID,
		"title": "新标题",
	})
	if isErr {
		t.Fatalf("编辑失败: %s", text)
	}

	post, err := st.GetPostByID(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if post.Title != "新标题" {
		t.Errorf("标题 = %q", post.Title)
	}
	if post.Content != "原正文" {
		t.Errorf("未传 content 应保留原值，实际 %q", post.Content)
	}
	if len(post.Tags) != 1 || post.Tags[0].Name != "Go" {
		t.Errorf("未传 tags 应保留原标签，实际 %+v", post.Tags)
	}
}

func TestUpdatePostChangesSlugWithoutDuplicating(t *testing.T) {
	st := newTestStore(t)
	session := connect(t, st)

	text, _ := callTool(t, session, "create_post", map[string]any{
		"title": "文章", "content": "正文", "slug": "old-slug",
	})
	var created createPostOutput
	_ = json.Unmarshal([]byte(text), &created)

	text, isErr := callTool(t, session, "update_post", map[string]any{
		"id": created.ID, "slug": "new-slug",
	})
	if isErr {
		t.Fatalf("改 slug 失败: %s", text)
	}

	// 关键：改 slug 后仍是同一条记录，不产生新文章
	n, _ := st.CountPosts()
	if n != 1 {
		t.Errorf("改 slug 不应新增记录，总数 = %d", n)
	}
	post, err := st.GetPostByID(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if post.Slug != "new-slug" {
		t.Errorf("slug = %q", post.Slug)
	}
}

func TestUpdatePostErrors(t *testing.T) {
	st := newTestStore(t)
	session := connect(t, st)

	if _, isErr := callTool(t, session, "update_post", map[string]any{"id": 9999, "title": "x"}); !isErr {
		t.Error("不存在的文章应返回错误")
	}
	if _, isErr := callTool(t, session, "update_post", map[string]any{"id": 0, "title": "x"}); !isErr {
		t.Error("id<=0 应返回错误")
	}

	text, _ := callTool(t, session, "create_post", map[string]any{"title": "A", "content": "a"})
	var created createPostOutput
	_ = json.Unmarshal([]byte(text), &created)
	if _, isErr := callTool(t, session, "update_post", map[string]any{"id": created.ID, "title": "  "}); !isErr {
		t.Error("空标题应返回错误")
	}
}

func TestListPosts(t *testing.T) {
	st := newTestStore(t)
	session := connect(t, st)

	callTool(t, session, "create_post", map[string]any{"title": "已发布", "content": "x", "published": true})
	callTool(t, session, "create_post", map[string]any{"title": "草稿", "content": "y", "published": false})

	// 全部
	text, isErr := callTool(t, session, "list_posts", map[string]any{})
	if isErr {
		t.Fatalf("列出失败: %s", text)
	}
	var all listPostsOutput
	_ = json.Unmarshal([]byte(text), &all)
	if all.Total != 2 {
		t.Errorf("全部总数 = %d，期望 2", all.Total)
	}

	// 仅草稿
	text, _ = callTool(t, session, "list_posts", map[string]any{"status": "draft"})
	var drafts listPostsOutput
	_ = json.Unmarshal([]byte(text), &drafts)
	if drafts.Total != 1 || drafts.Posts[0].Title != "草稿" {
		t.Errorf("草稿筛选结果不符: %+v", drafts)
	}

	// 关键词
	text, _ = callTool(t, session, "list_posts", map[string]any{"keyword": "已发布"})
	var kw listPostsOutput
	_ = json.Unmarshal([]byte(text), &kw)
	if kw.Total != 1 {
		t.Errorf("关键词筛选总数 = %d，期望 1", kw.Total)
	}

	// 非法 status
	if _, isErr := callTool(t, session, "list_posts", map[string]any{"status": "bogus"}); !isErr {
		t.Error("非法 status 应返回错误")
	}
}

func TestGetPost(t *testing.T) {
	st := newTestStore(t)
	session := connect(t, st)

	text, _ := callTool(t, session, "create_post", map[string]any{
		"title": "读取测试", "content": "# 正文\n\n内容", "tags": []string{"Go"},
	})
	var created createPostOutput
	_ = json.Unmarshal([]byte(text), &created)

	text, isErr := callTool(t, session, "get_post", map[string]any{"id": created.ID})
	if isErr {
		t.Fatalf("读取失败: %s", text)
	}
	var got getPostOutput
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != "读取测试" {
		t.Errorf("标题 = %q", got.Title)
	}
	if !strings.Contains(got.Content, "正文") {
		t.Errorf("应返回正文，实际 %q", got.Content)
	}

	if _, isErr := callTool(t, session, "get_post", map[string]any{"id": 9999}); !isErr {
		t.Error("不存在的文章应返回错误")
	}
}
