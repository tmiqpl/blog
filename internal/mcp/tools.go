package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tmiqpl/blog/internal/models"
	"github.com/tmiqpl/blog/internal/store"
)

// 文章列表一次最多返回多少条，避免把整个库塞进模型上下文。
const maxListLimit = 100

/* --------------------------- 统一返回格式 --------------------------- */

// jsonResult 把结构化数据序列化成一段文本内容返回。
//
// 之所以不依赖 SDK 的 OutputSchema 自动填充：本服务的出参形状不一（有时是单篇、
// 有时是列表），统一走文本 JSON 更直观，模型也更容易直接读取。
func jsonResult(v any) (*sdk.CallToolResult, any, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return &sdk.CallToolResult{
			IsError: true,
			Content: []sdk.Content{&sdk.TextContent{Text: "序列化结果失败：" + err.Error()}},
		}, nil, nil
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: string(raw)}},
	}, nil, nil
}

// errResult 构造一个「工具执行失败」的结果。
//
// 按 MCP 约定，业务错误应放在 Content 里并把 IsError 置为 true，
// 而不是返回协议层错误——这样模型能看到失败原因并自行纠正。
func errResult(format string, args ...any) (*sdk.CallToolResult, any, error) {
	return &sdk.CallToolResult{
		IsError: true,
		Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf(format, args...)}},
	}, nil, nil
}

// postURL 拼出文章在前台的访问地址。
func postURL(slug string) string { return "/post/" + slug }

/* ---------------------------- create_post ---------------------------- */

type createPostInput struct {
	Title     string   `json:"title" jsonschema:"文章标题，不能为空"`
	Content   string   `json:"content" jsonschema:"Markdown 格式的正文，不能为空"`
	Slug      string   `json:"slug,omitempty" jsonschema:"URL 别名，只能包含中文、字母、数字、连字符和下划线；省略时由标题自动生成"`
	Summary   string   `json:"summary,omitempty" jsonschema:"文章摘要；省略时自动从正文截取"`
	Tags      []string `json:"tags,omitempty" jsonschema:"文章标签列表"`
	Published *bool    `json:"published,omitempty" jsonschema:"是否立即发布；默认 true，传 false 则存为草稿"`
	CreatedAt string   `json:"createdAt,omitempty" jsonschema:"发布时间，RFC3339 格式（如 2026-10-12T09:00:00Z）；省略时取当前时间"`
}

type createPostOutput struct {
	ID        int64  `json:"id"`
	Slug      string `json:"slug"`
	URL       string `json:"url"`
	Published bool   `json:"published"`
	Created   bool   `json:"created"`
}

func (s *Server) handleCreatePost(_ context.Context, _ *sdk.CallToolRequest, in createPostInput) (*sdk.CallToolResult, any, error) {
	if strings.TrimSpace(in.Title) == "" {
		return errResult("文章标题不能为空")
	}
	if strings.TrimSpace(in.Content) == "" {
		return errResult("正文不能为空")
	}

	slug := strings.TrimSpace(in.Slug)
	if slug == "" {
		// 自动生成：Slugify 对中英混排会原样返回标题，可能含空格等非法字符，
		// 故再用 sanitizeSlug 清洗一次，保证 AI 传入常见中文标题时不会卡住。
		slug = sanitizeSlug(store.Slugify(in.Title))
	}
	if !store.ValidSlug(slug) {
		return errResult("别名 %q 不合法：只能包含中文、字母、数字、连字符和下划线", slug)
	}
	if taken, err := s.store.SlugTaken(slug, 0); err != nil {
		return errResult("校验别名失败：%v", err)
	} else if taken {
		return errResult("别名 %q 已被其他文章占用，请换一个或改用 update_post", slug)
	}

	createdAt, err := parseTimeArg(in.CreatedAt)
	if err != nil {
		return errResult("createdAt 解析失败：%v", err)
	}

	published := true
	if in.Published != nil {
		published = *in.Published
	}

	id, isNew, err := s.store.UpsertPost(store.PostInput{
		Title:     in.Title,
		Slug:      slug,
		Summary:   in.Summary,
		Content:   in.Content,
		Published: published,
		CreatedAt: createdAt,
		Tags:      in.Tags,
	})
	if err != nil {
		return errResult("新增文章失败：%v", err)
	}

	return jsonResult(createPostOutput{
		ID: id, Slug: slug, URL: postURL(slug), Published: published, Created: isNew,
	})
}

