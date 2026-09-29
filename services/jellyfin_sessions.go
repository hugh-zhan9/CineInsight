package services

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"video-master/database"
	"video-master/models"
)

// Jellyfin 会话持久化（D-PC47、详细设计 §8.8）。jellyfin_sessions 是会话的真值来源，内存表只是
// 它的缓存：鉴权先查内存，未命中再按令牌哈希查表。表里只存 hex(sha256(token))，明文令牌只在登录
// 响应里出现一次。Stop()（应用退出、进入维护模式）只清内存缓存，重启后令牌照样有效；用户在设置里
// 关闭服务、改账号或改配置（Configure）时删除全部行。
const (
	// jellyfinSessionTTL 是滑动过期窗口：最近一次使用后 30 天失效。
	jellyfinSessionTTL = 30 * 24 * time.Hour
	// jellyfinSessionSeenInterval：last_seen_at（连同 expires_at）每个会话最多每 10 分钟刷新一次。
	jellyfinSessionSeenInterval = 10 * time.Minute
	// jellyfinSessionLimit 是未过期会话的上限；到达上限后新登录返回 429。
	jellyfinSessionLimit = 64
	// jellyfinClientFieldMax 限制落库与诊断里客户端名、设备标识的长度。
	jellyfinClientFieldMax = 128
)

var errJellyfinSessionLimit = errors.New("jellyfin_session_limit")

// jellyfinSessionDB 关掉 SQL 日志：出错时 GORM 会把参数（令牌哈希）插进日志。
func jellyfinSessionDB() *gorm.DB {
	return database.DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
}

func jellyfinTokenHash(key [32]byte) string { return hex.EncodeToString(key[:]) }

// createJellyfinSession 先清掉已过期的行，再按未过期行数判上限，最后插入新会话。登录由 loginSlot
// 串行化，计数与插入之间没有别的写入方。时间一律按 UTC 落库，两个后端的比较口径一致。
func createJellyfinSession(key [32]byte, deviceID, client string, now time.Time) (jellyfinSession, error) {
	db := jellyfinSessionDB()
	now = now.UTC()
	if err := db.Where("expires_at <= ?", now).Delete(&models.JellyfinSession{}).Error; err != nil {
		return jellyfinSession{}, err
	}
	var active int64
	if err := db.Model(&models.JellyfinSession{}).Where("expires_at > ?", now).Count(&active).Error; err != nil {
		return jellyfinSession{}, err
	}
	if active >= jellyfinSessionLimit {
		return jellyfinSession{}, errJellyfinSessionLimit
	}
	row := models.JellyfinSession{TokenHash: jellyfinTokenHash(key), DeviceID: deviceID, Client: client, LastSeenAt: now, ExpiresAt: now.Add(jellyfinSessionTTL)}
	if err := db.Create(&row).Error; err != nil {
		return jellyfinSession{}, err
	}
	return jellyfinSession{expires: row.ExpiresAt, seen: row.LastSeenAt, id: hexSessionID(key)}, nil
}

// loadJellyfinSession 按令牌哈希取未过期的会话。
func loadJellyfinSession(key [32]byte, now time.Time) (jellyfinSession, bool, error) {
	var rows []models.JellyfinSession
	if err := jellyfinSessionDB().Where("token_hash = ? AND expires_at > ?", jellyfinTokenHash(key), now.UTC()).Limit(1).Find(&rows).Error; err != nil {
		return jellyfinSession{}, false, err
	}
	if len(rows) == 0 {
		return jellyfinSession{}, false, nil
	}
	return jellyfinSession{expires: rows[0].ExpiresAt, seen: rows[0].LastSeenAt, id: hexSessionID(key)}, true, nil
}

// touchJellyfinSession 顺延一个仍有效的会话；行已被删除或已过期时返回 false。
func touchJellyfinSession(key [32]byte, now time.Time) (jellyfinSession, bool, error) {
	now = now.UTC()
	expires := now.Add(jellyfinSessionTTL)
	result := jellyfinSessionDB().Model(&models.JellyfinSession{}).
		Where("token_hash = ? AND expires_at > ?", jellyfinTokenHash(key), now).
		Updates(map[string]interface{}{"last_seen_at": now, "expires_at": expires})
	if result.Error != nil {
		return jellyfinSession{}, false, result.Error
	}
	return jellyfinSession{expires: expires, seen: now, id: hexSessionID(key)}, result.RowsAffected == 1, nil
}

