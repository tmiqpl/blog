package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// 站点设置项的键名。
const (
	SettingSiteTitle       = "site_title"
	SettingSiteDescription = "site_description"
	SettingSiteAuthor      = "site_author"
	SettingSiteBio         = "site_bio"
	SettingSiteGitHub      = "site_github"
	SettingSiteEmail       = "site_email"
	SettingSiteICP         = "site_icp"
	SettingAdminUsername   = "admin_username"
	SettingAdminPassword   = "admin_password_hash"
	// SettingSiteInitialized 标记站点是否已完成初始化。
	// 该键不存在或值不为 "1" 时，前台与后台都会把访客引导到初始化页面。
	SettingSiteInitialized = "site_initialized"
)

// InitializedValue 是 SettingSiteInitialized 表示「已完成」的取值。
const InitializedValue = "1"

// SettingKeys 是允许通过后台编辑的设置项白名单。
var SettingKeys = []string{
	SettingSiteTitle,
	SettingSiteDescription,
	SettingSiteAuthor,
	SettingSiteBio,
	SettingSiteGitHub,
	SettingSiteEmail,
	SettingSiteICP,
}

// GetSetting 读取单个设置项，不存在时返回空字符串。
func (s *Store) GetSetting(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取设置 %s 失败: %w", key, err)
	}
	return value, nil
}

// IsInitialized 判断站点是否已完成初始化。
//
// 判定依据是 SettingSiteInitialized 标记。之所以不直接用「设置表里有没有内容」，
// 是因为那个条件太脆弱——任何一条默认值写进去就会成立。
//
// 另外还加了一条兼容规则：数据库里已经存在管理员账号时，也视为早已初始化。
// 这样从旧版本升级上来的库（当时还没有这个标记）不会被突然要求重新初始化。
func (s *Store) IsInitialized() (bool, error) {
	v, err := s.GetSetting(SettingSiteInitialized)
	if err != nil {
		return false, err
	}
	if v == InitializedValue {
		return true, nil
	}

	hash, err := s.GetSetting(SettingAdminPassword)
	if err != nil {
		return false, err
	}
	return hash != "", nil
}

// MarkInitialized 把站点标记为已初始化。
func (s *Store) MarkInitialized() error {
	return s.SetSetting(SettingSiteInitialized, InitializedValue)
}

// Settings 一次性读取全部设置项。
func (s *Store) Settings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("读取设置失败: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string, 16)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSetting 写入单个设置项。
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("保存设置 %s 失败: %w", key, err)
	}
	return nil
}

// SetSettings 在一个事务中批量写入设置项。
func (s *Store) SetSettings(values map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for k, v := range values {
		if _, err := tx.Exec(
			`INSERT INTO settings(key, value) VALUES(?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return fmt.Errorf("保存设置 %s 失败: %w", k, err)
		}
	}
	return tx.Commit()
}

// SeedSettings 仅在键不存在时写入默认值，不会覆盖已有配置。
func (s *Store) SeedSettings(defaults map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for k, v := range defaults {
		if _, err := tx.Exec(
			`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}