/* ---------------------------- update_post ---------------------------- */

type updatePostInput struct {
	ID        int64    `json:"id" jsonschema:"要编辑的文章 ID"`
	Title     *string  `json:"title,omitempty" jsonschema:"新标题；省略则保持不变"`
	Content   *string  `json:"content,omitempty" jsonschema:"新的 Markdown 正文；省略则保持不变"`
	Slug      *string  `json:"slug,omitempty" jsonschema:"新的 URL 别名；省略则保持不变"`
	Summary   *string  `json:"summary,omitempty" jsonschema:"新的摘要；省略则保持不变"`
	Tags      []string `json:"tags,omitempty" jsonschema:"新的标签列表，传入即整体替换；省略则保持不变"`
	Published *bool    `json:"published,omitempty" jsonschema:"是否发布；省略则保持不变"`
}

type updatePostOutput struct {
	ID        int64  `json:"id"`
	Slug      string `json:"slug"`
	URL       string `json:"url"`
	Published bool   `json:"published"`
	Updated   bool   `json:"updated"`
}

func (s *Server) handleUpdatePost(_ context.Context, _ *sdk.CallToolRequest, in updatePostInput) (*sdk.CallToolResult, any, error) {
	if in.ID <= 0 {
		return errResult("id 必须为正整数")
	}

	post, err := s.store.GetPostByID(in.ID)
	if errors.Is(err, store.ErrNotFound) {
		return errResult("文章 %d 不存在，可用 list_posts 查看现有文章", in.ID)
	}
	if err != nil {
		return errResult("读取文章失败：%v", err)
	}

	// 未传入的字段保留原值
	title := post.Title
	content := post.Content
	slug := post.Slug
	summary := post.Summary
	published := post.Published

	if in.Title != nil {
		title = strings.TrimSpace(*in.Title)
		if title == "" {
			return errResult("标题不能为空")
		}
	}
	if in.Content != nil {
		content = *in.Content
		if strings.TrimSpace(content) == "" {
			return errResult("正文不能为空")
		}
	}
	if in.Slug != nil {
		slug = strings.TrimSpace(*in.Slug)
		if !store.ValidSlug(slug) {
			return errResult("别名 %q 不合法：只能包含中文、字母、数字、连字符和下划线", slug)
		}
		if taken, err := s.store.SlugTaken(slug, post.ID); err != nil {
			return errResult("校验别名失败：%v", err)
		} else if taken {
			return errResult("别名 %q 已被其他文章占用", slug)
		}
	}
	if in.Summary != nil {
		summary = strings.TrimSpace(*in.Summary)
	}
	if in.Published != nil {
		published = *in.Published
	}

	tags := postTagNames(post)
	if in.Tags != nil {
		tags = in.Tags
	}

	// 按 ID 更新，允许同时修改 slug（UpsertPost 按 slug 定位，改 slug 会变成新增）
	if err := s.store.UpdatePostByID(in.ID, store.PostInput{
		Title:     title,
		Slug:      slug,
		Summary:   summary,
		Content:   content,
		Published: published,
		Tags:      tags,
	}); err != nil {
		return errResult("更新文章失败：%v", err)
	}

	return jsonResult(updatePostOutput{
		ID: in.ID, Slug: slug, URL: postURL(slug), Published: published, Updated: true,
	})
}

/* ---------------------------- list_posts ---------------------------- */

type listPostsInput struct {
	Status  string `json:"status,omitempty" jsonschema:"按状态筛选：all（全部，默认）| published（仅已发布）| draft（仅草稿）"`
	Keyword string `json:"keyword,omitempty" jsonschema:"按标题或正文关键词过滤"`
	Limit   int    `json:"limit,omitempty" jsonschema:"最多返回多少条，默认 20，上限 100"`
}

