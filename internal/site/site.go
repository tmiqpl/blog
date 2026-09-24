// Package site 定义站点元信息，并负责从数据库设置中加载（带回退默认值）。
package site

import (
	"github.com/tmiqpl/blog/internal/store"
)

// Info 描述站点的基础信息。
type Info struct {
	Title       string
	Description string
	Author      string
	Bio         string
	GitHub      string
	Email       string
	ICP         string
}

// Load 从数据库设置中读取站点信息，缺失项回退到 defaults。
func Load(st *store.Store, defaults Info) Info {
	values, err := st.Settings()
	if err != nil {
		return defaults
	}

	out := defaults
	pick := func(key, current string) string {
		if v, ok := values[key]; ok && v != "" {
			return v
		}
		return current
	}

	out.Title = pick(store.SettingSiteTitle, out.Title)
	out.Description = pick(store.SettingSiteDescription, out.Description)
	out.Author = pick(store.SettingSiteAuthor, out.Author)
	out.Bio = pick(store.SettingSiteBio, out.Bio)
	out.GitHub = pick(store.SettingSiteGitHub, out.GitHub)
	out.Email = pick(store.SettingSiteEmail, out.Email)
	out.ICP = pick(store.SettingSiteICP, out.ICP)

	return out
}

// ToSettings 把站点信息转换为设置表的键值对。
func (i Info) ToSettings() map[string]string {
	return map[string]string{
		store.SettingSiteTitle:       i.Title,
		store.SettingSiteDescription: i.Description,
		store.SettingSiteAuthor:      i.Author,
		store.SettingSiteBio:         i.Bio,
		store.SettingSiteGitHub:      i.GitHub,
		store.SettingSiteEmail:       i.Email,
		store.SettingSiteICP:         i.ICP,
	}
}

// FromForm 从表单字段构造 Info，字段名与后台设置页保持一致。
func FromForm(get func(string) string) Info {
	return Info{
		Title:       get("site_title"),
		Description: get("site_description"),
		Author:      get("site_author"),
		Bio:         get("site_bio"),
		GitHub:      get("site_github"),
		Email:       get("site_email"),
		ICP:         get("site_icp"),
	}
}
