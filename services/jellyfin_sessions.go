package services

import (
	"encoding/hex"
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
	// jellyfinSessionLimit 是未过期会话的上限；到达上限后新登录淘汰 last_seen_at 最旧的会话（B-m1）。
	jellyfinSessionLimit = 64
	// jellyfinClientFieldMax 限制落库与诊断里客户端名、设备标识的长度。
	jellyfinClientFieldMax = 128
	// jellyfinUnknownTokenTTL 是未知令牌负缓存的有效期（B-m3）：同一个查不到的令牌在这段时间内
	// 直接判无效，不再回表。
	jellyfinUnknownTokenTTL = 30 * time.Second
	// jellyfinUnknownTokenLimit 是负缓存的容量上限：满了先丢过期的，仍满时丢最早到期的一条。
	jellyfinUnknownTokenLimit = 1024
)

// jellyfinAuthOutcome 是鉴权结论。stopping 与 rejected 分开（B-m4）：服务正在停（Stop 或 Configure
// 停服）时接纳的请求回 503，客户端留着令牌稍后重试；只有令牌本身无效才回 401——客户端遇到 401
// 会丢掉令牌、要求重新输入密码。
type jellyfinAuthOutcome int

const (
	jellyfinAuthRejected jellyfinAuthOutcome = iota
	jellyfinAuthAccepted
	jellyfinAuthStopping
)

// jellyfinSessionStore 是会话表的读写口。默认实现直接读写 jellyfin_sessions；测试替换其中的函数，
// 模拟数据库出错（B-m6）或在查表与写回之间插入注销（B-m3）。
type jellyfinSessionStore struct {
	// create 插入新会话，同时删掉同一设备的旧会话与超出上限的最旧会话，返回被删会话的令牌哈希。
	create func(key [32]byte, deviceID, client string, now time.Time) (jellyfinSession, [][32]byte, error)
	load   func(key [32]byte, now time.Time) (jellyfinSession, bool, error)
	touch  func(key [32]byte, now time.Time) (jellyfinSession, bool, error)
	remove func(key [32]byte) error
}

func defaultJellyfinSessionStore() jellyfinSessionStore {
	return jellyfinSessionStore{create: createJellyfinSession, load: loadJellyfinSession, touch: touchJellyfinSession, remove: deleteJellyfinSession}
}

// jellyfinSessionDB 关掉 SQL 日志：出错时 GORM 会把参数（令牌哈希）插进日志。
func jellyfinSessionDB() *gorm.DB {
	return database.DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
}

func jellyfinTokenHash(key [32]byte) string { return hex.EncodeToString(key[:]) }

// jellyfinTokenKey 是 jellyfinTokenHash 的逆运算；表里的值不是 64 位十六进制时返回 false。
func jellyfinTokenKey(hash string) ([32]byte, bool) {
	var key [32]byte
	raw, err := hex.DecodeString(hash)
	if err != nil || len(raw) != len(key) {
		return key, false
	}
	copy(key[:], raw)
	return key, true
}

