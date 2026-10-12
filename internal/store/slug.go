package store

import "regexp"

// slugPattern 限制别名只包含中文、字母、数字、连字符与下划线。
var slugPattern = regexp.MustCompile(`^[\p{Han}\p{Latin}\p{N}_-]+$`)

// ValidSlug 校验文章别名：非空、不超过 120 个字符，且字符集受限。
func ValidSlug(slug string) bool {
	return slug != "" && len([]rune(slug)) <= 120 && slugPattern.MatchString(slug)
}