type postSummary struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Slug      string   `json:"slug"`
	Summary   string   `json:"summary"`
	Published bool     `json:"published"`
	Tags      []string `json:"tags"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

type listPostsOutput struct {
	Total int           `json:"total"`
	Count int           `json:"count"`
	Posts []postSummary `json:"posts"`
}

func (s *Server) handleListPosts(_ context.Context, _ *sdk.CallToolRequest, in listPostsInput) (*sdk.CallToolResult, any, error) {
	status := strings.ToLower(strings.TrimSpace(in.Status))
	switch status {
	case "", "all":
		status = ""
	case "published", "draft":
		// 保留
	default:
		return errResult("status 只能是 all、published 或 draft")
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	posts, total, err := s.store.ListPostsAdmin(store.PostFilter{
		Status:  status,
		Keyword: in.Keyword,
	}, limit, 0)
	if err != nil {
		return errResult("读取文章列表失败：%v", err)
	}

	out := listPostsOutput{Total: total, Count: len(posts), Posts: make([]postSummary, 0, len(posts))}
	for _, p := range posts {
		out.Posts = append(out.Posts, postSummary{
			ID:        p.ID,
			Title:     p.Title,
			Slug:      p.Slug,
			Summary:   p.Summary,
			Published: p.Published,
			Tags:      postTagNames(p),
			CreatedAt: formatTime(p.CreatedAt),
			UpdatedAt: formatTime(p.UpdatedAt),
		})
	}
	return jsonResult(out)
}

/* ----------------------------- get_post ----------------------------- */

type getPostInput struct {
	ID int64 `json:"id" jsonschema:"要读取的文章 ID"`
}

type getPostOutput struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Slug      string   `json:"slug"`
	Summary   string   `json:"summary"`
	Content   string   `json:"content"`
	Published bool     `json:"published"`
	Tags      []string `json:"tags"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

func (s *Server) handleGetPost(_ context.Context, _ *sdk.CallToolRequest, in getPostInput) (*sdk.CallToolResult, any, error) {
	if in.ID <= 0 {
		return errResult("id 必须为正整数")
	}

	post, err := s.store.GetPostByID(in.ID)
	if errors.Is(err, store.ErrNotFound) {
		return errResult("文章 %d 不存在", in.ID)
	}
	if err != nil {
		return errResult("读取文章失败：%v", err)
	}

	return jsonResult(getPostOutput{
		ID:        post.ID,
		Title:     post.Title,
		Slug:      post.Slug,
		Summary:   post.Summary,
		Content:   post.Content,
		Published: post.Published,
		Tags:      postTagNames(post),
		CreatedAt: formatTime(post.CreatedAt),
		UpdatedAt: formatTime(post.UpdatedAt),
	})
}

/* ------------------------------ 工具函数 ------------------------------ */

// postTagNames 取出文章标签名。
func postTagNames(p *models.Post) []string {
	names := make([]string, 0, len(p.Tags))
	for _, t := range p.Tags {
		names = append(names, t.Name)
	}
	return names
}

// disallowedSlugRune 匹配 slug 中不允许的字符，与 store.ValidSlug 允许的字符集互补。
var disallowedSlugRune = regexp.MustCompile(`[^\p{Han}\p{Latin}\p{N}_-]+`)

// sanitizeSlug 把 slug 清洗为 store.ValidSlug 能接受的字符集：
// 非法字符（空格、标点、斜杠等）替换为连字符，并去掉首尾分隔符。
func sanitizeSlug(slug string) string {
	out := disallowedSlugRune.ReplaceAllString(strings.TrimSpace(slug), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}

// parseTimeArg 解析可选的 RFC3339 时间参数，空字符串返回零值。
func parseTimeArg(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("需要 RFC3339 格式（如 2026-10-12T09:00:00Z）")
	}
	return t, nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
