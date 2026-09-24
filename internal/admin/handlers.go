package admin

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tmiqpl/blog/internal/auth"
	"github.com/tmiqpl/blog/internal/captcha"
	"github.com/tmiqpl/blog/internal/markdown"
	"github.com/tmiqpl/blog/internal/models"
	"github.com/tmiqpl/blog/internal/site"
	"github.com/tmiqpl/blog/internal/store"
)

/* ------------------------------ 登录 ------------------------------ */

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.renderLogin(w, r, http.StatusOK, safeNext(r.URL.Query().Get("next")), "")
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, status int, next, errMsg string) {
	data := loginData{
		baseData: baseData{
			Title:     "登录",
			CSRF:      issueLoginCSRF(w, r),
			Flash:     popFlash(w, r),
			SiteTitle: s.siteInfo().Title,
			Year:      time.Now().Year(),
			Dev:       s.dev,
		},
		Error:       errMsg,
		Next:        next,
		CaptchaMode: s.captchaMode,
	}

	switch s.captchaMode {
	case CaptchaModeImage:
		data.CaptchaURL = captchaURL()
	case CaptchaModeSlider:
		opts := s.slider.Options()
		data.Slider = &sliderConfig{
			Width:     opts.Width,
			Height:    opts.Height,
			PieceSize: opts.PieceSize,
		}
	}

	s.render(w, r, status, "login", data)
}

// captchaURL 生成带随机参数的验证码地址。
// 每次渲染都换一个参数，避免浏览器直接复用上一张已作废的图片。
func captchaURL() string {
	return fmt.Sprintf("%s/captcha?t=%s", BasePath, strconv.FormatInt(time.Now().UnixNano(), 36))
}

// handleCaptchaImage 生成并返回一张字符验证码图片，同时把挑战标识写进 Cookie。
func (s *Server) handleCaptchaImage(w http.ResponseWriter, r *http.Request) {
	if s.captcha == nil {
		http.NotFound(w, r)
		return
	}

	challenge, err := s.captcha.Generate()
	if err != nil {
		s.logger.Error("生成验证码失败", "error", err)
		http.Error(w, "验证码生成失败，请刷新重试", http.StatusInternalServerError)
		return
	}

	setCaptchaCookie(w, r, challenge.ID, s.captcha.TTL())

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(challenge.Image)
}

/* --------------------------- 滑块拼图 --------------------------- */