func deleteJellyfinSession(key [32]byte) error {
	return jellyfinSessionDB().Where("token_hash = ?", jellyfinTokenHash(key)).Delete(&models.JellyfinSession{}).Error
}

// deleteAllJellyfinSessionsTx 作废全部会话；Configure 把它与设置写回放在同一个事务里。
func deleteAllJellyfinSessionsTx(tx *gorm.DB) error {
	return tx.Where("1 = 1").Delete(&models.JellyfinSession{}).Error
}

// authenticate 是 Handler 的准入校验。内存命中且不需要刷新时不碰数据库；未命中（例如重启之后）
// 按哈希查表并放进内存；距上次刷新满 10 分钟时顺延过期时间。数据库出错时返回 error，由调用方
// 回 500，而不是把有效令牌当成无效（客户端遇到 401 会丢掉令牌、要求重新输入密码）。
func (s *JellyfinServer) authenticate(identity jellyfinIdentity) (bool, error) {
	now := s.now()
	if valid, settled := s.cachedSession(identity, now); settled {
		return valid, nil
	}
	// 数据库这一段串行化：注销删行与这里的「查表 → 放进内存」不会交错，已注销的令牌不会被放回内存。
	s.sessionIO.Lock()
	defer s.sessionIO.Unlock()
	if valid, settled := s.cachedSession(identity, now); settled {
		return valid, nil
	}
	s.mu.Lock()
	session, cached := s.sessions[identity.token]
	s.mu.Unlock()
	if !cached {
		loaded, found, err := loadJellyfinSession(identity.token, now)
		if err != nil || !found {
			return false, err
		}
		session = loaded
	}
	if now.Sub(session.seen) >= jellyfinSessionSeenInterval {
		touched, ok, err := touchJellyfinSession(identity.token, now)
		if err != nil {
			return false, err
		}
		if !ok {
			// 行已不在或已过期：以表为准，内存里的副本一并作废。
			s.mu.Lock()
			delete(s.sessions, identity.token)
			s.mu.Unlock()
			return false, nil
		}
		session = touched
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.config.JellyfinEnabled || identity.generation != s.generation {
		return false, nil
	}
	s.sessions[identity.token] = session
	return true, nil
}

// cachedSession 只看内存：settled=false 表示需要查表或刷新。
func (s *JellyfinServer) cachedSession(identity jellyfinIdentity, now time.Time) (valid bool, settled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.config.JellyfinEnabled || identity.generation != s.generation {
		return false, true
	}
	session, ok := s.sessions[identity.token]
	if !ok {
		return false, false
	}
	if !now.Before(session.expires) {
		delete(s.sessions, identity.token)
		return false, true
	}
	if now.Sub(session.seen) < jellyfinSessionSeenInterval {
		return true, true
	}
	return false, false
}

// logout 先删表里的行，删成功才清内存：删不掉时令牌在重启后仍会生效，必须如实报错。
func (s *JellyfinServer) logout(identity jellyfinIdentity) error {
	s.sessionIO.Lock()
	defer s.sessionIO.Unlock()
	if err := deleteJellyfinSession(identity.token); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.sessions, identity.token)
	s.mu.Unlock()
	return nil
}

// jellyfinClientInfo 取 MediaBrowser 授权头里的 DeviceId 与 Client（缺失时看 X-Emby-Device-Id /
// X-Emby-Client），去掉控制字符并截断。它们只用于会话记录、观看去重与诊断，不参与鉴权。
func jellyfinClientInfo(r *http.Request) (deviceID, client string) {
	deviceID = jellyfinAuthField(r, "DeviceId")
	if deviceID == "" {
		deviceID = strings.TrimSpace(r.Header.Get("X-Emby-Device-Id"))
	}
	client = jellyfinAuthField(r, "Client")
	if client == "" {
		client = strings.TrimSpace(r.Header.Get("X-Emby-Client"))
	}
	clean := func(value string) string {
		if value == "" {
			return ""
		}
		return jellyfinLogSafe(value, jellyfinClientFieldMax)
	}
	return clean(deviceID), clean(client)
}
