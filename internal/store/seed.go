package store

import (
	"fmt"
	"strings"

	"github.com/tmiqpl/blog/internal/markdown"
)

type seedPost struct {
	Title   string
	Slug    string
	Date    string
	Tags    []string
	Content string
}

// seed 仅在文章表为空时写入示例内容，方便首次启动即可看到完整站点。
func (s *Store) seed() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&n); err != nil {
		return fmt.Errorf("检查种子数据失败: %w", err)
	}
	if n > 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tagIDs := map[string]int64{}
	tagID := func(name string) (int64, error) {
		if id, ok := tagIDs[name]; ok {
			return id, nil
		}
		slug := Slugify(name)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO tags(name, slug) VALUES(?, ?)`, name, slug); err != nil {
			return 0, err
		}
		var id int64
		if err := tx.QueryRow(`SELECT id FROM tags WHERE slug = ?`, slug).Scan(&id); err != nil {
			return 0, err
		}
		tagIDs[name] = id
		return id, nil
	}

	for _, sp := range seedPosts {
		created := sp.Date + "T09:00:00Z"
		res, err := tx.Exec(
			`INSERT INTO posts(title, slug, summary, content, published, created_at, updated_at)
			 VALUES(?, ?, ?, ?, 1, ?, ?)`,
			sp.Title, sp.Slug, markdown.Excerpt(sp.Content, 120), sp.Content, created, created)
		if err != nil {
			return fmt.Errorf("写入示例文章失败: %w", err)
		}
		postID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, tagName := range sp.Tags {
			id, err := tagID(tagName)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO post_tags(post_id, tag_id) VALUES(?, ?)`, postID, id); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// Slugify 生成 URL 友好的标签 slug：
// 纯 ASCII 名称转小写并用连字符连接；含中文等非 ASCII 字符时直接使用原名
// （tags.name 本身有 UNIQUE 约束，可保证唯一，浏览器也能正常显示中文 URL）。
func Slugify(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	ascii := true
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r - 'A' + 'a')
		case r == ' ' || r == '_' || r == '-':
			b.WriteByte('-')
		default:
			ascii = false
		}
	}
	slug := strings.Trim(b.String(), "-")
	if ascii && slug != "" {
		return slug
	}
	return name
}