// sliderChallengeResponse 是一次滑块挑战的下发内容。
// 背景图与拼图块以 data URL 内联，省去两次额外请求，也避免图片被单独缓存。
type sliderChallengeResponse struct {
	Token      string `json:"token"`
	Background string `json:"background"`
	Piece      string `json:"piece"`
	Y          int    `json:"y"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	PieceSize  int    `json:"pieceSize"`
}

type sliderVerifyRequest struct {
	Token      string `json:"token"`
	X          int    `json:"x"`
	DurationMS int    `json:"durationMs"`
	Track      []int  `json:"track"`
}

func (s *Server) handleSliderChallenge(w http.ResponseWriter, r *http.Request) {
	if s.slider == nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")

	challenge, err := s.slider.Generate()
	if err != nil {
		s.logger.Error("生成滑块验证码失败", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "验证码生成失败，请刷新重试",
		})
		return
	}

	writeJSON(w, http.StatusOK, sliderChallengeResponse{
		Token:      challenge.Token,
		Background: dataURL(captcha.BackgroundMIME, challenge.Background),
		Piece:      dataURL(captcha.PieceMIME, challenge.Piece),
		Y:          challenge.Y,
		Width:      challenge.Width,
		Height:     challenge.Height,
		PieceSize:  challenge.PieceSize,
	})
}

func (s *Server) handleSliderVerify(w http.ResponseWriter, r *http.Request) {
	if s.slider == nil {
		http.NotFound(w, r)
		return
	}

	if !checkLoginCSRF(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "会话已过期，请刷新页面重试"})
		return
	}

	// 单独限流：滑块失败不计入登录失败次数，否则手滑几次就会把账号锁死
	key := clientKey(r)
	s.sliderLimiter.Fail(key)
	if allowed, _ := s.sliderLimiter.Allowed(key); !allowed {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": "操作过于频繁，请稍后再试",
		})
		return
	}

	var req sliderVerifyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
		return
	}

	ticket, err := s.slider.Verify(req.Token, req.X, captcha.Behavior{
		DurationMS: req.DurationMS,
		Track:      req.Track,
	}, key)
	if err != nil {
		s.logger.Warn("滑块校验失败", "remote", key, "reason", err)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"code":  sliderErrorCode(err),
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ticket": ticket})
}

// sliderErrorCode 把错误转成前端可判断的标识。
// expired 表示挑战已作废、必须换一张；其余情况用户可以在同一张图上再试。
func sliderErrorCode(err error) string {
	switch {
	case errors.Is(err, captcha.ErrChallengeNotFound):
		return "expired"
	case errors.Is(err, captcha.ErrPositionMismatch):
		return "position"
	case errors.Is(err, captcha.ErrBehaviorSuspicious):
		return "behavior"
	default:
		return "unknown"
	}
}

// dataURL 把图片字节编码为可直接放进 <img src> 的 data URL。
func dataURL(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, r, http.StatusBadRequest, BasePath, "表单解析失败，请重试")
		return
	}

	if !checkLoginCSRF(r) {
		s.renderLogin(w, r, http.StatusBadRequest, BasePath, "表单已过期，请重新提交")
		return
	}

	key := clientKey(r)
	if allowed, wait := s.limiter.Allowed(key); !allowed {
		minutes := int(wait.Minutes()) + 1
		s.renderLogin(w, r, http.StatusTooManyRequests, BasePath,
			fmt.Sprintf("尝试次数过多，请 %d 分钟后再试", minutes))
		return
	}

	next := safeNext(r.PostFormValue("next"))

	// 人机校验在密码校验之前，失败同样计入登录限流
	switch s.captchaMode {
	case CaptchaModeSlider:
		if !s.slider.RedeemTicket(r.PostFormValue("captcha_ticket"), key) {
			s.limiter.Fail(key)
			s.logger.Warn("滑块票据无效", "remote", key)
			s.renderLogin(w, r, http.StatusUnauthorized, next, "请先完成滑块拼图验证")
			return
		}

	case CaptchaModeImage:
		id := captchaIDFromRequest(r)
		answer := r.PostFormValue("captcha")

		clearCaptchaCookie(w, r) // 无论结果如何，当前验证码都已作废

		if !s.captcha.Verify(id, answer) {
			s.limiter.Fail(key)
			s.logger.Warn("验证码校验失败", "remote", key)
			s.renderLogin(w, r, http.StatusUnauthorized, next, "验证码不正确，请重新输入")
			return
		}
	}

	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")

	storedUser, err := s.store.GetSetting(store.SettingAdminUsername)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "登录失败", "读取账号信息失败")
		return
	}
	storedHash, err := s.store.GetSetting(store.SettingAdminPassword)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "登录失败", "读取账号信息失败")
		return
	}

	// 两个条件都要计算，避免通过响应时间判断用户名是否存在
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(storedUser)) == 1
	passOK := auth.VerifyPassword(storedHash, password)

	if !userOK || !passOK {
		s.limiter.Fail(key)
		s.logger.Warn("后台登录失败", "username", username, "remote", key)
		s.renderLogin(w, r, http.StatusUnauthorized, next, "用户名或密码不正确")
		return
	}

	s.limiter.Reset(key)
	sess, err := s.auth.Create(username)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "登录失败", "创建会话失败，请重试")
		return
	}

	s.setSessionCookie(w, r, sess)
	s.logger.Info("后台登录成功", "username", username, "remote", key)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFrom(r)
	if ok && !validCSRF(r, sess) {
		http.Error(w, "CSRF 校验失败，请刷新页面后重试", http.StatusForbidden)
		return
	}
	if ok {
		s.auth.Destroy(sess.ID)
	}
	s.clearSessionCookie(w, r)
	setFlash(w, r, "success", "已安全退出")
	http.Redirect(w, r, BasePath+"/login", http.StatusSeeOther)
}

/* ----------------------------- 仪表盘 ----------------------------- */

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	stats, err := s.store.Stats()
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载失败", "统计数据读取失败")
		return
	}

	recent, _, err := s.store.ListPostsAdmin(store.PostFilter{}, 5, 0)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载失败", "文章列表读取失败")
		return
	}

	drafts, _, err := s.store.ListPostsAdmin(store.PostFilter{Status: "draft"}, 5, 0)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载失败", "草稿列表读取失败")
		return
	}

	pct := 0
	if stats.TotalPosts > 0 {
		pct = stats.PublishedPosts * 100 / stats.TotalPosts
	}

	data := dashboardData{
		baseData:     s.baseAuthed(w, r, sess, navDashboard, "仪表盘"),
		Stats:        stats,
		RecentPosts:  recent,
		DraftPosts:   drafts,
		TagCount:     stats.TotalTags,
		PublishedPct: pct,
	}
	s.render(w, r, http.StatusOK, "dashboard", data)
}

/* ---------------------------- 文章列表 ---------------------------- */

func (s *Server) handlePostList(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	page := atoiDefault(r.URL.Query().Get("page"), 1)
	filter := store.PostFilter{
		Status:  r.URL.Query().Get("status"),
		Keyword: strings.TrimSpace(r.URL.Query().Get("q")),
	}

	posts, total, err := s.store.ListPostsAdmin(filter, perPage, (page-1)*perPage)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载失败", "文章列表读取失败")
		return
	}

	p := newPagination(page, perPage, total)
	data := postsData{
		baseData: s.baseAuthed(w, r, sess, navPosts, "文章管理"),
		Posts:    posts,
		Page:     p,
		Filter:   filter,
		Total:    total,
		HasPrev:  p.Page > 1,
		HasNext:  p.Page < p.TotalPages,
		PrevURL:  postListURL(filter, p.Page-1),
		NextURL:  postListURL(filter, p.Page+1),
	}
	s.render(w, r, http.StatusOK, "posts", data)
}

func postListURL(f store.PostFilter, page int) string {
	v := url.Values{}
	if f.Status != "" {
		v.Set("status", f.Status)
	}
	if f.Keyword != "" {
		v.Set("q", f.Keyword)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return BasePath + "/posts"
	}
	return BasePath + "/posts?" + v.Encode()
}

/* --------------------------- 文章编辑器 --------------------------- */

func (s *Server) handlePostNew(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	tags, err := s.store.ListTagsAdmin()
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载失败", "标签读取失败")
		return
	}

	data := editorData{
		baseData: s.baseAuthed(w, r, sess, navPosts, "写文章"),
		IsNew:    true,
		Form: postForm{
			Published: true,
			CreatedAt: time.Now().Format("2006-01-02T15:04"),
		},
		AllTags: tagNames(tags),
	}
	s.render(w, r, http.StatusOK, "editor", data)
}

func (s *Server) handlePostEdit(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "参数错误", "文章 ID 无效")
		return
	}

	post, err := s.store.GetPostByID(id)
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, r, http.StatusNotFound, "文章不存在", "该文章可能已被删除")
		return
	}
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载失败", "文章读取失败")
		return
	}

	tags, err := s.store.ListTagsAdmin()
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载失败", "标签读取失败")
		return
	}

	names := make([]string, 0, len(post.Tags))
	for _, t := range post.Tags {
		names = append(names, t.Name)
	}

	data := editorData{
		baseData: s.baseAuthed(w, r, sess, navPosts, "编辑文章"),
		IsNew:    false,
		PostID:   post.ID,
		Form: postForm{
			Title:     post.Title,
			Slug:      post.Slug,
			Summary:   post.Summary,
			Tags:      strings.Join(names, ", "),
			Content:   post.Content,
			Published: post.Published,
			CreatedAt: post.CreatedAt.Local().Format("2006-01-02T15:04"),
		},
		AllTags:   tagNames(tags),
		UpdatedAt: post.UpdatedAt.Local().Format("2006-01-02 15:04"),
	}
	s.render(w, r, http.StatusOK, "editor", data)
}

func (s *Server) handlePostCreate(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	s.savePost(w, r, sess, 0)
}

func (s *Server) handlePostUpdate(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "参数错误", "文章 ID 无效")
		return
	}
	s.savePost(w, r, sess, id)
}

// savePost 处理文章的新建与更新（id 为 0 表示新建）。
func (s *Server) savePost(w http.ResponseWriter, r *http.Request, sess *auth.Session, id int64) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "提交失败", "表单解析失败")
		return
	}

	form := postForm{
		Title:     strings.TrimSpace(r.PostFormValue("title")),
		Slug:      strings.TrimSpace(r.PostFormValue("slug")),
		Summary:   strings.TrimSpace(r.PostFormValue("summary")),
		Tags:      strings.TrimSpace(r.PostFormValue("tags")),
		Content:   r.PostFormValue("content"),
		Published: r.PostFormValue("published") == "on" || r.PostFormValue("published") == "true",
		CreatedAt: strings.TrimSpace(r.PostFormValue("created_at")),
	}

	title := "写文章"
	if id != 0 {
		title = "编辑文章"
	}

	// 校验
	var errs []string
	if form.Title == "" {
		errs = append(errs, "标题不能为空")
	}
	if strings.TrimSpace(form.Content) == "" {
		errs = append(errs, "正文不能为空")
	}

	slug := form.Slug
	if slug == "" {
		slug = store.Slugify(form.Title)
	}
	if !validSlug(slug) {
		errs = append(errs, "别名只能包含中文、字母、数字、连字符和下划线")
	} else if taken, err := s.store.SlugTaken(slug, id); err != nil {
		errs = append(errs, "校验别名失败，请重试")
	} else if taken {
		errs = append(errs, "别名「"+slug+"」已被其他文章占用")
	}

	createdAt, err := parseFormDate(form.CreatedAt)
	if err != nil {
		errs = append(errs, "发布时间格式不正确")
	}

	if len(errs) > 0 {
		s.renderEditorWithErrors(w, r, sess, id, form, errs, title)
		return
	}

	tags := splitTags(form.Tags)
	_, isNew, err := s.store.UpsertPost(store.PostInput{
		Title:     form.Title,
		Slug:      slug,
		Summary:   form.Summary,
		Content:   form.Content,
		Published: form.Published,
		CreatedAt: createdAt,
		Tags:      tags,
	})
	if err != nil {
		s.renderEditorWithErrors(w, r, sess, id, form, []string{"保存失败：" + err.Error()}, title)
		return
	}

	action := "已更新"
	if isNew {
		action = "已发布"
		if !form.Published {
			action = "已保存为草稿"
		}
	}
	s.redirectWithFlash(w, r, BasePath+"/posts", "success", action+"《"+form.Title+"》")
}

func (s *Server) renderEditorWithErrors(w http.ResponseWriter, r *http.Request, sess *auth.Session, id int64, form postForm, errs []string, title string) {
	tags, err := s.store.ListTagsAdmin()
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载失败", "标签读取失败")
		return
	}

	data := editorData{
		baseData: s.baseAuthed(w, r, sess, navPosts, title),
		Form:     form,
		IsNew:    id == 0,
		PostID:   id,
		Errors:   errs,
		AllTags:  tagNames(tags),
	}
	s.render(w, r, http.StatusUnprocessableEntity, "editor", data)
}

func (s *Server) handlePostDelete(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "参数错误", "文章 ID 无效")
		return
	}

	post, err := s.store.GetPostByID(id)
	if errors.Is(err, store.ErrNotFound) {
		s.redirectWithFlash(w, r, BasePath+"/posts", "error", "文章不存在或已被删除")
		return
	}
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "删除失败", "文章读取失败")
		return
	}

	if err := s.store.DeletePostByID(id); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "删除失败", "数据库操作失败")
		return
	}

	s.logger.Info("删除文章", "id", id, "title", post.Title, "by", sess.Username)
	s.redirectWithFlash(w, r, BasePath+"/posts", "success", "已删除《"+post.Title+"》")
}

func (s *Server) handlePostToggle(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "参数错误", "文章 ID 无效")
		return
	}

	post, err := s.store.GetPostByID(id)
	if errors.Is(err, store.ErrNotFound) {
		s.redirectWithFlash(w, r, BasePath+"/posts", "error", "文章不存在或已被删除")
		return
	}
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "操作失败", "文章读取失败")
		return
	}

	if err := s.store.SetPublished(id, !post.Published); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "操作失败", "状态更新失败")
		return
	}

	msg := "已发布《" + post.Title + "》"
	if post.Published {
		msg = "已转为草稿《" + post.Title + "》"
	}
	s.redirectWithFlash(w, r, postListURL(store.PostFilter{
		Status:  r.URL.Query().Get("status"),
		Keyword: r.URL.Query().Get("q"),
	}, atoiDefault(r.URL.Query().Get("page"), 1)), "success", msg)
}

/* --------------------------- Markdown 预览 --------------------------- */

type previewRequest struct {
	Markdown string `json:"markdown"`
}

type previewResponse struct {
	HTML  string `json:"html"`
	Words int    `json:"words"`
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	var req previewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体解析失败"})
		return
	}

	writeJSON(w, http.StatusOK, previewResponse{
		HTML:  markdown.Render(req.Markdown),
		Words: len([]rune(markdown.PlainText(req.Markdown))),
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

/* ---------------------------- 标签管理 ---------------------------- */

func (s *Server) handleTags(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	tags, err := s.store.ListTagsAdmin()
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载失败", "标签读取失败")
		return
	}

	data := tagsData{
		baseData: s.baseAuthed(w, r, sess, navTags, "标签管理"),
		Tags:     tags,
	}
	s.render(w, r, http.StatusOK, "tags", data)
}

func (s *Server) handleTagUpdate(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "参数错误", "标签 ID 无效")
		return
	}

	if err := r.ParseForm(); err != nil {
		s.redirectWithFlash(w, r, BasePath+"/tags", "error", "表单解析失败")
		return
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	slug := strings.TrimSpace(r.PostFormValue("slug"))
	if slug == "" {
		slug = store.Slugify(name)
	}
	if name == "" {
		s.redirectWithFlash(w, r, BasePath+"/tags", "error", "标签名不能为空")
		return
	}

	if err := s.store.RenameTag(id, name, slug); err != nil {
		s.redirectWithFlash(w, r, BasePath+"/tags", "error", "重命名失败："+err.Error())
		return
	}
	s.redirectWithFlash(w, r, BasePath+"/tags", "success", "标签已更新为「"+name+"」")
}

func (s *Server) handleTagDelete(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	id, err := pathID(r, "id")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "参数错误", "标签 ID 无效")
		return
	}

	_ = r.ParseForm()
	removePosts := r.PostFormValue("remove_posts") == "on"

	removed, err := s.store.DeleteTag(id, removePosts)
	if errors.Is(err, store.ErrNotFound) {
		s.redirectWithFlash(w, r, BasePath+"/tags", "error", "标签不存在")
		return
	}
	if err != nil {
		s.redirectWithFlash(w, r, BasePath+"/tags", "error", "删除失败："+err.Error())
		return
	}

	msg := "标签已删除，关联文章已解除该标签"
	if removePosts {
		msg = fmt.Sprintf("标签已删除，同时删除了 %d 篇文章", removed)
	}
	s.logger.Info("删除标签", "id", id, "removed_posts", removed, "by", sess.Username)
	s.redirectWithFlash(w, r, BasePath+"/tags", "success", msg)
}

func (s *Server) handleTagCleanup(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	n, err := s.store.CleanupOrphanTags()
	if err != nil {
		s.redirectWithFlash(w, r, BasePath+"/tags", "error", "清理失败："+err.Error())
		return
	}
	if n == 0 {
		s.redirectWithFlash(w, r, BasePath+"/tags", "info", "没有需要清理的空标签")
		return
	}
	s.redirectWithFlash(w, r, BasePath+"/tags", "success", fmt.Sprintf("已清理 %d 个空标签", n))
}

/* ---------------------------- 站点设置 ---------------------------- */

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	data := settingsData{
		baseData: s.baseAuthed(w, r, sess, navSettings, "站点设置"),
		Form:     s.siteInfo(),
	}
	s.render(w, r, http.StatusOK, "settings", data)
}

func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "保存失败", "表单解析失败")
		return
	}

	info := site.FromForm(func(key string) string {
		return strings.TrimSpace(r.PostFormValue(key))
	})

	var errs []string
	if info.Title == "" {
		errs = append(errs, "站点标题不能为空")
	}
	if info.Email != "" && !strings.Contains(info.Email, "@") {
		errs = append(errs, "邮箱格式不正确")
	}
	for _, link := range []struct{ name, value string }{
		{"GitHub 地址", info.GitHub},
	} {
		if link.value != "" && !strings.HasPrefix(link.value, "http://") && !strings.HasPrefix(link.value, "https://") {
			errs = append(errs, link.name+"需要以 http:// 或 https:// 开头")
		}
	}

	if len(errs) > 0 {
		data := settingsData{
			baseData: s.baseAuthed(w, r, sess, navSettings, "站点设置"),
			Form:     info,
			Errors:   errs,
		}
		s.render(w, r, http.StatusUnprocessableEntity, "settings", data)
		return
	}

	if err := s.store.SetSettings(info.ToSettings()); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "保存失败", "写入数据库失败")
		return
	}

	s.logger.Info("更新站点设置", "by", sess.Username)
	s.redirectWithFlash(w, r, BasePath+"/settings", "success", "站点设置已保存，前台立即生效")
}

/* ---------------------------- 账号设置 ---------------------------- */

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	data := accountData{
		baseData: s.baseAuthed(w, r, sess, navAccount, "账号设置"),
		Username: sess.Username,
	}
	s.render(w, r, http.StatusOK, "account", data)
}

func (s *Server) handleAccountSave(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "保存失败", "表单解析失败")
		return
	}

	renderErr := func(msg string) {
		data := accountData{
			baseData: s.baseAuthed(w, r, sess, navAccount, "账号设置"),
			Error:    msg,
			Username: sess.Username,
		}
		s.render(w, r, http.StatusUnprocessableEntity, "account", data)
	}

	current := r.PostFormValue("current_password")
	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("new_password")
	confirm := r.PostFormValue("confirm_password")

	storedHash, err := s.store.GetSetting(store.SettingAdminPassword)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "保存失败", "读取账号信息失败")
		return
	}
	if !auth.VerifyPassword(storedHash, current) {
		renderErr("当前密码不正确")
		return
	}
	if username == "" {
		renderErr("用户名不能为空")
		return
	}

	values := map[string]string{store.SettingAdminUsername: username}

	passwordChanged := false
	if password != "" {
		if len([]rune(password)) < 8 {
			renderErr("新密码至少需要 8 个字符")
			return
		}
		if password != confirm {
			renderErr("两次输入的新密码不一致")
			return
		}
		encoded, err := auth.HashPassword(password)
		if err != nil {
			s.renderError(w, r, http.StatusInternalServerError, "保存失败", "密码加密失败")
			return
		}
		values[store.SettingAdminPassword] = encoded
		passwordChanged = true
	}

	if err := s.store.SetSettings(values); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "保存失败", "写入数据库失败")
		return
	}

	s.logger.Info("更新后台账号", "by", sess.Username, "password_changed", passwordChanged)

	if passwordChanged {
		// 密码变更后强制所有会话重新登录
		s.auth.DestroyAll()
		s.clearSessionCookie(w, r)
		setFlash(w, r, "success", "密码已更新，请使用新密码重新登录")
		http.Redirect(w, r, BasePath+"/login", http.StatusSeeOther)
		return
	}

	s.redirectWithFlash(w, r, BasePath+"/account", "success", "账号信息已保存")
}

/* ------------------------------ 兜底 ------------------------------ */

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.renderError(w, r, http.StatusNotFound, "页面不存在", "你访问的后台页面不存在。")
}

/* ------------------------------ 工具 ------------------------------ */

var slugPattern = regexp.MustCompile(`^[\p{Han}\p{Latin}\p{N}_-]+$`)

func validSlug(slug string) bool {
	return slug != "" && len([]rune(slug)) <= 120 && slugPattern.MatchString(slug)
}

// splitTags 把逗号分隔的标签字符串拆成切片（中英文逗号、分号均可）。
func splitTags(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || r == '\n'
	})

	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

func tagNames(tags []store.TagUsage) []models.Tag {
	out := make([]models.Tag, 0, len(tags))
	for _, t := range tags {
		out = append(out, t.Tag)
	}
	return out
}

var formDateLayouts = []string{
	"2006-01-02T15:04",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

func parseFormDate(value string) (time.Time, error) {
	if value == "" {
		return time.Now().UTC(), nil
	}
	for _, layout := range formDateLayouts {
		if t, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析时间 %q", value)
}

func atoiDefault(raw string, fallback int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