// createJellyfinSession 在一个事务里：清掉已过期的行；DeviceId 非空时删掉同一设备的旧会话
// （Jellyfin 语义：同一台设备重新登录顶掉旧令牌，B-m1）；未过期行仍达到上限时按 last_seen_at
// 从旧到新淘汰，腾出一个名额；最后插入新会话。返回被删掉的未过期会话的令牌哈希，调用方据此清掉
// 内存副本。登录由 loginSlot 串行化，计数与插入之间没有别的登录。时间一律按 UTC 落库。
func createJellyfinSession(key [32]byte, deviceID, client string, now time.Time) (jellyfinSession, [][32]byte, error) {
	now = now.UTC()
	row := models.JellyfinSession{TokenHash: jellyfinTokenHash(key), DeviceID: deviceID, Client: client, LastSeenAt: now, ExpiresAt: now.Add(jellyfinSessionTTL)}
	var revoked [][32]byte
	err := jellyfinSessionDB().Transaction(func(tx *gorm.DB) error {
		revoked = revoked[:0]
		if err := tx.Where("expires_at <= ?", now).Delete(&models.JellyfinSession{}).Error; err != nil {
			return err
		}
		drop := func(rows []models.JellyfinSession) error {
			if len(rows) == 0 {
				return nil
			}
			ids := make([]uint, 0, len(rows))
			for _, stale := range rows {
				ids = append(ids, stale.ID)
				if staleKey, ok := jellyfinTokenKey(stale.TokenHash); ok {
					revoked = append(revoked, staleKey)
				}
			}
			return tx.Where("id IN ?", ids).Delete(&models.JellyfinSession{}).Error
		}
		if deviceID != "" {
			var sameDevice []models.JellyfinSession
			if err := tx.Select("id", "token_hash").Where("device_id = ?", deviceID).Find(&sameDevice).Error; err != nil {
				return err
			}
			if err := drop(sameDevice); err != nil {
				return err
			}
		}
		var active int64
		if err := tx.Model(&models.JellyfinSession{}).Where("expires_at > ?", now).Count(&active).Error; err != nil {
			return err
		}
		if excess := int(active) - jellyfinSessionLimit + 1; excess > 0 {
			var oldest []models.JellyfinSession
			if err := tx.Select("id", "token_hash").Where("expires_at > ?", now).
				Order("last_seen_at ASC").Order("id ASC").Limit(excess).Find(&oldest).Error; err != nil {
				return err
			}
			if err := drop(oldest); err != nil {
				return err
			}
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return jellyfinSession{}, nil, err
	}
	return jellyfinSession{expires: row.ExpiresAt, seen: row.LastSeenAt, id: hexSessionID(key)}, revoked, nil
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
// 回 500，而不是把有效令牌当成无效（客户端遇到 401 会丢掉令牌、要求重新输入密码），内存副本不动。
//
// 查表与刷新不持 sessionIO（B-m3）：未命中的请求可以并行回表，不会排在别人的数据库往返后面。
// sessionIO 只串行化「写回内存」与注销 / 淘汰：查表之前记下 revocations，写回时（持 sessionIO）
// 发现它变了，说明查表期间有会话被作废，结论可能已经过时——在锁内重查一次再写回。所以作废之后，
// 无论是写回还是负缓存，都不会让这个令牌重新生效。
func (s *JellyfinServer) authenticate(identity jellyfinIdentity) (jellyfinAuthOutcome, error) {
	now := s.now()
	outcome, settled, epoch := s.cachedSession(identity, now)
	if settled {
		return outcome, nil
	}
	session, found, err := s.lookupSession(identity.token, now)
	if err != nil {
		return jellyfinAuthRejected, err
	}
	s.sessionIO.Lock()
	defer s.sessionIO.Unlock()
	s.mu.Lock()
	raced := s.revocations != epoch
	s.mu.Unlock()
	if raced {
		if session, found, err = s.lookupSession(identity.token, now); err != nil {
			return jellyfinAuthRejected, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.config.JellyfinEnabled || identity.generation != s.generation {
		return jellyfinAuthStopping, nil
	}
	if !found {
		delete(s.sessions, identity.token)
		s.rememberUnknownTokenLocked(identity.token, now)
		return jellyfinAuthRejected, nil
	}
	s.sessions[identity.token] = session
	return jellyfinAuthAccepted, nil
}

// lookupSession 取令牌的会话：先看内存副本，没有再查表；需要刷新时顺延。found=false 表示表里
// 没有这个未过期的会话。不持任何锁做数据库往返。
func (s *JellyfinServer) lookupSession(token [32]byte, now time.Time) (jellyfinSession, bool, error) {
	s.mu.Lock()
	session, cached := s.sessions[token]
	store := s.store
	s.mu.Unlock()
	if !cached {
		loaded, found, err := store.load(token, now)
		if err != nil || !found {
			return jellyfinSession{}, false, err
		}
		session = loaded
	}
	if now.Sub(session.seen) < jellyfinSessionSeenInterval {
		return session, true, nil
	}
	touched, ok, err := store.touch(token, now)
	if err != nil || !ok {
		// 出错时 found=false 由 err 兜住（调用方回 500、内存不动）；行已不在或已过期时以表为准。
		return jellyfinSession{}, false, err
	}
	return touched, true, nil
}

// cachedSession 只看内存：settled=false 表示需要查表或刷新，epoch 是此刻的 revocations。
func (s *JellyfinServer) cachedSession(identity jellyfinIdentity, now time.Time) (outcome jellyfinAuthOutcome, settled bool, epoch uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	epoch = s.revocations
	if !s.config.JellyfinEnabled || identity.generation != s.generation {
		return jellyfinAuthStopping, true, epoch
	}
	session, ok := s.sessions[identity.token]
	if !ok {
		if s.unknownTokenLocked(identity.token, now) {
			return jellyfinAuthRejected, true, epoch
		}
		return jellyfinAuthRejected, false, epoch
	}
	if !now.Before(session.expires) {
		delete(s.sessions, identity.token)
		return jellyfinAuthRejected, true, epoch
	}
	if now.Sub(session.seen) < jellyfinSessionSeenInterval {
		return jellyfinAuthAccepted, true, epoch
	}
	return jellyfinAuthRejected, false, epoch
}

// unknownTokenLocked 报告令牌是否在负缓存里且未到期；到期的顺手删掉。调用方持有 mu。
func (s *JellyfinServer) unknownTokenLocked(token [32]byte, now time.Time) bool {
	until, ok := s.unknownTokens[token]
	if !ok {
		return false
	}
	if now.Before(until) {
		return true
	}
	delete(s.unknownTokens, token)
	return false
}

// rememberUnknownTokenLocked 把查不到的令牌放进负缓存（有界）。调用方持有 mu。
func (s *JellyfinServer) rememberUnknownTokenLocked(token [32]byte, now time.Time) {
	if s.unknownTokens == nil {
		s.unknownTokens = make(map[[32]byte]time.Time)
	}
	if _, exists := s.unknownTokens[token]; !exists && len(s.unknownTokens) >= jellyfinUnknownTokenLimit {
		var oldestKey [32]byte
		var oldest time.Time
		found := false
		for key, until := range s.unknownTokens {
			if !now.Before(until) {
				delete(s.unknownTokens, key)
				continue
			}
			if !found || until.Before(oldest) {
				oldestKey, oldest, found = key, until, true
			}
		}
		if len(s.unknownTokens) >= jellyfinUnknownTokenLimit && found {
			delete(s.unknownTokens, oldestKey)
		}
	}
	s.unknownTokens[token] = now.Add(jellyfinUnknownTokenTTL)
}

// forgetSessions 在表里的行已经删掉之后（登录顶掉同设备旧会话、淘汰最旧会话）清掉它们的内存副本，
// 并推进 revocations，让查表期间拿到旧结论的鉴权在写回前重查。
func (s *JellyfinServer) forgetSessions(keys [][32]byte) {
	if len(keys) == 0 {
		return
	}
	s.sessionIO.Lock()
	defer s.sessionIO.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		delete(s.sessions, key)
	}
	s.revocations++
}

// logout 先删表里的行，删成功才清内存：删不掉时令牌在重启后仍会生效，必须如实报错，内存副本也
// 留着（与表一致）。删行与清内存都在 sessionIO 里，与鉴权的写回互斥。
func (s *JellyfinServer) logout(identity jellyfinIdentity) error {
	s.sessionIO.Lock()
	defer s.sessionIO.Unlock()
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	if err := store.remove(identity.token); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.sessions, identity.token)
	s.revocations++
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
