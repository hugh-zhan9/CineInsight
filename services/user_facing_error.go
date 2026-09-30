package services

import (
	"regexp"
	"strings"
)

// userFacingError 只替换错误文案，错误链不变：errors.Is / errors.As 仍按原错误判断
// （例如恢复的 restore_committed / restore_fatal 语义）。
type userFacingError struct {
	err error
	msg string
}

func (e *userFacingError) Error() string { return e.msg }
func (e *userFacingError) Unwrap() error { return e.err }

// WithoutAbsolutePaths 把错误文案里的绝对路径擦成 <path>（G-3）：包进来的系统错误（改名、打开文件、
// 外部命令的输出）常带完整路径，直接交给界面就会显示出来。文案里没有路径时原样返回。
func WithoutAbsolutePaths(err error) error {
	if err == nil {
		return nil
	}
	msg := scrubAbsolutePaths(err.Error())
	if msg == err.Error() {
		return err
	}
	return &userFacingError{err: err, msg: msg}
}

// userFacingPathPattern 只认「绝对路径」：斜杠前是开头、空白或常见分隔符，斜杠后至少一个路径字符。
// 「恢复 / 切换」这类带空格的斜杠、「SQLite/PG」这类词中斜杠都不会被当成路径。
var userFacingPathPattern = regexp.MustCompile(`(^|[\s:=(（"'“])(/[^\s'"”，。；）)]+)`)

func scrubAbsolutePaths(text string) string {
	if !strings.Contains(text, "/") {
		return text
	}
	return userFacingPathPattern.ReplaceAllStringFunc(text, func(match string) string {
		start := strings.Index(match, "/")
		lead, path := match[:start], match[start:]
		// 尾随标点不属于路径：留着它，「<path>: 原因」这种可读边界才不会被吃掉。
		suffix := ""
		for len(path) > 1 && strings.ContainsRune(":,.;!?", rune(path[len(path)-1])) {
			suffix = string(path[len(path)-1]) + suffix
			path = path[:len(path)-1]
		}
		return lead + "<path>" + suffix
	})
}