var seedPosts = []seedPost{
	{
		Title: "用 Go 从零搭建一个博客系统",
		Slug:  "build-blog-with-go",
		Date:  "2026-08-18",
		Tags:  []string{"Golang", "后端"},
		Content: "# 用 Go 从零搭建一个博客系统\n\n" +
			"Go 的标准库 `net/http` 已经足够强大，配合 `html/template` 就能搭建一个不依赖任何重型框架的博客。\n\n" +
			"## 为什么选择标准库\n\n" +
			"- **零依赖启动快**：编译出来就是一个二进制文件，扔到服务器上就能跑。\n" +
			"- **性能足够**：标准库的 HTTP 服务在绝大多数博客场景下绰绰有余。\n" +
			"- **可控性高**：没有黑盒，每一层都能看得清清楚楚。\n\n" +
			"## 项目结构\n\n" +
			"```text\n" +
			"blog/\n" +
			"├── main.go\n" +
			"├── internal/\n" +
			"│   ├── models/    # 数据模型\n" +
			"│   ├── store/     # 数据库访问\n" +
			"│   ├── markdown/  # Markdown 渲染\n" +
			"│   └── web/       # 路由与处理器\n" +
			"├── templates/     # HTML 模板\n" +
			"└── static/        # 静态资源\n" +
			"```\n\n" +
			"## 路由设计\n\n" +
			"| 路径 | 说明 |\n" +
			"| --- | --- |\n" +
			"| `/` | 文章列表（分页） |\n" +
			"| `/post/{slug}` | 文章详情 |\n" +
			"| `/tags` | 全部标签 |\n" +
			"| `/tag/{slug}` | 标签下的文章 |\n" +
			"| `/about` | 关于页面 |\n\n" +
			"## 小结\n\n" +
			"> 简单的架构不一定弱，够用就好。\n\n" +
			"把复杂度留给真正需要的地方，剩下的交给标准库。\n",
	},
	{
		Title: "SQLite 在生产环境中的正确用法",
		Slug:  "sqlite-in-production",
		Date:  "2026-08-05",
		Tags:  []string{"数据库", "SQLite"},
		Content: "# SQLite 在生产环境中的正确用法\n\n" +
			"很多人觉得 SQLite 只能用来做玩具项目，其实不然。它被部署在数以亿计的设备上，可靠性毋庸置疑。\n\n" +
			"## 关键配置\n\n" +
			"### 1. 开启 WAL 模式\n\n" +
			"```sql\nPRAGMA journal_mode = WAL;\n```\n\n" +
			"WAL（Write-Ahead Logging）允许读写并发，大幅提升多连接场景下的吞吐。\n\n" +
			"### 2. 限制写连接数\n\n" +
			"```go\ndb.SetMaxOpenConns(1)\n```\n\n" +
			"SQLite 同一时刻只允许一个写事务。把连接池限制为 1，可以让 Go 的 `database/sql` 帮你排队，避免 `SQLITE_BUSY`。\n\n" +
			"### 3. 设置忙等待超时\n\n" +
			"```sql\nPRAGMA busy_timeout = 5000;\n```\n\n" +
			"遇到锁时最多等待 5 秒再报错，而不是立即失败。\n\n" +
			"## 什么时候不适合\n\n" +
			"- 写入 QPS 极高（每秒数千次写入）\n" +
			"- 需要多台机器同时写入同一份数据\n\n" +
			"除了这些场景，SQLite 往往是更省心的选择。\n\n" +
			"## 参考清单\n\n" +
			"- [x] 开启 WAL\n" +
			"- [x] 设置 busy_timeout\n" +
			"- [x] 限制写连接为 1\n" +
			"- [x] 定期执行 `VACUUM` 回收空间\n" +
			"- [ ] 配置备份策略\n",
	},
	{
		Title: "Markdown 渲染的工程实践",
		Slug:  "markdown-rendering-practice",
		Date:  "2026-07-22",
		Tags:  []string{"Golang", "Markdown"},
		Content: "# Markdown 渲染的工程实践\n\n" +
			"在服务端渲染 Markdown，比在浏览器里用 JavaScript 渲染更可靠——首屏直出，对 SEO 也友好。\n\n" +
			"## 选择 goldmark\n\n" +
			"Go 生态里 Markdown 库不少，`goldmark` 的优势在于：\n\n" +
			"1. **完全兼容 CommonMark 规范**\n" +
			"2. **扩展机制清晰**，GFM 表格、任务列表都能按需开启\n" +
			"3. **性能优秀**，纯 Go 实现，无 CGO 依赖\n\n" +
			"## 基础用法\n\n" +
			"```go\n" +
			"md := goldmark.New(\n" +
			"    goldmark.WithExtensions(extension.GFM),\n" +
			"    goldmark.WithParserOptions(parser.WithAutoHeadingID()),\n" +
			"    goldmark.WithRendererOptions(html.WithUnsafe()),\n" +
			")\n\n" +
			"var buf bytes.Buffer\n" +
			"if err := md.Convert([]byte(source), &buf); err != nil {\n" +
			"    log.Fatal(err)\n" +
			"}\n" +
			"fmt.Println(buf.String())\n" +
			"```\n\n" +
			"## 几个容易踩的坑\n\n" +
			"### 硬换行\n\n" +
			"中文写作习惯是「一段一行」，如果不开启 `WithHardWraps()`，段落内的换行会被合并成空格。\n\n" +
			"### 安全性\n\n" +
			"`WithUnsafe()` 会放行原始 HTML。如果内容来自不可信的用户，务必配合 `bluemonday` 之类的库做白名单过滤。\n\n" +
			"### 摘要提取\n\n" +
			"列表页不需要完整渲染，把 Markdown 转成纯文本再截断即可，性能好很多。\n\n" +
			"## 性能对比\n\n" +
			"| 方案 | 首次渲染 | 缓存后 |\n" +
			"| --- | ---: | ---: |\n" +
			"| 服务端渲染 | 约 1.2ms | 约 0.02ms |\n" +
			"| 客户端渲染 | 约 80ms | 约 40ms |\n\n" +
			"数据仅供参考，但趋势很明显。\n",
	},
	{
		Title: "关于代码可读性的一点思考",
		Slug:  "on-code-readability",
		Date:  "2026-07-03",
		Tags:  []string{"随笔", "工程"},
		Content: "# 关于代码可读性的一点思考\n\n" +
			"代码是写给人看的，只是顺便能在机器上运行。\n\n" +
			"## 命名的力量\n\n" +
			"一个糟糕的变量名，能让读者停顿十秒钟。十次停顿就是一分钟，一百次就是十分钟。\n\n" +
			"```go\n" +
			"// 不好\n" +
			"func proc(d []byte, f bool) error { /* ... */ }\n\n" +
			"// 好\n" +
			"func publishPost(content []byte, notifySubscribers bool) error { /* ... */ }\n" +
			"```\n\n" +
			"## 注释写什么\n\n" +
			"注释应该解释 **为什么**，而不是 **是什么**。代码本身已经说明了「是什么」。\n\n" +
			"```go\n" +
			"// 不好：把 i 加一\n" +
			"i++\n\n" +
			"// 好：跳过表头行\n" +
			"i++\n" +
			"```\n\n" +
			"## 函数的长度\n\n" +
			"没有硬性标准，但有一个经验法则：\n\n" +
			"> 如果这个函数需要滚动三次才能看完，那它大概率该拆了。\n\n" +
			"## 最后\n\n" +
			"追求可读性不是追求「优雅」。优雅是主观的，可读性是客观的——**别人能不能快速看懂**，这是唯一的评判标准。\n",
	},
	{
		Title: "静态资源优化的几个小技巧",
		Slug:  "static-assets-optimization",
		Date:  "2026-06-15",
		Tags:  []string{"前端", "性能"},
		Content: "# 静态资源优化的几个小技巧\n\n" +
			"博客的加载速度直接影响阅读体验。下面这些手段成本很低，收益却不小。\n\n" +
			"## 1. 内联关键 CSS\n\n" +
			"首屏需要的样式直接内联进 `<head>`，避免一次额外的网络往返。\n\n" +
			"## 2. 字体用系统栈\n\n" +
			"```css\n" +
			"font-family: -apple-system, BlinkMacSystemFont, \"Segoe UI\",\n" +
			"             \"PingFang SC\", \"Microsoft YaHei\", sans-serif;\n" +
			"```\n\n" +
			"中文字体动辄几 MB，用系统自带的最划算。\n\n" +
			"## 3. 图片延迟加载\n\n" +
			"```html\n" +
			"<img src=\"cover.jpg\" loading=\"lazy\" alt=\"封面\">\n" +
			"```\n\n" +
			"浏览器原生支持，一行属性搞定。\n\n" +
			"## 4. 启用压缩\n\n" +
			"在 Go 里加一层 gzip 中间件，文本资源通常能压到原来的 30% 左右。\n\n" +
			"## 效果对比\n\n" +
			"优化前后，本地 Lighthouse 的分数：\n\n" +
			"- 优化前：**72**\n" +
			"- 优化后：**98**\n\n" +
			"代价是不到 30 行代码。\n\n" +
			"## 小结\n\n" +
			"优化的第一原则是 **先测量，再优化**。不要凭感觉加缓存、加 CDN，先打开开发者工具看看时间到底花在哪了。\n",
	},
}
